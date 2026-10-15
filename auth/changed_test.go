package auth_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type changedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s changedStream) Context() context.Context { return s.ctx }

func TestConfiguredIdentityCanEndItsStreams(t *testing.T) {
	changed := make(chan struct{})
	h := auth.HandlerFunc(func(context.Context) (auth.Identity, error) {
		return auth.Identity{Authority: "file", Tenant: "acme", Alias: "robot", Grant: frame.Whole(), Changed: changed}, nil
	})
	r := auth.ResolverFunc(func(context.Context, auth.Identity) (*frame.Frame, error) {
		f := frame.New(pdid.Nil, pdid.Nil, frame.Grant{})
		f.Name = "@acme/robot"
		f.TenantName = "acme"
		return f, nil
	})
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- auth.InterceptorStream(h, r, nil)(nil, changedStream{ctx: t.Context()}, &grpc.StreamServerInfo{FullMethod: getMethod}, func(_ any, ss grpc.ServerStream) error {
			f := frame.Must(ss.Context())
			require.Equal(t, "@acme/robot", f.Name)
			require.True(t, f.Actor.IsZero())
			close(ready)
			<-ss.Context().Done()
			return ss.Context().Err()
		})
	}()
	<-ready
	close(changed)
	require.Equal(t, codes.Unauthenticated, status.Code(<-done))
	_, err := serve(h, r, nil, t.Context(), getMethod)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
