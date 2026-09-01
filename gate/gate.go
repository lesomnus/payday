// Package gate decides what the caller of a request may do with it.
//
// Who the caller is was settled before this: `auth` put it in the frame. What
// is left is whether that caller may see or change the thing being asked about,
// and payday's answer is one rule -- a tenant is a wall, and what is inside one
// is not visible from outside.
//
// # Most of that rule is stated here and enforced elsewhere
//
// Narrowing what a caller may see is a predicate, and a predicate belongs in
// the query, so the wall is generated onto the innermost server and never runs
// here. What is left in a gate layer is what is genuinely not a predicate:
// whether a tenant may be put up or taken down, and which tenant a holder may
// be added to. Both are about a row that does not exist yet, so there is
// nothing to narrow.
//
// # What is here and what is generated
//
// The judgement is here, as functions. The layer that calls them is generated
// into an app, because a layer is a `struct{ app.Overlay }` and `app.Server` is
// an interface generated per app -- payday cannot name it, and a type parameter
// does not help, since an overlay has to forward services payday has never
// heard of.
//
// So a reader looking for what a rule *is* comes here. What is generated is the
// wiring that calls it.
package gate

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

// ErrNotFound is what is answered about something the caller may not see --
// that it exists is itself something not to say -- and about something that is
// not there, which is the point of the two being the same answer.
//
// Most of the time nothing says it: what a caller may not see is a row the
// query did not match, and the server that ran the query says so. It is for the
// places that ask about a row before writing a different one.
func ErrNotFound(what string) error {
	return status.Errorf(codes.NotFound, "%s not found", what)
}

// ErrDeployment is the answer to putting a tenant up or taking one down.
//
// A tenant is the wall every other rule is about, so neither is something that
// happens from inside one -- and from behind this layer, inside one is the only
// place there is. Both are refused to **everybody**, the way the trail refuses
// its own writes, and for the same reason: it is not about who is asking, and
// no credential changes it.
//
// What does them is a server this layer is not in front of. An app builds one
// -- it is what puts the first tenant there before anything is served -- and a
// deployment that wants an operator's path serves that one somewhere only an
// operator can reach. The capability is then a server instance somebody was
// handed, which can be taken away, rather than a row somebody is, which cannot.
func ErrDeployment(what string) error {
	return status.Errorf(codes.Unimplemented,
		"a tenant is %s by whoever runs this deployment, "+
			"which is not something asked for from inside one", what)
}

// Actor is who the request is from.
//
// A request that got this far without a frame is a server built without
// anything in front of it that says who is calling, so it is refused rather
// than served as nobody.
func Actor(ctx context.Context) (*frame.Frame, error) {
	f, ok := frame.From(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "who is asking?")
	}

	return f, nil
}

// ByTenant is what a call is counted against, for a rate limit.
//
// The tenant, and not the holder or the credential: a tenant makes as many of
// either as it likes, so counting one of those is a limit anybody can raise by
// asking for another one. The other direction -- one runaway client inside a
// tenant starving the rest of it -- is a second key, not a different one.
//
// A call with no frame is not counted. It is one that got past authentication
// without being anybody, which means it is public, and public calls are the
// health check and the reflection listing.
func ByTenant() func(ctx context.Context, method string) string {
	return func(ctx context.Context, _ string) string {
		f, ok := frame.From(ctx)
		if !ok {
			return ""
		}

		return f.Tenant.String()
	}
}

// Call is what a [Policy] is asked about.
type Call struct {
	// Actor is who is asking, by identifier, and Tenant is who they are held
	// by.
	Actor  pdid.Id
	Tenant pdid.Id

	// Row is the actor as it was read, for a policy that knows what kind of
	// thing that is. It is the app's own type and the app owns both sides of
	// the assertion; see [frame.Frame.Row].
	Row any

	// Action is the RPC gRpc dispatched, by the name it knows it by.
	Action string
}

// Policy is what a deployment consults about a caller, and is the seam for
// anything payday does not decide.
//
// It is unset by default, and then the wall is what an app shows on its own:
// everybody sees their own tenant and nobody sees more. Injecting one is a
// deployment saying otherwise, which is why there is no identifier compared
// against a well-known one anywhere in here -- a privilege granted by being a
// particular row cannot be revoked, cannot be narrowed, and belongs to whoever
// finds the row.
type Policy interface {
	// May answers whether this call may be made at all, before anything is
	// read. An error is the caller's answer, so it may be a status.
	May(ctx context.Context, c Call) error

	// Where answers which tenants this caller may see. It is asked once per
	// call and read from the frame by everything after.
	Where(ctx context.Context, c Call) (frame.Tenants, error)
}
