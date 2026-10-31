package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/lesomnus/otx/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

// Resolver turns a claim into the frame a request is served in, for an app
// whose holders are memberships of accounts.
//
// A credential that names an account is served as one of its memberships: the
// one in the tenant the call names in [Resolver.Header], or its only one. For
// a method the schema marks as an account's own -- [Resolver.Methods] -- it is
// served as the account instead, whatever the call names ([frame.OfAccount]).
// Every other credential is [Resolver.Next]'s, as it was before there were
// accounts, so this goes in front of the resolver an app already has:
//
//	auth.Interceptor(handler, pd.AccountResolver(client, "acme-workspace", signedIn, Resolver(ungated)), public)
//
// # What it does not decide
//
// Which credentials name an account ([Resolver.Names]): that is what an app's
// sessions are, and payday keeps none. What a membership is served as: it asks
// [Resolver.Next] about the holder it found, so the frame is the one the app's
// own resolver answers for that holder, with the account put on it.
//
// # A credential bound to one tenant
//
// One whose [auth.Identity.TenantId] names a tenant is good there alone. It is
// served as the account's membership of that tenant, a header naming another
// is refused rather than followed, and it is not the account's own: a method
// an account calls as itself is refused to it, since that call is outside
// every tenant.
type Resolver struct {
	// Header is the metadata a call names its tenant in: the app's own word for
	// it, lower case as gRPC writes a key -- `acme-workspace`. Required.
	Header string

	// Names says whether a credential names an account rather than a holder,
	// and then its [auth.Identity.Id] is the account's identifier. Nil is
	// none: every credential is Next's, and no call is resolved to a
	// membership here.
	Names func(id auth.Identity) bool

	// Methods says which methods an account calls as itself, by the names gRPC
	// knows them by: the generated `pd.AccountMethod`. Nil is none, and every
	// call of an account is served as one of its memberships.
	Methods func(method string) bool

	// Memberships answers an account's memberships: the generated
	// `pd.Memberships`, on the deployment's own client. Required.
	Memberships Memberships

	// Next resolves every credential that names no account, and the
	// membership a call that names one was resolved to -- asked with that
	// holder's identifier as [auth.Identity.Id] and its tenant's as
	// [auth.Identity.TenantId], the rest of the credential as it came. It is
	// the app's own resolver, which reads the holder it is asked about.
	// Required.
	Next auth.Resolver
}

var _ auth.Resolver = Resolver{}

// errWiring is what a call is answered with when the resolver was not given
// what it needs, which is not the caller's to fix: the reason is in the log.
var errWiring = status.Error(codes.Internal, "could not say who is calling")

// Resolve answers with the frame `id` is served in. See [Resolver].
func (r Resolver) Resolve(ctx context.Context, id auth.Identity) (*frame.Frame, error) {
	if r.Next == nil {
		log.From(ctx).ErrorContext(ctx, "account: the resolver has no Next to resolve a holder with")
		return nil, errWiring
	}
	if r.Names == nil || !r.Names(id) {
		return r.Next.Resolve(ctx, id)
	}
	if err := r.check(); err != nil {
		log.From(ctx).ErrorContext(ctx, "account: the resolver cannot resolve an account", slog.Any("err", err))
		return nil, errWiring
	}

	a, err := pdid.Parse(id.Id)
	if err != nil || a.IsZero() {
		// A credential that names nobody who could be here is a bad one, which
		// is what [auth.Resolver] asks for: the caller goes and authenticates
		// again, rather than sending the same thing until it works.
		return nil, fmt.Errorf("%w: %q names no account", auth.ErrNoCredential, id.Id)
	}

	if m, ok := grpc.Method(ctx); ok && r.Methods != nil && r.Methods(m) {
		if id.TenantId != "" {
			return nil, status.Errorf(codes.PermissionDenied,
				"%s is the account's own, outside every tenant, and this credential is good in one", m)
		}

		// The grant is the credential's, which `auth` puts on; a resolver is
		// not asked for one and cannot widen one by answering.
		return frame.OfAccount(a, frame.Grant{}, r.Methods), nil
	}

	in, err := SelectorOf(ctx, r.Header)
	if err != nil {
		return nil, err
	}
	if id.TenantId != "" {
		bound, err := pdid.Parse(id.TenantId)
		if err != nil {
			return nil, fmt.Errorf("%w: names its tenant by %q, which is no identifier", auth.ErrNoCredential, id.TenantId)
		}
		if !in.Id.IsZero() && in.Id != bound {
			return nil, status.Errorf(codes.PermissionDenied, "this credential is good in one tenant, and %s is not it", in)
		}

		// Both, when the header named the tenant by alias: the membership has
		// to be of a tenant that is the bound one and is called that, which a
		// header naming another one is not.
		in.Id = bound
	}

	m, err := r.membership(ctx, a, in)
	if err != nil {
		return nil, err
	}

	f, err := r.Next.Resolve(ctx, auth.Identity{
		Authority: id.Authority,
		Changed:   id.Changed,
		Method:    id.Method,
		Id:        m.Holder.String(),
		TenantId:  m.Tenant.String(),
		Grant:     id.Grant,
		Expires:   id.Expires,
	})
	if err != nil {
		return nil, err
	}
	if f == nil || f.AsAccount() || f.Actor != m.Holder || f.Tenant != m.Tenant {
		// Not the membership it was asked about. Served as whoever that is,
		// a call would be somebody this account is not; refused, it is a
		// resolver to look at, which is the log's to say.
		log.From(ctx).ErrorContext(ctx, "account: the resolver answered for another than the membership it was asked about",
			slog.String("holder", m.Holder.String()), slog.String("tenant", m.Tenant.String()))
		return nil, errWiring
	}

	return f.WithAccount(a), nil
}

// membership is the holder of `a` a call is served as: the one in the tenant
// `in` names, or `a`'s only one when it names none.
func (r Resolver) membership(ctx context.Context, a pdid.Id, in Selector) (Membership, error) {
	// Two, which is all it takes to tell "the only one" from "one of several".
	vs, err := r.Memberships.Of(ctx, a, in, 2)
	if err != nil {
		return Membership{}, err
	}

	switch {
	case len(vs) == 1:
		if !in.Id.IsZero() && vs[0].Tenant != in.Id {
			log.From(ctx).ErrorContext(ctx, "account: memberships answered one of another tenant than the one asked about",
				slog.String("asked", in.Id.String()), slog.String("answered", vs[0].Tenant.String()))
			return Membership{}, errWiring
		}

		return vs[0], nil

	case !in.IsZero() && len(vs) == 0:
		return Membership{}, ErrNotIn(in)

	case !in.IsZero():
		return Membership{}, errTwice(in)

	case len(vs) == 0:
		return Membership{}, ErrInNone
	}

	return Membership{}, ErrNameOne(r.Header)
}

// check reports what the resolver lacks to resolve an account.
func (r Resolver) check() error {
	var errs []error
	if err := checkHeader(r.Header); err != nil {
		errs = append(errs, err)
	}
	if r.Memberships == nil {
		errs = append(errs, errors.New("account: no Memberships to find an account's holders in"))
	}

	return errors.Join(errs...)
}
