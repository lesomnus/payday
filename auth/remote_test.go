package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
)

// TestIntrospectionRoundTrip is the one test that says the wire form of a Grant
// keeps what a Grant means.
//
// It matters because of one shape: "every tenant" and "no tenant at all" are
// the same empty list, and only the flag beside it tells them apart.
//
// Confirmed by breaking it. With `any_action` left out of what `grantOf`
// writes, seven of these fail -- and the one worth naming is the pair below,
// where a token deliberately narrowed to nothing comes back allowing every
// method there is. The others fail loudly; that one is the shape a store would
// ship.
func TestIntrospectionRoundTrip(t *testing.T) {
	a, b := pdid.New(Tenant), pdid.New(Tenant)
	cell, other := pdid.New(Holder), pdid.New(Holder)

	for _, tc := range []struct {
		desc  string
		grant frame.Grant
	}{
		{"narrows nothing", frame.Whole()},
		{"narrows everything to nothing", frame.Grant{}},

		// The pair a list cannot tell apart. Both hold three empty slices and
		// they are opposites.
		{"every axis open", frame.Whole()},
		{"every axis named empty", frame.Whole().In().Within().To()},

		{"one tenant", frame.Whole().In(a)},
		{"two tenants", frame.Whole().In(a, b)},
		{"one set", frame.Whole().Within(cell)},
		{"two sets", frame.Whole().Within(cell, other)},
		{"one method", frame.Whole().To("/app.RobotService/Get")},
		{"two methods", frame.Whole().To("/app.RobotService/Get", "/app.RobotService/List")},
		{"all three narrowed", frame.Whole().In(a).Within(cell).To("/app.RobotService/Get")},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)

			res, err := auth.Introspection(auth.Identity{Grant: tc.grant})
			x.NoError(err)

			got, err := auth.IdentityFrom(res)
			x.NoError(err)

			// Every axis, by both halves of its pair -- a comparison that read
			// only the lists would call the two opposites equal.
			x.Equal(tc.grant.AnyTenant(), got.Grant.AnyTenant())
			x.Equal(tc.grant.TenantIds(), got.Grant.TenantIds())
			x.Equal(tc.grant.AnySet(), got.Grant.AnySet())
			x.Equal(tc.grant.SetIds(), got.Grant.SetIds())
			x.Equal(tc.grant.AnyAction(), got.Grant.AnyAction())
			x.Equal(tc.grant.Actions(), got.Grant.Actions())
			x.Equal(tc.grant.IsWhole(), got.Grant.IsWhole())
		})
	}

	t.Run("the two a list cannot tell apart survive", func(t *testing.T) {
		x := require.New(t)

		every, err := auth.Introspection(auth.Identity{Grant: frame.Whole()})
		x.NoError(err)
		none, err := auth.Introspection(auth.Identity{Grant: frame.Whole().In().Within().To()})
		x.NoError(err)

		// Indistinguishable by what they list, on the wire as in the struct.
		x.Empty(every.GetGrant().GetTenants())
		x.Empty(every.GetGrant().GetActions())
		x.Empty(none.GetGrant().GetTenants())
		x.Empty(none.GetGrant().GetActions())

		// And told apart anyway.
		x.True(every.GetGrant().GetAnyAction())
		x.False(none.GetGrant().GetAnyAction())

		one, err := auth.IdentityFrom(every)
		x.NoError(err)
		two, err := auth.IdentityFrom(none)
		x.NoError(err)

		x.True(one.Grant.Allows("/app.RobotService/Get"))
		x.False(two.Grant.Allows("/app.RobotService/Get"))
	})
}

// TestIntrospectionSilence is what an answer that left something out means.
//
// Every one of these is a store with a bug, and what is being fixed is which
// way the bug fails. A grant nobody filled in has to allow nothing: a token
// that does nothing is a bug reported within the minute, and a token that does
// everything is one nobody reports at all.
func TestIntrospectionSilence(t *testing.T) {
	t.Run("no grant at all allows nothing", func(t *testing.T) {
		x := require.New(t)

		id, err := auth.IdentityFrom(pdpb.TokenIntrospectResponse_builder{
			Alias: "somebody",
		}.Build())
		x.NoError(err)

		x.False(id.Grant.IsWhole())
		x.False(id.Grant.AnyTenant())
		x.False(id.Grant.AnySet())
		x.False(id.Grant.AnyAction())
		x.False(id.Grant.Allows("/app.RobotService/Get"))
	})

	t.Run("an empty grant message allows nothing", func(t *testing.T) {
		x := require.New(t)

		id, err := auth.IdentityFrom(pdpb.TokenIntrospectResponse_builder{
			Alias: "somebody",
			Grant: pdpb.Grant_builder{}.Build(),
		}.Build())
		x.NoError(err)

		x.False(id.Grant.Allows("/app.RobotService/Get"))
	})
}

func TestIntrospectionIdentity(t *testing.T) {
	who, tenant := pdid.New(Holder), pdid.New(Tenant)

	t.Run("by identifier", func(t *testing.T) {
		x := require.New(t)

		at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		res, err := auth.Introspection(auth.Identity{
			Id:       who.String(),
			TenantId: tenant.String(),
			Grant:    frame.Whole(),
			Expires:  at,
		})
		x.NoError(err)
		x.Equal(who.Bytes(), res.GetId())
		x.Equal(tenant.Bytes(), res.GetTenantId())

		got, err := auth.IdentityFrom(res)
		x.NoError(err)
		x.Equal(who.String(), got.Id)
		x.Equal(tenant.String(), got.TenantId)
		x.True(at.Equal(got.Expires))
	})

	t.Run("by alias", func(t *testing.T) {
		x := require.New(t)

		res, err := auth.Introspection(auth.Identity{
			Tenant: "acme",
			Alias:  "admin",
			Grant:  frame.Whole(),
		})
		x.NoError(err)

		got, err := auth.IdentityFrom(res)
		x.NoError(err)
		x.Equal("acme", got.Tenant)
		x.Equal("admin", got.Alias)
		x.Empty(got.Id)

		// No expiry is a credential that does not stop working, and it has to
		// survive as that rather than as the zero time meaning "long ago".
		x.True(got.Expires.IsZero())
	})

	t.Run("bytes that are not an identifier are refused", func(t *testing.T) {
		x := require.New(t)

		_, err := auth.IdentityFrom(pdpb.TokenIntrospectResponse_builder{
			Id: []byte{1, 2, 3},
		}.Build())
		x.Error(err)
	})
}

// introspector is a [pdpb.TokenServiceClient] that answers however the test
// says, so that [auth.Remote] can be asked what it makes of each answer without
// a server.
type introspector struct {
	pdpb.TokenServiceClient

	res *pdpb.TokenIntrospectResponse
	err error
}

func (c introspector) Introspect(ctx context.Context, in *pdpb.TokenIntrospectRequest, opts ...grpc.CallOption) (*pdpb.TokenIntrospectResponse, error) {
	if c.err != nil {
		return nil, c.err
	}

	return c.res, nil
}

// TestRemoteRefusals is which refusals are about the token and which are not.
//
// The division is the whole of what this store decides, and getting it wrong is
// an outage that reads as every user typing the wrong password at once: a
// `PermissionDenied` means this **app** may not introspect, and passed on as
// "no such token" it says nothing about the one line of configuration that
// would fix it.
func TestRemoteRefusals(t *testing.T) {
	for _, tc := range []struct {
		desc string
		code codes.Code
		bad  bool // the token is what was wrong
	}{
		{"no such token", codes.NotFound, true},

		{"this app may not ask", codes.PermissionDenied, false},
		{"this app's own credential is bad", codes.Unauthenticated, false},
		{"the store is down", codes.Unavailable, false},
		{"the store did not answer in time", codes.DeadlineExceeded, false},
		{"the store is overloaded", codes.ResourceExhausted, false},
		{"the store broke", codes.Internal, false},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			x := require.New(t)

			s := auth.Remote(introspector{err: status.Error(tc.code, "no")})
			_, err := s.Lookup(t.Context(), "rk_whatever")
			x.Error(err)

			if tc.bad {
				x.ErrorIs(err, auth.ErrUnknownToken)
				x.False(errors.Is(err, auth.ErrUnavailable))

				return
			}

			x.ErrorIs(err, auth.ErrUnavailable)
			x.False(errors.Is(err, auth.ErrUnknownToken))

			// And the code survives into the message, which is the only thing
			// an operator has to tell these apart by.
			x.Contains(err.Error(), tc.code.String())
		})
	}

	t.Run("an answer is read", func(t *testing.T) {
		x := require.New(t)

		who := pdid.New(Holder)
		res, err := auth.Introspection(auth.Identity{
			Id:    who.String(),
			Grant: frame.Whole().To("/app.RobotService/Get"),
		})
		x.NoError(err)

		id, err := auth.Remote(introspector{res: res}).Lookup(t.Context(), "rk_whatever")
		x.NoError(err)
		x.Equal(who.String(), id.Id)
		x.True(id.Grant.Allows("/app.RobotService/Get"))
		x.False(id.Grant.Allows("/app.RobotService/Erase"))
	})
}
