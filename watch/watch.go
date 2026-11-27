// Package watch publishes what a call changed, once its transaction has
// committed.
//
// It is the other end of the hook the trail hangs off. Both are told about a
// write by the same recorder, called from inside the transaction that makes it,
// and they want opposite things from that moment: a trail row has to hold or
// fall **with** the write, so it is written there and then; an event has to be
// published only if the write **survived**, so nothing is published there at
// all. What the recorder does here is remember, and an interceptor publishes
// once the handler has answered without an error.
//
// # What "the transaction committed" means
//
// The handler returning is the signal. Every transaction a call opens is opened
// and committed below the interceptor, so by the time the handler answers,
// whatever it wrote is committed or gone with the error. A layer that held a
// transaction open past its own handler would break that, and nothing does.
//
// # Why a subscriber that falls behind is cut off
//
// Of the three things that can be done to a slow subscriber, only one is safe
// here. Waiting for it holds the request path until the slowest watcher catches
// up, which turns a slow consumer into a slow server. Skipping an event in
// silence leaves a watcher believing it has seen everything, which is worse
// than being cut off. Being disconnected is a thing a client can notice and act
// on: it reconnects, and the first message of a fresh stream is everything that
// matches now.
//
// # It is not an outbox
//
// An event is published in this process, to whoever is listening in this
// process, and a crash between the commit and the dispatch loses it. Something
// that must not be lost is written in the transaction, as a row somebody else
// picks up. See [Broker] for the seam a deployment that outgrows this replaces.
package watch

import (
	"context"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// Change is one write, as an event carries it.
//
// It is payday's own shape rather than the generated one, so that this package
// -- and anything a deployment plugs into [Broker] -- does not name an app's
// types. The generated recorder converts.
type Change struct {
	// Method is the RPC the caller asked for, and By is the RPC of the
	// generated server that actually wrote. They answer different questions: a
	// trail is read by somebody asking what was asked for, and anything acting
	// on the row itself -- a cache to drop, a stream to feed -- has to know
	// which entity and which kind of write, which is By.
	Method string
	By     string

	// Key is the row that changed.
	Key pdid.Id

	// Patch is the document the write was compiled from, marshaled, and empty
	// for a write that was not one.
	Patch []byte

	// Resource is the immutable mutation envelope for ResourceService.Watch.
	// Nil identifies a legacy key-only notification.
	Resource *pdpb.WatchEvent

	// Signal is application invalidation metadata carried between replicas.
	// It is separate from mutation patches, which may contain secrets, and is
	// never exposed by ResourceService.Watch without a Resource envelope.
	Signal []byte

	// Routes are server-authored application interests, in addition to By and
	// Key. Nil means an older publisher did not say: scoped subscribers must
	// still hear the change. A non-nil empty slice says there are no application
	// routes. Keep this distinction on the wire and in the outbox.
	Routes []Route
}

// Event is one call that changed something.
//
// Every message in it is shared with every subscriber and with the call that
// produced it. **Read it; do not write to it.** A subscriber that needs to keep
// one keeps a copy.
type Event struct {
	// Actor is who made the call and Tenant is who they were held by. Both are
	// zero for a call nobody vouched for -- a deployment writing to itself
	// before it serves anything.
	Actor  pdid.Id
	Tenant pdid.Id

	// Method is the RPC gRPC dispatched, which is what the caller asked for.
	// What each write actually did is in [Event.Changes].
	Method string

	// Request and Response are what the call carried.
	Request  proto.Message
	Response proto.Message

	// Changes is every write the call made, in the order it made them. A call
	// writes more than one row often enough to be the normal case -- adding a
	// tenant writes the holder that comes with it -- and only the first of them
	// is the response.
	Changes []Change

	// One event's context-independent interpretation can be reused by its local
	// subscribers. This is neither a durable cache nor an authorization cache.
	shared *sync.Map
}

// Broker is where events go, and is the seam a deployment replaces.
//
// The one payday ships publishes inside this process, which is right for one
// replica and **silently wrong for two**: a subscriber on one replica never
// hears about a write on another, and nothing reports it. That is the reason a
// deployment has to name its broker rather than getting one by default; see the
// note on `watch.broker` in the plan.
type Broker interface {
	// Publish hands an event to whoever is listening. It must not block the
	// call that produced it: the write already happened, and a subscriber that
	// cannot keep up is that subscriber's problem.
	Publish(ctx context.Context, v Event)

	// Subscribe answers with a channel of events and a function that stops it.
	// The channel is **closed** if this subscriber falls too far behind, which
	// is how a stream finds out it has to start again.
	Subscribe() (<-chan Event, func())
}

// Backlog is how many events a subscriber may fall behind by before the memory
// broker cuts it off.
//
// Generous, because falling behind costs a client its stream and it should take
// more than a moment of slowness. Not unbounded, because the whole point is
// that somebody eventually pays and it is not the write path.
const Backlog = 64

// Memory is a [Broker] that publishes inside this process.
//
// It is what a sandbox and a single replica want, and it is what a deployment
// of two replicas must not have. Nothing defaults to it -- an app names it.
func Memory() Broker { return &memory{} }

type memory struct {
	hub Fanout
}

func (b *memory) Subscribe() (<-chan Event, func()) {
	s, err := b.Open(context.Background(), Interest{})
	if err != nil {
		// Closed: a subscription that ends as it begins, as one that fell
		// behind does.
		c := make(chan Event)
		close(c)
		return c, func() {}
	}
	return s.Events, s.Close
}

// Close ends every subscription, and a subscription asked for afterwards ends
// as it is made; a publication after it reaches nobody. Twice is nothing.
//
// It holds no connection and no goroutine, so this is all there is to close.
// It is needed all the same: a memory broker is a scoped one, so a Watch hands
// out the broker's own subscriptions rather than sharing one of its own, and
// [Watch.Close] ends them only by closing the broker. Without it a server built
// twice in one process kept the first one's streams open on a broker nothing
// would publish to again.
func (b *memory) Close() error {
	b.hub.Close()
	return nil
}

func (b *memory) Publish(_ context.Context, v Event) {
	b.hub.Publish(v)
}

func (b *memory) Open(ctx context.Context, interest Interest) (*Subscription, error) {
	return b.hub.Open(ctx, interest)
}
