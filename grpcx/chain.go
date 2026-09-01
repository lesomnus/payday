package grpcx

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
)

// Chain is what every call goes through, as the pieces rather than as the
// options one particular server takes.
//
// The distinction is not bookkeeping. `[]grpc.ServerOption` is the shape
// `grpc.NewServer` wants and nothing else does, and payday has two other
// callers for the same chain:
//
//   - **A server that is not gRpc's.** A page runs the whole app compiled to
//     wasm and speaks a datagram protocol to the browser rather than Http/2.
//     That server takes the same `grpc.UnaryServerInterceptor` and the same
//     `stats.Handler` -- it is only the option wrapper that differs.
//   - **A batch of calls inside one call.** Whatever refuses a method, counts
//     it, or decides what its caller may see is keyed on the method gRpc
//     dispatched, and a batch arrives as one method carrying many. Applying
//     those per operation means calling them as functions, which is only
//     possible if they are functions.
//
// Both wanted the same thing, so the cost of having it is paid once. Options
// are built from this rather than the other way round.
type Chain struct {
	// Stats run outside everything, including the interceptors, which is why
	// what records a call lives here: a call that panicked, one that ran out
	// of time and one refused before a handler was reached are all recorded
	// like any other.
	Stats []stats.Handler

	Unary  []grpc.UnaryServerInterceptor
	Stream []grpc.StreamServerInterceptor
}

// With answers with this chain and `c` after it, which is how a deployment adds
// what only it knows about.
//
// Order is kept, and it is the order calls travel: the first interceptor added
// is the outermost.
func (v Chain) With(c Chain) Chain {
	v.Stats = append(append([]stats.Handler{}, v.Stats...), c.Stats...)
	v.Unary = append(append([]grpc.UnaryServerInterceptor{}, v.Unary...), c.Unary...)
	v.Stream = append(append([]grpc.StreamServerInterceptor{}, v.Stream...), c.Stream...)

	return v
}

// WithUnary answers with this chain and `is` after it, for the common case of
// adding interceptors and nothing else.
func (v Chain) WithUnary(is ...grpc.UnaryServerInterceptor) Chain {
	return v.With(Chain{Unary: is})
}

// WithStream answers with this chain and `is` after it.
func (v Chain) WithStream(is ...grpc.StreamServerInterceptor) Chain {
	return v.With(Chain{Stream: is})
}

// WithStats answers with this chain and `hs` after it.
func (v Chain) WithStats(hs ...stats.Handler) Chain {
	return v.With(Chain{Stats: hs})
}

// ServerOptions is this chain in the shape [grpc.NewServer] takes it.
//
// The chains are installed with the Chain* forms rather than the singular ones,
// so a caller that adds an interceptor of its own beside this does not silently
// replace what is here.
func (v Chain) ServerOptions() []grpc.ServerOption {
	opts := make([]grpc.ServerOption, 0, len(v.Stats)+2)
	for _, h := range v.Stats {
		opts = append(opts, grpc.StatsHandler(h))
	}
	if len(v.Unary) > 0 {
		opts = append(opts, grpc.ChainUnaryInterceptor(v.Unary...))
	}
	if len(v.Stream) > 0 {
		opts = append(opts, grpc.ChainStreamInterceptor(v.Stream...))
	}

	return opts
}
