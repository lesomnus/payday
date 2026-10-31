package account_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/account"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/batch"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

const (
	header      = "acme-workspace"
	ownMethod   = "/app.InvitationService/Accept"
	otherMethod = "/app.RobotService/List"
)

// directory is the app's rows as [account.Memberships] answers about them: a
// holder of an account in a tenant, and what each tenant is called.
type directory struct {
	held    []held
	aliases map[pdid.Id]string
}

type held struct {
	account pdid.Id
	account.Membership
}

func (d *directory) Of(_ context.Context, a pdid.Id, in account.Selector, limit int) ([]account.Membership, error) {
	var out []account.Membership
	for _, v := range d.held {
		switch {
		case v.account != a:
		case !in.Id.IsZero() && v.Tenant != in.Id:
		case in.Alias != "" && d.aliases[v.Tenant] != in.Alias:
		default:
			out = append(out, v.Membership)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}

	return out, nil
}

// holders is the app's resolver: who a holder is, by its identifier.
type holders struct {
	of    map[pdid.Id]pdid.Id // holder -> its tenant
	asked []auth.Identity
}

func (h *holders) Resolve(_ context.Context, id auth.Identity) (*frame.Frame, error) {
	h.asked = append(h.asked, id)

	k, err := pdid.Parse(id.Id)
	if err != nil {
		return nil, auth.ErrNoCredential
	}
	t, ok := h.of[k]
	if !ok {
		return nil, auth.ErrNoCredential
	}

	return frame.New(k, t, frame.Grant{}), nil
}

// world is two tenants, acme and hooli, and three accounts: one in both, one
// in acme only, and one in none.
type world struct {
	acme, hooli, globex pdid.Id

	both, one, none pdid.Id

	// The holders: both's in acme and in hooli, one's in acme.
	bothAcme, bothHooli, oneAcme pdid.Id

	dir     *directory
	holders *holders
	r       account.Resolver
}

func newWorld() *world {
	w := &world{
		acme: pdid.New(tenantDomain), hooli: pdid.New(tenantDomain), globex: pdid.New(tenantDomain),
		both: pdid.New(holderDomain), one: pdid.New(holderDomain), none: pdid.New(holderDomain),
		bothAcme: pdid.New(holderDomain), bothHooli: pdid.New(holderDomain), oneAcme: pdid.New(holderDomain),
	}
	w.dir = &directory{
		held: []held{
			{w.both, account.Membership{Holder: w.bothAcme, Tenant: w.acme}},
			{w.both, account.Membership{Holder: w.bothHooli, Tenant: w.hooli}},
			{w.one, account.Membership{Holder: w.oneAcme, Tenant: w.acme}},
		},
		aliases: map[pdid.Id]string{w.acme: "acme", w.hooli: "hooli", w.globex: "globex"},
	}
	w.holders = &holders{of: map[pdid.Id]pdid.Id{w.bothAcme: w.acme, w.bothHooli: w.hooli, w.oneAcme: w.acme}}
	w.r = account.Resolver{
		Header:      header,
		Names:       func(id auth.Identity) bool { return id.Method == "account" },
		Methods:     func(m string) bool { return m == ownMethod },
		Memberships: w.dir,
		Next:        w.holders,
	}

	return w
}

// calling is a call of `method` naming `tenant` in the header, or none.
func calling(method string, tenant ...string) context.Context {
	ctx := batch.AsOp(context.Background(), method)
	for _, v := range tenant {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(header, v))
	}

	return ctx
}

func signedIn(a pdid.Id) auth.Identity {
	return auth.Identity{Method: "account", Id: a.String(), Grant: frame.Whole()}
}

func TestACredentialThatNamesNoAccountIsTheNextResolversAsItCame(t *testing.T) {
	x := require.New(t)
	w := newWorld()

	id := auth.Identity{Method: "token", Id: w.oneAcme.String()}
	f, err := w.r.Resolve(calling(otherMethod, "hooli"), id)
	x.NoError(err)
	x.Equal(w.oneAcme, f.Actor)
	x.True(f.Account.IsZero(), "a holder's credential is no account's")
	x.Equal([]auth.Identity{id}, w.holders.asked, "asked as it came, header or none")
}

func TestAnAccountsOwnMethodIsServedAsTheAccount(t *testing.T) {
	x := require.New(t)
	w := newWorld()

	// Whatever the call names: the account's own method is outside every
	// tenant, and a page that names its tenant on every call names it here
	// too.
	for _, ctx := range []context.Context{calling(ownMethod), calling(ownMethod, "acme"), calling(ownMethod, "globex")} {
		f, err := w.r.Resolve(ctx, signedIn(w.both))
		x.NoError(err)
		x.True(f.AsAccount())
		x.Equal(w.both, f.Actor)
		x.Equal(w.both, f.Account)
		x.True(f.Tenant.IsZero())
		x.True(f.Calls(ownMethod))
		x.False(f.Calls(otherMethod))
	}
	x.Empty(w.holders.asked, "no membership is the account")

	// Even one that is nobody's member: what an account asks before it is one.
	f, err := w.r.Resolve(calling(ownMethod), signedIn(w.none))
	x.NoError(err)
	x.True(f.AsAccount())
}

func TestACallIsServedAsTheMembershipItNames(t *testing.T) {
	w := newWorld()

	for _, tc := range []struct {
		name   string
		of     pdid.Id
		header []string
		holder pdid.Id
		tenant pdid.Id
	}{
		{"by alias", w.both, []string{"hooli"}, w.bothHooli, w.hooli},
		{"by alias, with its @", w.both, []string{"@acme"}, w.bothAcme, w.acme},
		{"by identifier", w.both, []string{w.hooli.String()}, w.bothHooli, w.hooli},
		{"its only one, naming none", w.one, nil, w.oneAcme, w.acme},
		{"its only one, naming it", w.one, []string{"acme"}, w.oneAcme, w.acme},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)
			w.holders.asked = nil

			f, err := w.r.Resolve(calling(otherMethod, tc.header...), signedIn(tc.of))
			x.NoError(err)
			x.False(f.AsAccount())
			x.Equal(tc.holder, f.Actor)
			x.Equal(tc.tenant, f.Tenant)
			x.Equal(tc.of, f.Account, "the membership says whose it is")

			x.Len(w.holders.asked, 1)
			x.Equal(tc.holder.String(), w.holders.asked[0].Id)
			x.Equal(tc.tenant.String(), w.holders.asked[0].TenantId)
			x.Equal("account", w.holders.asked[0].Method, "the rest of the credential as it came")
		})
	}
}

// TestATenantTheAccountIsNotInIsRefusedAndNotRepointed.
//
// The one answer whether or not there is such a tenant -- a header is not a way
// to learn which tenants there are -- and never the call served as one of the
// account's memberships somewhere else.
func TestATenantTheAccountIsNotInIsRefusedAndNotRepointed(t *testing.T) {
	w := newWorld()

	for _, tc := range []struct {
		name string
		of   pdid.Id
		in   string
	}{
		{"one there is, by alias", w.one, "hooli"},
		{"one there is, by identifier", w.one, w.hooli.String()},
		{"one nobody is in", w.both, "globex"},
		{"one there is not", w.both, "initech"},
		{"one there is not, by identifier", w.both, pdid.New(tenantDomain).String()},
		{"a holder's identifier, which is no tenant's", w.both, w.bothAcme.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)
			w.holders.asked = nil

			_, err := w.r.Resolve(calling(otherMethod, tc.in), signedIn(tc.of))
			x.Equal(codes.PermissionDenied, status.Code(err))
			x.Contains(status.Convert(err).Message(), "this account is not in")
			x.Empty(w.holders.asked, "nobody was served")
		})
	}
}

func TestACallThatNamesNoTenantIsTheAccountsOnlyMembership(t *testing.T) {
	w := newWorld()

	t.Run("refused when it has several, which the caller has to choose between", func(t *testing.T) {
		x := require.New(t)

		_, err := w.r.Resolve(calling(otherMethod), signedIn(w.both))
		x.Equal(codes.InvalidArgument, status.Code(err))
		x.Contains(err.Error(), header, "says where to name one")
	})

	t.Run("refused when it has none", func(t *testing.T) {
		x := require.New(t)

		_, err := w.r.Resolve(calling(otherMethod), signedIn(w.none))
		x.Equal(codes.FailedPrecondition, status.Code(err))
		x.Empty(w.holders.asked)
	})

	t.Run("refused when the call names two", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(batch.AsOp(context.Background(), otherMethod), metadata.Pairs(header, "acme", header, "hooli"))
		_, err := w.r.Resolve(ctx, signedIn(w.both))
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("refused when it names nothing a tenant could be", func(t *testing.T) {
		_, err := w.r.Resolve(calling(otherMethod, "not a tenant"), signedIn(w.both))
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestAnAccountOfTwoMembershipsInOneTenantIsServedAsNeither(t *testing.T) {
	x := require.New(t)
	w := newWorld()

	again := pdid.New(holderDomain)
	w.dir.held = append(w.dir.held, held{w.one, account.Membership{Holder: again, Tenant: w.acme}})
	w.holders.of[again] = w.acme

	_, err := w.r.Resolve(calling(otherMethod, "acme"), signedIn(w.one))
	x.Equal(codes.FailedPrecondition, status.Code(err))
	_, err = w.r.Resolve(calling(otherMethod), signedIn(w.one))
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Empty(w.holders.asked)
}

// TestACredentialBoundToATenantIsGoodThereAlone.
//
// One that names the tenant it was given for is served as the account's
// membership there, refuses a header naming another rather than following it,
// and is not the account's own: an account's call as itself is outside every
// tenant.
func TestACredentialBoundToATenantIsGoodThereAlone(t *testing.T) {
	w := newWorld()
	bound := signedIn(w.both)
	bound.TenantId = w.hooli.String()

	t.Run("served as the membership there, naming nothing", func(t *testing.T) {
		x := require.New(t)

		f, err := w.r.Resolve(calling(otherMethod), bound)
		x.NoError(err)
		x.Equal(w.bothHooli, f.Actor)
	})

	t.Run("and naming it", func(t *testing.T) {
		x := require.New(t)

		f, err := w.r.Resolve(calling(otherMethod, "hooli"), bound)
		x.NoError(err)
		x.Equal(w.bothHooli, f.Actor)
	})

	for _, in := range []string{"acme", w.acme.String()} {
		t.Run("refusing another the account is in: "+in, func(t *testing.T) {
			_, err := w.r.Resolve(calling(otherMethod, in), bound)
			require.Equal(t, codes.PermissionDenied, status.Code(err))
		})
	}

	t.Run("and not the account's own", func(t *testing.T) {
		_, err := w.r.Resolve(calling(ownMethod), bound)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})
}

func TestACredentialThatNamesNoAccountIsABadOne(t *testing.T) {
	w := newWorld()

	for _, v := range []string{"", "nobody", pdid.Nil.String(), "0a1b2c3d-0000-4000-8000-000000000000"} {
		t.Run(v, func(t *testing.T) {
			_, err := w.r.Resolve(calling(otherMethod), auth.Identity{Method: "account", Id: v})
			require.ErrorIs(t, err, auth.ErrNoCredential)
		})
	}
}

// TestAResolverAnsweringForSomebodyElseServesNobody.
//
// Served as whoever the next resolver answered with, a call would be somebody
// this account is not. It is refused, and the log says why.
func TestAResolverAnsweringForSomebodyElseServesNobody(t *testing.T) {
	w := newWorld()

	for _, tc := range []struct {
		name string
		next auth.ResolverFunc
	}{
		{"another holder", func(context.Context, auth.Identity) (*frame.Frame, error) {
			return frame.New(w.oneAcme, w.acme, frame.Grant{}), nil
		}},
		{"the holder, in another tenant", func(_ context.Context, id auth.Identity) (*frame.Frame, error) {
			return frame.New(pdid.MustParse(id.Id), w.globex, frame.Grant{}), nil
		}},
		{"an account", func(context.Context, auth.Identity) (*frame.Frame, error) {
			return frame.OfAccount(w.both, frame.Grant{}, nil), nil
		}},
		{"nothing", func(context.Context, auth.Identity) (*frame.Frame, error) { return nil, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := w.r
			r.Next = tc.next
			_, err := r.Resolve(calling(otherMethod, "hooli"), signedIn(w.both))
			require.Equal(t, codes.Internal, status.Code(err))
		})
	}

	t.Run("and its refusal is the answer", func(t *testing.T) {
		r := w.r
		r.Next = auth.ResolverFunc(func(context.Context, auth.Identity) (*frame.Frame, error) {
			return nil, auth.ErrNoCredential
		})
		_, err := r.Resolve(calling(otherMethod, "hooli"), signedIn(w.both))
		require.ErrorIs(t, err, auth.ErrNoCredential)
	})
}

// TestAResolverMissingAPartRefusesAccountsAndNothingElse: what it was not
// given is the deployment's to fix, and a holder's credential needs none of it.
func TestAResolverMissingAPartRefusesAccountsAndNothingElse(t *testing.T) {
	w := newWorld()

	for _, tc := range []struct {
		name string
		edit func(r *account.Resolver)
	}{
		{"no header", func(r *account.Resolver) { r.Header = "" }},
		{"a header gRPC sends as bytes", func(r *account.Resolver) { r.Header = "acme-workspace-bin" }},
		{"a header not in lower case", func(r *account.Resolver) { r.Header = "Acme-Workspace" }},
		{"no memberships", func(r *account.Resolver) { r.Memberships = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			r := w.r
			tc.edit(&r)
			_, err := r.Resolve(calling(otherMethod, "acme"), signedIn(w.one))
			x.Equal(codes.Internal, status.Code(err))

			f, err := r.Resolve(calling(otherMethod), auth.Identity{Method: "token", Id: w.oneAcme.String()})
			x.NoError(err)
			x.Equal(w.oneAcme, f.Actor)
		})
	}

	t.Run("no next serves nobody at all", func(t *testing.T) {
		r := w.r
		r.Next = nil
		_, err := r.Resolve(calling(otherMethod), auth.Identity{Method: "token", Id: w.oneAcme.String()})
		require.Equal(t, codes.Internal, status.Code(err))
	})

	t.Run("no Names serves every credential as a holder's", func(t *testing.T) {
		x := require.New(t)

		r := w.r
		r.Names = nil
		_, err := r.Resolve(calling(otherMethod), signedIn(w.one))
		x.ErrorIs(err, auth.ErrNoCredential, "an account's identifier names no holder")
	})

	t.Run("no Methods serves every call of an account as a membership", func(t *testing.T) {
		x := require.New(t)

		r := w.r
		r.Methods = nil
		f, err := r.Resolve(calling(ownMethod), signedIn(w.one))
		x.NoError(err)
		x.False(f.AsAccount())
		x.Equal(w.oneAcme, f.Actor)
	})
}

// TestMembershipsAnsweringAnotherTenantServesNobody: a Memberships that
// answers about another tenant than the one asked about is one to look at,
// and a call served by its word would be in a tenant it did not name.
func TestMembershipsAnsweringAnotherTenantServesNobody(t *testing.T) {
	w := newWorld()
	r := w.r
	r.Memberships = wrong{w.dir}

	_, err := r.Resolve(calling(otherMethod, w.hooli.String()), signedIn(w.both))
	require.Equal(t, codes.Internal, status.Code(err))
}

type wrong struct{ d *directory }

func (v wrong) Of(ctx context.Context, a pdid.Id, _ account.Selector, limit int) ([]account.Membership, error) {
	vs, err := v.d.Of(ctx, a, account.Selector{}, limit)
	return slices.Clip(vs[:1]), err
}

func TestAMembershipsThatCannotAnswerIsTheAnswer(t *testing.T) {
	w := newWorld()
	r := w.r
	broken := errors.New("the database is away")
	r.Memberships = failing{broken}

	_, err := r.Resolve(calling(otherMethod), signedIn(w.both))
	require.ErrorIs(t, err, broken)
}

type failing struct{ err error }

func (v failing) Of(context.Context, pdid.Id, account.Selector, int) ([]account.Membership, error) {
	return nil, v.err
}
