package grpcx_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"

	"github.com/lesomnus/payday/grpcx"
)

// noop is a stats handler that does nothing, for counting.
type noop struct{}

func (noop) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context { return ctx }
func (noop) HandleRPC(context.Context, stats.RPCStats)                       {}
func (noop) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}
func (noop) HandleConn(context.Context, stats.ConnStats) {}

func pass(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	return h(ctx, req)
}

func passStream(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
	return h(srv, ss)
}

func TestChain(t *testing.T) {
	t.Run("keeps the order calls travel in", func(t *testing.T) {
		x := require.New(t)

		var order []string
		mark := func(name string) grpc.UnaryServerInterceptor {
			return func(ctx context.Context, req any, i *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
				order = append(order, name)
				return h(ctx, req)
			}
		}

		c := grpcx.Chain{Unary: []grpc.UnaryServerInterceptor{mark("first")}}.
			WithUnary(mark("second")).
			WithUnary(mark("third"))

		// The chain is run by gRPC, so it is assembled here the way gRPC does.
		var run grpc.UnaryHandler = func(context.Context, any) (any, error) { return nil, nil }
		for i := len(c.Unary) - 1; i >= 0; i-- {
			next, at := run, c.Unary[i]
			run = func(ctx context.Context, req any) (any, error) {
				return at(ctx, req, &grpc.UnaryServerInfo{}, next)
			}
		}
		_, err := run(t.Context(), nil)
		x.NoError(err)

		x.Equal([]string{"first", "second", "third"}, order,
			"the first added is the outermost")
	})

	t.Run("adding to one does not change the other", func(t *testing.T) {
		x := require.New(t)

		a := grpcx.Chain{Unary: []grpc.UnaryServerInterceptor{pass}}
		b := a.WithUnary(pass)

		x.Len(a.Unary, 1, "the chain it was built from was changed")
		x.Len(b.Unary, 2)
	})

	t.Run("becomes the options a gRPC server takes", func(t *testing.T) {
		x := require.New(t)

		c := grpcx.Chain{
			Stats:  []stats.Handler{noop{}, noop{}},
			Unary:  []grpc.UnaryServerInterceptor{pass, pass, pass},
			Stream: []grpc.StreamServerInterceptor{passStream},
		}

		// Two handlers, and one option each for the two chains however many
		// interceptors are in them -- which is the point of chaining rather
		// than adding an option per interceptor: a caller that installs one of
		// its own beside these does not replace them.
		x.Len(c.ServerOptions(), 4)

		// And it is something a server actually accepts.
		x.NotPanics(func() { grpc.NewServer(c.ServerOptions()...).Stop() })
	})

	t.Run("an empty chain asks for nothing", func(t *testing.T) {
		require.Empty(t, grpcx.Chain{}.ServerOptions())
	})
}

// TestServingIsTheChain is the reason [grpcx.Chain] exists: what a call goes
// through has to be reachable as functions, not only as the options one
// particular server constructor takes.
//
// A server compiled into a page speaks a datagram protocol rather than HTTP/2
// and takes the same `grpc.UnaryServerInterceptor`; a batch of calls inside one
// call has to apply them per operation. Neither can be handed
// `[]grpc.ServerOption`.
func TestServingIsTheChain(t *testing.T) {
	x := require.New(t)

	c := grpcx.Serving(t.Context())
	x.NotEmpty(c.Stats)
	x.NotEmpty(c.Unary)
	x.NotEmpty(c.Stream)

	x.Equal(len(c.ServerOptions()), len(grpcx.ServerOptions(t.Context())),
		"the options are built from the chain, so they say the same thing")
}
