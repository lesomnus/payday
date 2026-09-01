package grpcx_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/grpcx"
)

func TestGeneralWrite(t *testing.T) {
	x := require.New(t)

	x.True(grpcx.GeneralWrite("/go_app.HolderService/Patch"))
	x.True(grpcx.GeneralWrite("/go_app.HolderService/Apply"))

	x.False(grpcx.GeneralWrite("/go_app.HolderService/Add"))
	x.False(grpcx.GeneralWrite("/go_app.HolderService/Get"))
	x.False(grpcx.GeneralWrite("/go_app.HolderService/Erase"))
	x.False(grpcx.GeneralWrite("/go_app.HolderService/List"))

	// The name an app writes by hand for the thing it wants a caller to be
	// able to do, which is implemented *with* Apply and is not it.
	x.False(grpcx.GeneralWrite("/go_app.HolderService/Rename"))

	// Not a prefix match on the service, either: a service whose name ends in
	// the word is still a service.
	x.False(grpcx.GeneralWrite("/go_app.PatchService/Add"))
}

// call runs `method` through the interceptor and reports whether the handler
// ran at all, and what the caller was told.
func call(t *testing.T, method string) (bool, error) {
	t.Helper()

	var ran bool
	_, err := grpcx.ClosedUnary(grpcx.GeneralWrite)(
		t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: method},
		func(context.Context, any) (any, error) {
			ran = true
			return nil, nil
		})

	return ran, err
}

func TestClosed(t *testing.T) {
	t.Run("a closed method never reaches its handler", func(t *testing.T) {
		x := require.New(t)

		ran, err := call(t, "/go_app.HolderService/Apply")
		x.False(ran)

		// Unimplemented and not PermissionDenied: this is not about who is
		// asking, and no credential changes it.
		x.Equal(codes.Unimplemented, status.Code(err))
	})

	t.Run("everything else is served", func(t *testing.T) {
		x := require.New(t)

		ran, err := call(t, "/go_app.HolderService/Add")
		x.True(ran)
		x.NoError(err)
	})

	t.Run("nothing named closes nothing", func(t *testing.T) {
		x := require.New(t)

		// Not an interceptor that lets everything through -- no interceptor.
		x.Nil(grpcx.Closed(nil))
	})
}

// TestNothingClosedIsNothingClosed is the same hole [TestNoLimitIsNoLimit] is
// about, one interceptor over.
//
// The guard lived in the option constructor, which answers with no options at
// all when there is nothing to close. Everything that takes the interceptors as
// functions -- a chain for a server that is not gRpc's, a batch applying them
// per operation -- reaches them without going past it.
func TestNothingClosedIsNothingClosed(t *testing.T) {
	x := require.New(t)

	served := false
	_, err := grpcx.ClosedUnary(nil)(t.Context(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/a.B/Apply"},
		func(context.Context, any) (any, error) { served = true; return nil, nil })
	x.NoError(err)
	x.True(served)

	served = false
	err = grpcx.ClosedStream(nil)(nil, nil,
		&grpc.StreamServerInfo{FullMethod: "/a.B/Watch"},
		func(any, grpc.ServerStream) error { served = true; return nil })
	x.NoError(err)
	x.True(served)
}
