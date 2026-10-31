// Package frame carries who a request is from.
//
// A frame is put into the context once, by whatever authenticated the caller,
// and read from it by whatever has to decide what the caller may do or see. A
// context without one is a request nobody has vouched for.
//
// # It holds identifiers, not rows
//
// Almost everything that reads a frame wants an identifier: the wall narrows a
// query by tenant, the trail stamps who acted, a rate limit counts against
// somebody. The row itself is wanted in exactly one place -- an app's own
// policy, which knows its own types -- so that is the one thing carried
// untyped.
//
// The alternative is a type parameter, and it does not stay in one place: a
// generic Frame makes every function that touches one generic too, all the way
// out to the interceptors. Two identifiers and a [proto.Message] cost a type
// assertion in the one spot that needs the row, and nothing anywhere else.
//
// # A call an account makes as itself
//
// In an app whose holders are memberships of accounts an identity provider
// keeps (payday#184), a call is ordinarily served as one membership: the
// holder in [Frame.Actor], its tenant in [Frame.Tenant], and the account it is
// a membership of in [Frame.Account]. A few methods are called by the account
// itself, outside any tenant -- what it asks before it is anybody's member --
// and [OfAccount] is their frame: the account where a holder would be, and no
// tenant. It sees nothing until the layer serving it says what it reads, and
// it may be of no method but the ones it was made for; see [OfAccount].
package frame

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"
)

type key struct{}

// Frame is what is known about a request other than what it asks for.
type Frame struct {
	// Actor is who the request is from, by identifier. A verified configuration
	// principal can have no persistent actor; then Actor is Nil and Name
	// identifies it. Applications must require a persistent actor for writes
	// that record an owner or an audit identity.
	Actor pdid.Id

	// Tenant is who the actor is held by.
	Tenant pdid.Id

	// Name and TenantName are trusted names established by the resolver, never
	// unverified request headers. They let configuration principals be logged,
	// rate limited and authorized without manufacturing database identifiers.
	Name       string
	TenantName string

	// Row is the actor as it was read, for whatever knows what kind of thing
	// that is. It is nil for a request that has an identifier and nothing
	// else, which is what a credential naming somebody who was not loaded
	// looks like.
	//
	// An app that consults it type-asserts, and owns both sides of that.
	Row proto.Message

	// Grant is what the credential the caller came with allows, which is at
	// most what the Actor allows. See [Grant].
	Grant Grant

	// Scope is which tenants this request may see: what the Actor may see, met
	// with what the Grant allows of it.
	//
	// It is worked out once, in front, and read from here by everything after
	// rather than asked again -- the hooks that narrow a query run once per
	// *query* and a request makes several, so a scope computed there would
	// consult whatever answers it several times for one call.
	//
	// The zero value sees nothing, so a frame built by hand that says nothing
	// about this is a frame that reads no rows.
	Scope Tenants

	// Account is the account the caller signed in as, in an app whose holders
	// are memberships of accounts an identity provider keeps (payday#184), and
	// Nil in every other.
	//
	// A call served as one of the account's memberships holds the membership in
	// [Frame.Actor] and the account here, so that a layer can tell which
	// account a membership's call came from without reading the holder again.
	// A call the account makes as itself ([OfAccount]) holds the account here
	// and in [Frame.Actor] both, and no tenant.
	Account pdid.Id

	// asAccount says this is a call an account makes as itself. Only
	// [OfAccount] sets it, so no frame written out field by field is one, and
	// a frame that is one stays one through every copy.
	asAccount bool

	// calls is which methods a call framed as an account may be of; see
	// [OfAccount].
	calls func(method string) bool
}

// New answers with the frame of a request from `actor`, held by `tenant`,
// carrying a credential that allows `grant`.
//
// The grant is an argument rather than a field set afterwards because there is
// no safe thing for it to default to: [Grant]'s zero value allows nothing, so a
// caller who forgot builds a frame that can do nothing -- which is the right
// way round, and still better not left to whether somebody remembered.
func New(actor, tenant pdid.Id, grant Grant) *Frame {
	return &Frame{Actor: actor, Tenant: tenant, Grant: grant}
}

// WithScope answers with this frame, seeing `t`.
//
// It is set apart from [New] because it arrives later and from somewhere else:
// a credential says what it allows as it is read, and what a caller may see is
// worked out afterwards, by whatever holds those rules.
//
// A call an account makes as itself keeps [Nothing] whatever it is given: it
// is in no tenant, and what it reads is said by the layer serving it rather
// than by a scope (see [OfAccount]). A policy answering every tenant for it
// would otherwise be the one line that opened the whole deployment to a
// caller who is nobody's member.
func (f *Frame) WithScope(t Tenants) *Frame {
	v := *f
	v.Scope = t
	if v.asAccount {
		v.Scope = Nothing
	}

	return &v
}

// WithRow answers with this frame carrying `v` as the row the actor was read
// from.
func (f *Frame) WithRow(v proto.Message) *Frame {
	u := *f
	u.Row = v

	return &u
}

// OfAccount answers with the frame of a call `account` makes as itself,
// outside any tenant (payday#184): the account where a holder would be -- in
// [Frame.Actor] as well as [Frame.Account], so that the trail, the access
// record and the past name it as who acted -- and no tenant.
//
// `calls` is which methods it may be of, by the names gRPC knows them by:
// the generated `pd.AccountMethod`, which answers for the methods the schema
// marks `(payday.method) = {account: {}}`. Nil is none of them, which is the
// frame of a call that may do nothing at all. `gate.Decide` refuses it every
// other method, and the generated gate does again for every method it serves,
// so a frame made for one method is not a way into another.
//
// The grant is an argument for the reason it is one of [New]: a resolver is
// not asked for one, and `auth` puts the credential's own on the frame.
//
// # What it sees
//
// Nothing, by itself. Its scope is [Nothing] and stays so ([Frame.WithScope]),
// so a read of a tenant's rows is narrowed to none of them; and the generated
// wall answers every entity, global ones too, with nothing for it -- unless
// the layer serving the method said what it reads (`pd.AccountReads`). There
// is no tenant to narrow by, so what the wall would have said, the layer has
// to.
func OfAccount(account pdid.Id, grant Grant, calls func(method string) bool) *Frame {
	return &Frame{Actor: account, Account: account, Grant: grant, asAccount: true, calls: calls}
}

// AsAccount reports whether this is the frame of a call an account makes as
// itself; see [OfAccount].
func (f *Frame) AsAccount() bool { return f != nil && f.asAccount }

// Calls reports whether this frame may be of `method`: always for a call
// served as a membership, or as anybody else -- what such a caller may call is
// a policy's to say -- and for a call an account makes as itself, only when
// `method` is one the frame was made for.
func (f *Frame) Calls(method string) bool {
	if !f.AsAccount() {
		return true
	}

	return f.calls != nil && f.calls(method)
}

// WithAccount answers with this frame saying it is a membership of `account`:
// what a resolver adds to the frame of the membership a call was served as,
// once it has worked out which one that is.
//
// The frame of a call an account makes as itself is answered as it is. Its
// account is its actor, and one that held another would name two callers.
func (f *Frame) WithAccount(account pdid.Id) *Frame {
	if f.AsAccount() {
		return f
	}

	v := *f
	v.Account = account

	return &v
}

// AccountOf is the account of the call `ctx` belongs to, when it is framed as
// one making a call as itself. See [OfAccount].
func AccountOf(ctx context.Context) (pdid.Id, bool) {
	f, ok := From(ctx)
	if !ok || !f.AsAccount() {
		return pdid.Nil, false
	}

	return f.Account, true
}

func Into(ctx context.Context, v *Frame) context.Context {
	return context.WithValue(ctx, key{}, v)
}

func From(ctx context.Context) (*Frame, bool) {
	v, ok := ctx.Value(key{}).(*Frame)
	return v, ok
}

// Must is [From] for the places that are only reached behind authentication, so
// a missing frame is a wiring mistake rather than a request to refuse.
func Must(ctx context.Context) *Frame {
	v, ok := From(ctx)
	if !ok {
		panic("frame: no frame in context")
	}

	return v
}
