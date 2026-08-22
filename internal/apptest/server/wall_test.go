package server_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/pdid"
)

// TestWallIsOnByDeclaration is the whole checkpoint.
//
// Nothing in this app writes a wall. There is no `wall.go`, no method per
// entity, no embedded default that says "no opinion". There is a line in each
// entity's schema saying whether it is behind the wall, and everything below
// follows from it.
func TestWallIsOnByDeclaration(t *testing.T) {
	t.Run("a caller sees their own tenant's rows", func(t *testing.T) {
		x := require.New(t)
		a := New(t)
		ctx := t.Context()

		acme := a.tenantOf(ctx, x, "acme")
		robot := a.robotOf(ctx, x, acme, "arm-01")

		v, err := a.Walled.Robot().Get(As(ctx, acme), app.RobotGetRequest_builder{
			Ref: app.RobotRef_builder{Id: robot.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal("arm-01", v.GetAlias())
	})

	t.Run("and not another's", func(t *testing.T) {
		x := require.New(t)
		a := New(t)
		ctx := t.Context()

		acme := a.tenantOf(ctx, x, "acme")
		other := a.tenantOf(ctx, x, "other")
		theirs := a.robotOf(ctx, x, other, "arm-01")

		// NotFound and not PermissionDenied. A row out of the wall is a row
		// the query did not match, and that it exists is itself something not
		// to say.
		_, err := a.Walled.Robot().Get(As(ctx, acme), app.RobotGetRequest_builder{
			Ref: app.RobotRef_builder{Id: theirs.Bytes()}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})

	t.Run("a tenant is inside itself and no other", func(t *testing.T) {
		x := require.New(t)
		a := New(t)
		ctx := t.Context()

		acme := a.tenantOf(ctx, x, "acme")
		other := a.tenantOf(ctx, x, "other")

		_, err := a.Walled.Tenant().Get(As(ctx, acme), app.TenantGetRequest_builder{
			Ref: app.TenantRef_builder{Id: acme.Bytes()}.Build(),
		}.Build())
		x.NoError(err)

		_, err = a.Walled.Tenant().Get(As(ctx, acme), app.TenantGetRequest_builder{
			Ref: app.TenantRef_builder{Id: other.Bytes()}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})
}

// TestWallReachesThroughEdges is `via: "robot.tenant"` -- an entity that does
// not hold its own tenant and is behind the wall anyway.
//
// It is the case a hand-written wall gets wrong quietly: the method is easy to
// write for the entity that holds a tenant and easy to forget for the one that
// reaches it through something else.
func TestWallReachesThroughEdges(t *testing.T) {
	x := require.New(t)
	a := New(t)
	ctx := t.Context()

	acme := a.tenantOf(ctx, x, "acme")
	other := a.tenantOf(ctx, x, "other")
	theirs := a.robotOf(ctx, x, other, "arm-01")

	v, err := a.Ungated.Joint().Add(ctx, app.JointAddRequest_builder{
		Robot: app.RobotRef_builder{Id: theirs.Bytes()}.Build(),
		Alias: "elbow",
	}.Build())
	x.NoError(err)

	joint, err := pdid.From(v.GetId())
	x.NoError(err)

	// Two steps out, and the wall is still on it.
	_, err = a.Walled.Joint().Get(As(ctx, acme), app.JointGetRequest_builder{
		Ref: app.JointRef_builder{Id: joint.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err))

	_, err = a.Walled.Joint().Get(As(ctx, other), app.JointGetRequest_builder{
		Ref: app.JointRef_builder{Id: joint.Bytes()}.Build(),
	}.Build())
	x.NoError(err)
}

// TestGlobalIsNotBehindTheWall is the other half of the declaration: an entity
// that said out loud it is shared.
func TestGlobalIsNotBehindTheWall(t *testing.T) {
	x := require.New(t)
	a := New(t)
	ctx := t.Context()

	acme := a.tenantOf(ctx, x, "acme")
	other := a.tenantOf(ctx, x, "other")

	v, err := a.Ungated.Fleet().Add(ctx, app.FleetAddRequest_builder{Alias: "east"}.Build())
	x.NoError(err)

	for _, who := range []pdid.Id{acme, other} {
		_, err := a.Walled.Fleet().Get(As(ctx, who), app.FleetGetRequest_builder{
			Ref: app.FleetRef_builder{Id: v.GetId()}.Build(),
		}.Build())
		x.NoError(err)
	}
}

// TestNoFrameIsRefused is a decision an earlier system arrived at the hard
// way: a request nobody vouched for is refused rather than served as
// everybody.
//
// The scope that means "everything" is not reachable by forgetting. What has
// to go around the wall goes around it by being handed a server the wall was
// never installed on, which is a wiring decision somebody can read.
func TestNoFrameIsRefused(t *testing.T) {
	x := require.New(t)
	a := New(t)
	ctx := t.Context()

	acme := a.tenantOf(ctx, x, "acme")
	robot := a.robotOf(ctx, x, acme, "arm-01")

	_, err := a.Walled.Robot().Get(ctx, app.RobotGetRequest_builder{
		Ref: app.RobotRef_builder{Id: robot.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.Unauthenticated, status.Code(err))
}

// TestScopeOfNothingSeesNothing is what an empty list has to mean.
//
// `IDIn()` with nothing renders as WHERE FALSE. Read the other way round --
// "no tenants, so nothing to narrow by" -- a scope would open up as it ran out,
// and the caller who may see the least would see the most.
func TestScopeOfNothingSeesNothing(t *testing.T) {
	x := require.New(t)
	a := New(t)
	ctx := t.Context()

	acme := a.tenantOf(ctx, x, "acme")
	robot := a.robotOf(ctx, x, acme, "arm-01")

	_, err := a.Walled.Robot().Get(As(ctx), app.RobotGetRequest_builder{
		Ref: app.RobotRef_builder{Id: robot.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err))
}

// TestAnAddedEntityArrivesWalled is the checkpoint question, written down.
//
// `Cell` was added to the schema after everything else here was working. What
// it cost was a message and a `(payday.entity)` option -- no Go, no method, no
// line anywhere in this app. This test is what says the wall was on it anyway.
//
// Written by hand, the wall of an entity added later is the method nobody
// wrote, and nothing reports it: the app compiles, the tests pass, and the rows
// are readable by everybody.
func TestAnAddedEntityArrivesWalled(t *testing.T) {
	x := require.New(t)
	a := New(t)
	ctx := t.Context()

	acme := a.tenantOf(ctx, x, "acme")
	other := a.tenantOf(ctx, x, "other")

	v, err := a.Ungated.Cell().Add(ctx, app.CellAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: other.Bytes()}.Build(),
		Alias:  "bay-1",
	}.Build())
	x.NoError(err)

	get := func(who pdid.Id) error {
		_, err := a.Walled.Cell().Get(As(ctx, who), app.CellGetRequest_builder{
			Ref: app.CellRef_builder{Id: v.GetId()}.Build(),
		}.Build())
		return err
	}

	x.NoError(get(other))
	x.Equal(codes.NotFound, status.Code(get(acme)))

	// And it names what it is, which is the other half of the same declaration.
	k, err := pdid.From(v.GetId())
	x.NoError(err)
	x.Equal("cell", k.Domain().String())
}
