// Package account is the account layer: what an app whose holders are
// memberships of accounts needs to serve a call as one of them (payday#184).
//
// # Why there is such a thing
//
// In payday a [frame.Frame] is who a request is from, and a holder is inside
// one tenant. Some apps have people who sign in once and belong to many
// tenants: an account of an identity provider's directory, with a membership
// -- a holder -- in each tenant it is in. A session names the account, and a
// call has to be served as one of its memberships; and a few calls are the
// account's own, made before it is anybody's member or across all of its
// tenants at once. Each such app built this layer itself. This is it, once.
//
// # What payday keeps, and what it does not
//
// The reference from a membership to its account -- a holder field the app
// names, `option (payday.app) = {accounts: ...}` -- the frame of a call an
// account makes as itself ([frame.OfAccount]), and resolving a call to one
// membership ([Resolver]). Not the account: its names, its addresses, how it
// signs in and whether it may are the identity provider's, and copying them
// into every app is one more place a person has to be erased from. An account
// is an identifier here and nothing else.
//
// # Which membership a call is
//
// A call names its tenant in a header the app chooses -- by alias, with or
// without its `@`, or by identifier ([Selector]) -- and is served as the
// account's membership there. Naming a tenant the account is not in is
// refused, and is the one refusal whether or not that tenant exists: a header
// is not a way to find out which tenants there are, and it is never pointed
// somewhere else instead. A call that names none is served as the account's
// only membership, and refused when it has none or several -- the second is a
// caller who has to say which.
//
// # A call an account makes as itself
//
// The methods the schema marks `(payday.method) = {account: {}}` are served as
// the account, whatever the call names: no tenant, and the account where a
// holder would be. Such a call reaches those methods and no others -- the gate
// refuses the rest -- and reads **nothing** through the walled servers, global
// rows included, until the layer serving it declares what it reads
// (`pd.AccountReads`). The wall has no tenant to narrow by, so what it would
// have said, the layer has to; `pd doctor` reports a method whose layer does
// not.
//
// The access record names the account as who made such a call, and the trail
// and the past name it as who acted, with no tenant beside it -- which no
// membership's row has, a holder being always some tenant's.
package account

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"
)

// Membership is one holder of an account: its membership of one tenant.
type Membership struct {
	Holder pdid.Id
	Tenant pdid.Id
}

// Memberships answers which holders an account has, out of the app's own rows.
// The generated `pd.Memberships` is one, reading the holder field the schema
// names past the wall: working out who is calling happens before there is
// anybody to be walled by.
type Memberships interface {
	// Of answers with at most `limit` of the memberships `account` has that are
	// still there, oldest first: the ones in the tenant `in` names -- by its
	// identifier, its alias, or both, when both have to hold -- or in any
	// tenant when `in` is the zero [Selector]. A limit of zero or less is no
	// limit.
	//
	// It answers about one account and never about the Nil one, which is
	// nobody's: a holder that names no account is no account's membership.
	Of(ctx context.Context, account pdid.Id, in Selector, limit int) ([]Membership, error)
}

// Selector is the tenant a call names: by identifier, by alias, or -- for a
// credential bound to one tenant, which a header then names again -- both,
// when both have to hold of one tenant. The zero value names none.
type Selector struct {
	Id    pdid.Id
	Alias string
}

// IsZero reports whether this names no tenant.
func (s Selector) IsZero() bool { return s.Id.IsZero() && s.Alias == "" }

// String is the selector as a person wrote it: the alias with its `@`, or the
// identifier.
func (s Selector) String() string {
	if s.Alias != "" {
		return "@" + s.Alias
	}
	if !s.Id.IsZero() {
		return s.Id.String()
	}

	return ""
}

// ParseSelector reads what a call wrote to name its tenant: an identifier, or
// an alias with or without its `@`, folded the way an alias is stored. Blank
// is the zero Selector, which names none.
//
// What is neither is refused, with InvalidArgument: it names no tenant at all,
// and the answer to one that names a tenant somebody else is in is a
// different one ([ErrNotIn]).
func ParseSelector(v string) (Selector, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return Selector{}, nil
	}
	if id, err := pdid.Parse(v); err == nil {
		if id.IsZero() {
			return Selector{}, status.Error(codes.InvalidArgument, "the Nil identifier names no tenant")
		}

		return Selector{Id: id}, nil
	}

	a, err := slug.ParseAlias(strings.TrimPrefix(v, "@"))
	if err != nil {
		return Selector{}, status.Errorf(codes.InvalidArgument, "%q names no tenant: neither an identifier nor an alias (%s)", v, err)
	}

	return Selector{Alias: a}, nil
}

// SelectorOf is the tenant the call `ctx` belongs to names in `header`, and
// the zero Selector when it names none.
//
// A header written twice is refused rather than either of its values taken:
// two tenants named is no tenant named, and taking the first is taking
// whichever a proxy happened to put first.
func SelectorOf(ctx context.Context, header string) (Selector, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return Selector{}, nil
	}

	vs := md.Get(header)
	switch len(vs) {
	case 0:
		return Selector{}, nil
	case 1:
		return ParseSelector(vs[0])
	}

	return Selector{}, status.Errorf(codes.InvalidArgument, "%s names %d tenants, and a call is served in one", header, len(vs))
}

// ErrNotIn is the answer to a call that names a tenant its account has no
// membership of.
//
// One answer whether or not there is such a tenant, so that a header says
// nothing about which tenants there are; and a refusal, never the call served
// as some other membership instead.
func ErrNotIn(in Selector) error {
	return status.Errorf(codes.PermissionDenied, "this account is not in %s", in)
}

// ErrInNone is the answer to a call that names no tenant, from an account that
// is no tenant's member.
var ErrInNone = status.Error(codes.FailedPrecondition, "this account is in no tenant")

// ErrNameOne is the answer to a call that names no tenant, from an account in
// more than one: which of them is the caller's to say, in `header`.
func ErrNameOne(header string) error {
	return status.Errorf(codes.InvalidArgument, "this account is in more than one tenant: name one, in %s", header)
}

// errTwice is a tenant an account holds two memberships of, which is not one a
// call can be served as without guessing which.
func errTwice(in Selector) error {
	return status.Errorf(codes.FailedPrecondition, "this account has more than one membership of %s, and a call is served as one", in)
}

// checkHeader refuses a header no call can name a tenant in: gRPC folds a key
// to lower case, sends one ending `-bin` as bytes, and keeps `grpc-` for
// itself.
func checkHeader(v string) error {
	switch {
	case v == "":
		return fmt.Errorf("account: no header names a call's tenant")
	case v != strings.ToLower(v):
		return fmt.Errorf("account: %q is not a metadata key, which is lower case", v)
	case strings.HasSuffix(v, "-bin"):
		return fmt.Errorf("account: %q is binary metadata, and a tenant is named in text", v)
	case strings.HasPrefix(v, "grpc-"):
		return fmt.Errorf("account: %q is gRPC's own", v)
	}

	return nil
}
