package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestAuthenticationFactsReachUnaryAndStreamFrames(t *testing.T) {
	now := time.Now().UTC()
	who, tenant := pdid.New(200), pdid.New(200)
	shared := frame.New(who, tenant, frame.Whole())
	shared.Authentication = frame.Authentication{Method: "resolver", Date: now}
	resolve := auth.ResolverFunc(func(context.Context, auth.Identity) (*frame.Frame, error) { return shared, nil })
	for _, date := range []time.Time{now.Add(-time.Hour), {}} {
		identity := auth.Identity{Id: who.String(), Method: "session", DateAuthenticated: date, Grant: frame.Whole()}
		handle := auth.HandlerFunc(func(context.Context) (auth.Identity, error) { return identity, nil })
		// A header about proof is not proof. Only the verified identity reaches it.
		ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("auth_time", now.Format(time.RFC3339), "auth_method", "oidc"))
		var unary, stream *frame.Frame
		_, err := auth.InterceptorUnary(handle, resolve, nil)(ctx, nil, &grpc.UnaryServerInfo{FullMethod: getMethod}, func(ctx context.Context, _ any) (any, error) { unary, _ = frame.From(ctx); return nil, nil })
		require.NoError(t, err)
		err = auth.InterceptorStream(handle, resolve, nil)(nil, fakeStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: getMethod}, func(_ any, s grpc.ServerStream) error { stream, _ = frame.From(s.Context()); return nil })
		require.NoError(t, err)
		for _, got := range []*frame.Frame{unary, stream} {
			require.Equal(t, identity.Authentication(), got.Authentication)
			require.Equal(t, identity.Grant, got.Grant)
			require.False(t, got.Authentication.Fresh(now, 5*time.Minute))
		}
		require.Equal(t, frame.Authentication{Method: "resolver", Date: now}, shared.Authentication, "resolver state was mutated")
	}
}

func TestRepresentedPartiesKeepSeparateAuthenticationFacts(t *testing.T) {
	now := time.Now().UTC()
	person, device, tenant := pdid.New(200), pdid.New(200), pdid.New(200)
	identity := auth.Identity{Id: device.String(), Method: "mtls", DateAuthenticated: now, Grant: frame.Whole()}
	handler := auth.HandlerFunc(func(context.Context) (auth.Identity, error) { return identity, nil })
	deviceFrame := frame.New(device, tenant, frame.Whole())
	personFrame := frame.New(person, tenant, frame.Whole())
	personFrame.Authentication = frame.Authentication{Method: "session", Date: now.Add(-time.Hour)}
	resolver := auth.Represent(auth.ResolverFunc(func(context.Context, auth.Identity) (*frame.Frame, error) { return deviceFrame, nil }), func(_ context.Context, call auth.PresenceCall) (*frame.Frame, error) {
		require.Equal(t, identity.Authentication(), call.Caller.Authentication)
		return personFrame, nil
	})
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(auth.PresenceHeader, "presence", auth.SubjectHeader, person.String(), auth.RecordedHeader, now.Format(time.RFC3339Nano)))
	var got *frame.Frame
	_, err := auth.InterceptorUnary(handler, resolver, nil)(ctx, nil, &grpc.UnaryServerInfo{FullMethod: getMethod}, func(ctx context.Context, _ any) (any, error) { got, _ = frame.From(ctx); return nil, nil })
	require.NoError(t, err)
	require.Equal(t, personFrame.Authentication, got.Authentication)
	require.Equal(t, identity.Authentication(), got.Caller.Authentication)
	require.False(t, got.Authentication.Fresh(now, 5*time.Minute))
	require.True(t, got.Caller.Authentication.Fresh(now, 5*time.Minute))
	require.Nil(t, personFrame.Caller)
	require.Empty(t, deviceFrame.Authentication.Method)
	require.Equal(t, got.Caller.Authentication, got.Caller.Frame(got.Grant).Authentication)
}

func TestIntrospectionPreservesAuthenticationTime(t *testing.T) {
	date := time.Now().UTC().Add(-time.Hour)
	for _, date := range []time.Time{date, {}} {
		original := auth.Identity{Id: pdid.New(200).String(), Method: "apikey", DateAuthenticated: date, DateExpires: time.Now().UTC().Add(time.Hour), Grant: frame.Whole()}
		wire, err := auth.Introspection(original)
		require.NoError(t, err)
		got, err := auth.IdentityFrom(wire)
		require.NoError(t, err)
		require.True(t, date.Equal(got.DateAuthenticated))
		require.Empty(t, got.Method, "the receiving handler decides how the token arrived")
		require.Equal(t, original.Grant, got.Grant)
		if date.IsZero() {
			require.Nil(t, wire.GetDateAuthenticated())
		}
	}
	_, err := auth.IdentityFrom(pdpb.TokenIntrospectResponse_builder{DateAuthenticated: &timestamppb.Timestamp{Seconds: 1, Nanos: 1000000000}}.Build())
	require.ErrorContains(t, err, "date_authenticated")
	_, err = auth.Introspection(auth.Identity{DateAuthenticated: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)})
	require.ErrorContains(t, err, "date_authenticated")
}
