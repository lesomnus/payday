package grpcx_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/grpcx"
)

type req struct{ name string }
type res struct{ id string }

func add(_ context.Context, v *req) (*res, error) { return &res{id: "made:" + v.name}, nil }

// TestRunUnary is the seam a generated layer calls, on its own.
func TestRunUnary(t *testing.T) {
	ctx := t.Context()

	t.Run("with none, the call is the call", func(t *testing.T) {
		x := require.New(t)

		v, err := grpcx.RunUnary(ctx, nil, nil, "/app.RobotService/Add", &req{name: "arm-01"}, add)
		x.NoError(err)
		x.Equal("made:arm-01", v.id)
	})

	t.Run("it is handed the method and the server", func(t *testing.T) {
		x := require.New(t)

		var info *grpc.UnaryServerInfo
		srv := "the next server"
		v, err := grpcx.RunUnary(ctx, func(ctx context.Context, r any, i *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			info = i
			return next(ctx, r)
		}, srv, "/app.RobotService/Add", &req{name: "arm-02"}, add)

		x.NoError(err)
		x.Equal("made:arm-02", v.id)
		x.Equal("/app.RobotService/Add", info.FullMethod)
		x.Equal(srv, info.Server)
	})

	t.Run("a refusal is the answer, and the call never ran", func(t *testing.T) {
		x := require.New(t)

		ran := false
		want := errors.New("not today")
		v, err := grpcx.RunUnary(ctx, func(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
			return nil, want
		}, nil, "/app.RobotService/Add", &req{}, func(ctx context.Context, v *req) (*res, error) {
			ran = true
			return add(ctx, v)
		})

		x.ErrorIs(err, want)
		x.Nil(v)
		x.False(ran)
	})

	// An interceptor is free to answer instead of calling on -- a cache is the
	// example -- and one that answers something else is a mistake this cannot
	// fix. What it must not do is panic inside generated code the app did not
	// write, so the answer is the zero value.
	t.Run("an answer of the wrong type is nil rather than a panic", func(t *testing.T) {
		x := require.New(t)

		v, err := grpcx.RunUnary(ctx, func(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
			return "not a *res", nil
		}, nil, "/app.RobotService/Add", &req{}, add)

		x.NoError(err)
		x.Nil(v)
	})
}

// TestChainUnary, whose order is the order calls travel.
func TestChainUnary(t *testing.T) {
	x := require.New(t)

	x.Nil(grpcx.ChainUnary(nil), "none is nil, so a caller can tell")

	var order []string
	mark := func(name string) grpc.UnaryServerInterceptor {
		return func(ctx context.Context, r any, i *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			order = append(order, name+" in")
			v, err := next(ctx, r)
			order = append(order, name+" out")

			return v, err
		}
	}

	v, err := grpcx.RunUnary(t.Context(), grpcx.ChainUnary([]grpc.UnaryServerInterceptor{mark("a"), mark("b")}),
		nil, "/app.RobotService/Add", &req{name: "arm-03"}, add)

	x.NoError(err)
	x.Equal("made:arm-03", v.id)
	x.Equal([]string{"a in", "b in", "b out", "a out"}, order)
}
