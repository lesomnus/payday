// Package auth says who a caller is.
//
// It is two steps, kept apart because they change for different reasons. A
// [Handler] reads whatever the transport carries - a header, a certificate, a
// token - and says who the caller claims to be. A [Resolver] looks that claim
// up and answers with the frame the request is served in, or with nothing.
// What the second one hands back is what the request is served as, and it
// comes from the database rather than from the caller.
//
// Three handlers are written here, and they differ in one thing: where the
// name comes from.
//
//	Plain   the caller writes it in a header, and is believed
//	MTLS    the certificate the connection was made with carries it
//	Bearer  the token carries nothing, and is exchanged for a name
//
// The first two read a name that is already there, so they are pure functions
// of the request. [Bearer] has to ask something, which is what makes it the
// interesting one: it can fail in a way the caller did not cause, and
// [ErrUnavailable] is how it says so.
//
// [Plain] believes whatever it is told, and is for development and for tests.
// It must not be reachable by anyone who is not already trusted to say the
// truth -- a deployment that serves it where anybody can reach it has no
// authentication at all, whatever else is configured.
//
// # What this package cannot name
//
// payday owns the schema and the app owns the Go types that come out of it, so
// the actor a credential names has no name here; see docs/SCHEMA.md. What is
// left is what this package talks in, and it is deliberately little: an
// [Identity] is a tenant alias, an actor alias, or an identifier, as text, and
// a [frame.Frame] is two identifiers, a grant, and a row that nothing here
// looks inside.
//
// That is also why the second step is an interface rather than a function.
// Looking an actor up is a query against the app's own generated servers, so
// the app writes it. What payday supplies is the shape of the question, the
// shape of the answer, and the interceptor that puts the answer where
// everything behind it can read it -- see [Resolver] and [Interceptor].
//
// # What a credential says, and what it does not
//
// Every handler here answers the same question -- who is this. What a caller
// may then do is the wall's, and whatever a deployment puts above it, and this
// package has nothing to say about it.
//
// What a credential *can* say is that it should be able to do less than the
// actor it names. A token meaning "john, but only for reading" needs somewhere
// to put the "only", and that is [frame.Grant]: a set of tenants and a set of
// methods, carried on the frame beside the actor and intersected with whatever
// the wall decides. It is an attenuation and never a widening -- a token that
// says "every tenant" held by somebody who may see one still sees one.
//
// Only [Bearer] can carry one, because only a token has anywhere to put one. A
// header and a certificate name somebody and stop, so [Plain] and [MTLS]
// answer [frame.Whole] and say so.
//
// What issues such a token, and who decided what it should be narrowed to, is
// not here and should not be. payday reads credentials and enforces them; it
// does not mint them. [MemTokenStore] is a sample of the shape, and a real one
// is a table or an issuer.
package auth

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/lesomnus/payday/frame"
)

// ErrNoCredential is what a handler reports when the request carries nothing
// it knows how to read. It is not a refusal: the next handler is asked, and if
// none of them find anything the call is Unauthenticated.
//
// A credential that is there but wrong is a different thing entirely, and must
// be reported as itself so that it is not read as "nobody asked".
var ErrNoCredential = io.EOF

// ErrUnavailable is what a handler reports when it could not find out whether
// the credential is good -- the store it would have asked is not answering.
//
// It is a third answer, and it exists because a handler that has to look
// something up can fail in a way the caller did not cause. Told
// Unauthenticated, a caller throws away a token that is perfectly good and
// goes to get another one, from the thing that is already down. Told
// Unavailable, it waits.
//
// It does not fall through to the next handler either. Somebody presented a
// credential; serving them as whatever the next handler makes of them would be
// answering a question nobody asked, possibly as somebody else.
var ErrUnavailable = errors.New("auth: cannot say whether the credential is good")

// Identity is who a caller claims to be, before anything has been looked up.
//
// It holds text and nothing else, which is the whole of what a credential can
// say: an actor is named either by its identifier or by its alias within a
// tenant, and both of those are what somebody wrote. Nothing here has read a
// row, so nothing here is a row.
type Identity struct {
	// Method is what the claim was read from, for the log and for nothing
	// else. A rule that turns on the way somebody authenticated is a rule that
	// will be wrong one day.
	Method string

	// Tenant is the alias of whoever holds the actor, and Alias is the actor's
	// name within it. They come as a pair: an alias with no tenant names one
	// row in every tenant there is, which is nobody in particular.
	Tenant string
	Alias  string

	// Id is the identifier the credential named, when it named one directly,
	// written the way [pdid.Id.String] writes one -- and empty when the
	// credential gave an alias instead. Whatever fills it owes a [Resolver] a
	// string [pdid.Parse] can read: [ParseName] parses before it writes, and a
	// [TokenStore] that hands out identifiers owes the same.
	//
	// Text like the rest, and this is the one field where that costs
	// something: it was parsed here and whatever resolves it parses it again.
	// It is kept anyway, because what a resolver does with it is build a query
	// -- against a UUID column, against a request field of bytes -- so the
	// parsed value is not the shape either end holds, and one currency for the
	// three ways a credential can name somebody is worth more than the one
	// call it would save.
	Id string

	// TenantId is the tenant the credential said holds the actor, when it said
	// so by identifier, and empty otherwise -- which is nearly always. It is
	// [Identity.Tenant]'s other half, the way [Identity.Id] is [Identity.Alias]'s.
	//
	// Nothing resolves anybody with it. It is a **claim to be disagreed with**:
	// a resolver reads the row and knows which tenant really holds the actor,
	// and [Interceptor] refuses the call when the two differ. So a credential
	// cannot move an actor between tenants by saying it has moved, and one that
	// is merely out of date is refused rather than served under the tenant it
	// used to be in.
	//
	// That is why it is separate from Tenant rather than sharing it. A resolver
	// asked about `@acme/admin` looks up both halves together, so the alias is
	// checked by being used. An identifier resolves on its own and the tenant
	// beside it would never be read -- which is exactly the credential a device
	// certificate is, and exactly the check its issuer meant to be making.
	TenantId string

	// Grant is what this credential allows, which is at most what the actor it
	// names allows. A handler that reads a credential with nowhere to carry an
	// attenuation answers [frame.Whole]; see [frame.Grant].
	Grant frame.Grant

	// Expires is when this credential stops working, and the zero time is one
	// that does not. Only a [TokenStore] fills it: a header and a certificate
	// have nowhere to carry an expiry.
	//
	// It is here rather than being the store's private business because a
	// **stream** needs it. A unary call carries its credential every time and
	// is refused at the next one; a stream reads it once, at the handshake, and
	// without this would go on being served for as long as it stays open --
	// which for a Watch is until somebody hangs up. See [Identity.Valid] and
	// the note on cutting a stream in [InterceptorStream].
	Expires time.Time
}

// Valid reports whether this credential is still good at `at`.
//
// An identity with no expiry is always good, which is what a header and a
// certificate are: neither has anywhere to carry one, and a certificate's own
// expiry is the transport's business rather than this package's.
//
// What needs it is a **stream**. A call carries its credential every time and
// finds out at the next one; a stream carries it once at the handshake and
// would otherwise go on being served forever. See [InterceptorStream].
func (v Identity) Valid(at time.Time) bool {
	return v.Expires.IsZero() || at.Before(v.Expires)
}

// NamesNobody reports whether this identity says who the caller is at all.
//
// A tenant on its own does not: it says which tenant an alias would have been
// read under, and there is no alias.
func (v Identity) NamesNobody() bool { return v.Id == "" && v.Alias == "" }

// Handler reads a claim out of the context a call is served with.
type Handler interface {
	// Handle returns who the caller claims to be. It wraps
	// [ErrNoCredential] if the request carries nothing it can read.
	Handle(ctx context.Context) (Identity, error)
}

type HandlerFunc func(ctx context.Context) (Identity, error)

func (f HandlerFunc) Handle(ctx context.Context) (Identity, error) {
	return f(ctx)
}

// Seq asks each handler in turn and takes the first claim any of them finds. A
// handler that finds nothing is passed over; one that finds something wrong,
// or that cannot tell, stops the search.
//
// That is what makes a fallback safe. `Seq(Bearer(store), MTLS())` means "the
// token if there is one, otherwise the certificate" -- not "the token, and if
// anything at all goes wrong, the certificate". A token that is expired, or
// one this server cannot check right now, must not quietly become a different
// caller with different authority.
func Seq(hs ...Handler) Handler {
	return HandlerFunc(func(ctx context.Context) (Identity, error) {
		for _, h := range hs {
			v, err := h.Handle(ctx)
			if err == nil {
				return v, nil
			}
			if !errors.Is(err, ErrNoCredential) {
				return Identity{}, err
			}
		}

		return Identity{}, ErrNoCredential
	})
}

// Resolver turns a claim into the frame the request is served in. **The app
// implements it**, and it is the seam this package is arranged around.
//
// It cannot be written here. Answering "who is @acme/admin" is a query against
// the app's own generated servers, whose types payday cannot name, and there is
// no type parameter that fixes that -- see docs/SCHEMA.md, "It is a contract,
// not a Go type". So payday says what the question is and what the answer must
// hold, the app says how to find it, and [Interceptor] is what joins them.
//
// What the frame must carry:
//
//   - [frame.Frame.Actor] and [frame.Frame.Tenant], which is the whole reason
//     for asking. They are read from the database and never from the claim: a
//     caller who says "@acme/admin" is telling this app where to look, not what
//     to answer.
//   - [frame.Frame.Row], if the app has a policy that wants the row it read.
//     Nothing in payday looks inside it.
//
// What it must not bother with is [frame.Frame.Grant]: what a credential
// allows was read out of the credential, and [Interceptor] puts it on the
// frame itself. A resolver cannot widen a credential by answering with
// [frame.Whole], and cannot break one by forgetting.
//
// [frame.Frame.Scope] is nobody's business here either. It is the meet of what
// the actor may see with what the grant allows, and it is worked out by
// whatever holds the rules about who may see whom, which runs after this.
type Resolver interface {
	// Resolve returns the frame the identity is served in, or an error
	// wrapping [ErrNoCredential] if there is no such actor. A caller that
	// names somebody who does not exist is no better off than one that named
	// nobody.
	//
	// A store that could not be reached wraps [ErrUnavailable], for the reason
	// written there: it is not the caller's fault and they must not be sent to
	// fetch a new credential over it.
	Resolve(ctx context.Context, id Identity) (*frame.Frame, error)
}

type ResolverFunc func(ctx context.Context, id Identity) (*frame.Frame, error)

func (f ResolverFunc) Resolve(ctx context.Context, id Identity) (*frame.Frame, error) {
	return f(ctx, id)
}

// Provider puts a credential into the context of an outgoing call, which is
// the other half of a [Handler]. It is what a client of this app, or this app
// calling another one, uses.
type Provider interface {
	Provide(ctx context.Context) context.Context
}

type ProviderFunc func(ctx context.Context) context.Context

func (f ProviderFunc) Provide(ctx context.Context) context.Context {
	return f(ctx)
}
