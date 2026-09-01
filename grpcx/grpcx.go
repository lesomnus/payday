// Package grpcx holds the plumbing the app is served with: what every call
// goes through before it reaches a service server.
package grpcx

import (
	"context"
	"time"

	"github.com/lesomnus/otx"
	"github.com/lesomnus/otx/otxgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
)

// Option is something [ServerOptions] is told.
type Option func(*options)

type options struct {
	deadline time.Duration
}

// WithDeadline caps a call that arrived without a deadline of its own. Zero
// caps nothing, and is a thing to mean rather than to end up with; see
// [Deadline] for why what is capped is the absence and never the presence.
//
// Unset is [DefaultTimeout]. A server that caps nothing is a decision, and a
// decision is something said rather than something left out.
func WithDeadline(d time.Duration) Option {
	return func(o *options) { o.deadline = d }
}

// ServerOptions returns the options the app is served with. Every call is
// traced, measured, recorded, given a deadline if it brought none and, if it
// panics, reported as an error rather than taken as a reason to end the
// process.
//
// Nothing here checks a request. What a request must be is the servers' to say,
// in Go, next to the rule it is part of; see the README, "What a request must
// say".
//
// The order matters, twice. [Otel] is registered before [Log], so a record
// carries the ids of the span it happened inside. And both are stats handlers
// rather than interceptors, which puts the record outside everything that
// follows -- a call that panicked, one that ran out of time and one that was
// refused before a handler was reached are all recorded like any other.
func ServerOptions(ctx context.Context, opts ...Option) []grpc.ServerOption {
	return Serving(ctx, opts...).ServerOptions()
}

// Serving is [ServerOptions] as the pieces, which is what everything that is
// not `grpc.NewServer` needs. See [Chain].
func Serving(ctx context.Context, opts ...Option) Chain {
	o := options{deadline: DefaultTimeout}
	for _, f := range opts {
		f(&o)
	}

	c := Chain{
		// Otel before Log so that a record carries the ids of the span it
		// happened inside.
		Stats:  []stats.Handler{otxgrpc.NewServerHandler(otx.From(ctx)), logHandler(ctx)},
		Unary:  []grpc.UnaryServerInterceptor{RecoverUnary()},
		Stream: []grpc.StreamServerInterceptor{RecoverStream()},
	}
	if o.deadline > 0 {
		c.Unary = append(c.Unary, DeadlineUnary(o.deadline))
	}

	return c
}

// Otel traces and measures the calls with the providers held by `ctx`, and
// hands what `ctx` carries to every call.
//
// The second half is what makes the first half reachable. gRpc builds the
// context of a call out of a background one, so without this a handler is
// served with nothing of what the app was started with and everything it logs
// or measures goes nowhere.
//
// The propagator comes from `ctx` as well, and it is what continues the trace
// a caller started rather than beginning one of its own. Note that this is the
// caller's word: see the note on trust in README.md before putting the server
// where anyone can reach it.
func Otel(ctx context.Context) grpc.ServerOption {
	return grpc.StatsHandler(otxgrpc.NewServerHandler(otx.From(ctx)))
}

// Seed puts something into the context of every call before anything else runs.
//
// It is a stats handler because that is the only place gRpc lets one do so, and
// the difference is not academic: an interceptor runs behind the handlers that
// read the context as a call arrives and is answered -- the log among them --
// so what an interceptor puts there is not something they ever see.
func Seed(f func(ctx context.Context) context.Context) grpc.ServerOption {
	return grpc.StatsHandler(ctxHandler{f})
}

// ctxHandler is a [stats.Handler] that does nothing but seed the context of a
// call.
type ctxHandler struct {
	f func(ctx context.Context) context.Context
}

func (h ctxHandler) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return h.f(ctx)
}

func (h ctxHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

func (ctxHandler) HandleRPC(context.Context, stats.RPCStats) {}

func (ctxHandler) HandleConn(context.Context, stats.ConnStats) {}

// serverStream carries a context of its own, since a [grpc.ServerStream] hands
// over the one it was made with.
type serverStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s serverStream) Context() context.Context {
	return s.ctx
}

// StreamWithContext returns `ss` with `ctx` as its context.
func StreamWithContext(ss grpc.ServerStream, ctx context.Context) grpc.ServerStream {
	if ss.Context() == ctx {
		return ss
	}

	return serverStream{ss, ctx}
}
