package watch

import (
	"context"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/grpcx"
)

// Watch is the two halves of publishing an event, which are installed in two
// different places and are useless apart.
//
// [Watch.Note] is called by the generated recorder, where writes happen.
// [Watch.Interceptor] goes on the server, where calls end. Install the
// interceptor alone and every event has no changes; install the recorder alone
// and nothing is published at all, since there is nowhere for it to remember
// into. Neither is an error and both are wiring somebody can read.
type Watch struct {
	to Broker
}

func New(to Broker) *Watch { return &Watch{to: to} }

// Broker answers with where this publishes, which is what anything else that
// has events to publish needs -- the outbox drainer is the one that does.
//
// It is nil for a deployment that named no broker, and everything here goes on
// working: [Watch.Note] still remembers and the interceptor still finds nothing
// to publish to.
func (w *Watch) Broker() Broker { return w.to }

// Note is what the generated recorder calls, from inside the transaction that
// makes the write.
//
// It only remembers. Publishing there would announce a write that a later
// failure in the same call is about to undo -- which is the difference between
// this and the trail, and the reason they hang off one hook and do opposite
// things with it.
func (w *Watch) Note(ctx context.Context, c Change) {
	if v, ok := use(ctx); ok {
		v.add(c)
	}
}

// Interceptor publishes one event per call that changed something.
//
// Unary only, and for the reason a default deadline is: a stream has no single
// response to publish and no single moment to publish it at. A stream that
// wants to say what it did says it itself.
func (w *Watch) Interceptor() grpcx.Chain {
	return grpcx.Chain{Unary: []grpc.UnaryServerInterceptor{w.Unary()}}
}

func (w *Watch) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		vs := &changes{}
		ctx = into(ctx, vs)

		res, err := handler(ctx, req)
		if err != nil {
			// Nothing is published for a call that failed. What it wrote was
			// undone with it -- or, for a call that joined a transaction
			// somebody else began, is theirs to undo.
			return res, err
		}

		w.publish(ctx, req, res, info.FullMethod, vs.taken())
		return res, nil
	}
}

// Subscribe is what a Watch Rpc listens on.
//
// The nil case answers with a channel nothing will ever send on, which is
// honest only because nobody reaches it any more: [Stream] refuses a Watch
// with no broker before it subscribes, and that is where the answer belongs.
// See [ErrNoBroker] for what this shape used to do to a client.
func (w *Watch) Subscribe() (<-chan Event, func()) {
	if w.to == nil {
		c := make(chan Event)
		return c, func() { close(c) }
	}

	return w.to.Subscribe()
}

// publish sends the event, and sends none for a call that changed nothing. A
// read is most of what a server does and is not news.
func (w *Watch) publish(ctx context.Context, req, res any, method string, cs []Change) {
	if w.to == nil || len(cs) == 0 {
		return
	}

	v := Event{
		Method:   method,
		Request:  message(req),
		Response: message(res),
		Changes:  cs,
	}
	if f, ok := frame.From(ctx); ok {
		v.Actor = f.Actor
		v.Tenant = f.Tenant
	}

	// Detached from cancellation. The write already happened, and a caller who
	// hung up is not a reason for it to go unannounced.
	w.to.Publish(context.WithoutCancel(ctx), v)
}

// message is `v` as something a subscriber can read, and nothing for a value
// that is not a message. Every request and response is one; the signature is
// `any` because that is what an interceptor is handed.
func message(v any) proto.Message {
	m, _ := v.(proto.Message)
	return m
}

type key struct{}

func into(ctx context.Context, v *changes) context.Context {
	return context.WithValue(ctx, key{}, v)
}

func use(ctx context.Context) (*changes, bool) {
	v, ok := ctx.Value(key{}).(*changes)
	return v, ok
}

// changes is what one call wrote, gathered as it wrote it.
//
// Guarded, because nothing promises a handler writes from one goroutine. It is
// held by pointer in the context so that a recorder running several layers down
// adds to what the interceptor will read.
type changes struct {
	mu sync.Mutex
	vs []Change
}

func (c *changes) add(v Change) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.vs = append(c.vs, v)
}

// taken answers with what was written and leaves nothing behind, so that the
// slice handed to every subscriber is one nothing else appends to.
func (c *changes) taken() []Change {
	c.mu.Lock()
	defer c.mu.Unlock()

	vs := c.vs
	c.vs = nil

	return vs
}
