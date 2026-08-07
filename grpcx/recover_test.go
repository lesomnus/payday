package grpcx_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/grpcx"
)

// stream is a [grpc.ServerStream] that is nothing but a context; the embedded
// interface is nil, so any other method panics if it is ever called.
type stream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s stream) Context() context.Context {
	return s.ctx
}

func TestRecoverUnary(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: "/go_app.TenantService/Add"}

	t.Run("a panic is reported as an internal error", func(t *testing.T) {
		x := require.New(t)

		v, err := grpcx.RecoverUnary()(t.Context(), nil, info, func(ctx context.Context, req any) (any, error) {
			panic("no")
		})
		x.ErrorContains(err, "internal error")
		x.Equal(codes.Internal, status.Code(err))
		x.Nil(v)

		// The client is not told what happened.
		x.NotContains(status.Convert(err).Message(), "no")
	})
	t.Run("an error is left as it is", func(t *testing.T) {
		x := require.New(t)

		expected := status.Error(codes.NotFound, "Tenant not found")
		_, err := grpcx.RecoverUnary()(t.Context(), nil, info, func(ctx context.Context, req any) (any, error) {
			return nil, expected
		})
		x.Equal(expected, err)
	})
	t.Run("a result is left as it is", func(t *testing.T) {
		x := require.New(t)

		v, err := grpcx.RecoverUnary()(t.Context(), nil, info, func(ctx context.Context, req any) (any, error) {
			return "ok", nil
		})
		x.NoError(err)
		x.Equal("ok", v)
	})
}

func TestRecoverStream(t *testing.T) {
	info := &grpc.StreamServerInfo{FullMethod: "/go_app.UserService/Watch"}

	t.Run("a panic is reported as an internal error", func(t *testing.T) {
		x := require.New(t)

		ss := stream{ctx: t.Context()}
		err := grpcx.RecoverStream()(nil, ss, info, func(srv any, ss grpc.ServerStream) error {
			panic(errors.New("no"))
		})
		x.Equal(codes.Internal, status.Code(err))
	})
	t.Run("an error is left as it is", func(t *testing.T) {
		x := require.New(t)

		ss := stream{ctx: t.Context()}
		expected := status.Error(codes.Canceled, "canceled")
		err := grpcx.RecoverStream()(nil, ss, info, func(srv any, ss grpc.ServerStream) error {
			return expected
		})
		x.Equal(expected, err)
	})
}
