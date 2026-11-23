package auth_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
)

func TestPresenceIsVerifiedAgainstTheCredentialHolder(t *testing.T) {
	x := require.New(t)
	device, person, tenant := pdid.New(200), pdid.New(200), pdid.New(200)
	grant := frame.Whole().In(tenant)
	h := auth.HandlerFunc(func(context.Context) (auth.Identity, error) {
		return auth.Identity{Id: device.String(), TenantId: tenant.String(), Grant: grant}, nil
	})
	base := auth.ResolverFunc(func(context.Context, auth.Identity) (*frame.Frame, error) {
		return frame.New(device, tenant, frame.Whole()), nil
	})
	at := time.Now().Add(-time.Hour).UTC()
	md := metadata.Pairs(auth.PresenceHeader, "session-a", auth.SubjectHeader, person.String(), auth.RecordedHeader, at.Format(time.RFC3339Nano))
	var seen auth.PresenceCall
	verify := func(_ context.Context, c auth.PresenceCall) (*frame.Frame, error) {
		seen = c
		if c.Reference != "session-a" || c.Caller.Actor != device {
			return nil, status.Error(codes.PermissionDenied, "not this device's presence")
		}
		return frame.New(person, tenant, frame.Whole()), nil
	}
	call := func(md metadata.MD, r auth.Resolver) (*frame.Frame, error) {
		var got *frame.Frame
		_, err := auth.InterceptorUnary(h, r, nil)(metadata.NewIncomingContext(t.Context(), md), &emptypb.Empty{}, &grpc.UnaryServerInfo{FullMethod: getMethod}, func(ctx context.Context, _ any) (any, error) { got, _ = frame.From(ctx); return nil, nil })
		return got, err
	}
	r := auth.Represent(base, verify)
	got, err := call(md, r)
	x.NoError(err)
	x.Equal(person, got.Actor)
	x.Equal(device, got.Caller.Actor)
	x.Equal("session-a", got.Presence)
	x.Equal(grant, got.Grant)
	x.False(got.Scope.All())
	x.Empty(got.Scope.Ids())
	x.Equal(device, seen.Caller.Actor)
	x.Equal(getMethod, seen.Method)
	x.IsType(&emptypb.Empty{}, seen.Request)
	x.True(seen.Recorded.Equal(at))
	x.Equal(grant, seen.Credential.Grant)
	for _, tc := range []struct {
		name string
		md   metadata.MD
		r    auth.Resolver
		code codes.Code
	}{
		{"different person's presence", metadata.Pairs(auth.PresenceHeader, "session-a", auth.SubjectHeader, device.String()), r, codes.PermissionDenied},
		{"unbound reference", metadata.Pairs(auth.PresenceHeader, "session-b", auth.SubjectHeader, person.String()), r, codes.PermissionDenied},
		{"duplicated reference", metadata.Pairs(auth.PresenceHeader, "session-a", auth.PresenceHeader, "session-a", auth.SubjectHeader, person.String()), r, codes.PermissionDenied},
		{"bad time", metadata.Pairs(auth.PresenceHeader, "session-a", auth.SubjectHeader, person.String(), auth.RecordedHeader, "yesterday"), r, codes.PermissionDenied},
		{"no verifier", md, base, codes.Unauthenticated},
		{"verifier unavailable", md, auth.Represent(base, func(context.Context, auth.PresenceCall) (*frame.Frame, error) { return nil, auth.ErrUnavailable }), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := call(tc.md, tc.r)
			require.Nil(t, got)
			require.Equal(t, tc.code, status.Code(err))
		})
	}
	got, err = call(nil, r)
	x.NoError(err)
	x.Equal(device, got.Actor)
	x.Nil(got.Caller)
}

func TestPresenceBoundsTheStreamIndependentlyOfTheDeviceCredential(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "expiry", true: "revocation"}[revoke], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				person, device, tenant := pdid.New(200), pdid.New(200), pdid.New(200)
				changed := make(chan struct{})
				credentialChanged := make(chan struct{})
				h := auth.HandlerFunc(func(context.Context) (auth.Identity, error) {
					return auth.Identity{Id: device.String(), Grant: frame.Whole(), Changed: credentialChanged, DateExpired: time.Now().Add(time.Hour)}, nil
				})
				base := auth.ResolverFunc(func(context.Context, auth.Identity) (*frame.Frame, error) {
					return frame.New(device, tenant, frame.Whole()), nil
				})
				r := auth.Represent(base, func(_ context.Context, c auth.PresenceCall) (*frame.Frame, error) {
					require.Nil(t, c.Request)
					f := frame.New(person, tenant, frame.Whole())
					f.PresenceExpires = time.Now().Add(10 * time.Minute)
					if revoke {
						f.PresenceChanged = changed
					}
					return f, nil
				})
				ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(auth.PresenceHeader, "presence", auth.SubjectHeader, person.String()))
				start := time.Now()
				if revoke {
					time.AfterFunc(time.Minute, func() { close(changed) })
				}
				err := auth.InterceptorStream(h, r, nil)(nil, fakeStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: getMethod}, func(_ any, s grpc.ServerStream) error { <-s.Context().Done(); return s.Context().Err() })
				require.Equal(t, codes.Unauthenticated, status.Code(err))
				want := 10 * time.Minute
				if revoke {
					want = time.Minute
				}
				require.Equal(t, want, time.Since(start))
			})
		})
	}
}
