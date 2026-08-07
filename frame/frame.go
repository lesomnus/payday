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
package frame

import (
	"context"

	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"
)

type key struct{}

// Frame is what is known about a request other than what it asks for.
type Frame struct {
	// Actor is who the request is from, by identifier. It is what was read
	// from the database rather than what the caller said about itself.
	Actor pdid.Id

	// Tenant is who the actor is held by.
	Tenant pdid.Id

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
func (f *Frame) WithScope(t Tenants) *Frame {
	v := *f
	v.Scope = t

	return &v
}

// WithRow answers with this frame carrying `v` as the row the actor was read
// from.
func (f *Frame) WithRow(v proto.Message) *Frame {
	u := *f
	u.Row = v

	return &u
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
