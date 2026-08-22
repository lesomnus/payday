package cmd_test

import (
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
)

// A stamp is the tenant a `via` reaches, kept on the row and written by the
// server.
//
// `Reading` is the entity it is declared on, and the one it is for: readings
// arrive faster than anybody reads them, so the wall on that table is the read
// that runs most. Without a stamp it is `HasRobotWith(robot.TenantIDIn(...))`,
// a correlated subquery; with one it is the comparison a direct edge gets.

func TestAStampIsWrittenByTheServer(t *testing.T) {
	x := pdtest.NewX(t)
	b, ctx := build(t)

	robot, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	v, err := b.Walled.Reading().Add(b.as(ctx), app.ReadingAddRequest_builder{
		Robot:   app.RobotRef_builder{Id: robot.GetId()}.Build(),
		Celsius: 21.5,
	}.Build())
	x.NoError(err)

	// Nobody said it. It is not a field of the request in any useful sense --
	// the server writes what `robot.tenant` reaches.
	got, err := pdid.From(v.GetTenantId())
	x.NoError(err)
	x.Equal(b.Tenant, got)
}

// TestAStampIsNotTheCallersToWrite is the whole reason it is called a stamp.
//
// A caller that could set it could put a row behind a wall its edge does not
// agree with, and the row would then be readable by a tenant that does not hold
// it. So what a caller puts there is overwritten, on every path into the
// server -- including the one with no wall and no gate, because the stamp is on
// the Sink that both stacks are built on.
func TestAStampIsNotTheCallersToWrite(t *testing.T) {
	x := pdtest.NewX(t)
	b, ctx := build(t)

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)

	robot, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-02",
	}.Build())
	x.NoError(err)

	// Through the server the deployment does its own work with -- no wall, no
	// gate -- claiming the row is beta's.
	v, err := b.Ungated.Reading().Add(ctx, app.ReadingAddRequest_builder{
		Robot:    app.RobotRef_builder{Id: robot.GetId()}.Build(),
		TenantId: other.GetId(),
		Celsius:  9,
	}.Build())
	x.NoError(err)

	got, err := pdid.From(v.GetTenantId())
	x.NoError(err)
	x.Equal(b.Tenant, got, "what the edge reaches, not what the caller wrote")
}

// TestTheWallReadsTheStamp: the row is behind the wall the stamp names, which
// is what makes the column worth keeping rather than merely correct.
func TestTheWallReadsTheStamp(t *testing.T) {
	x := pdtest.NewX(t)
	b, ctx := build(t)

	robot, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-03",
	}.Build())
	x.NoError(err)

	v, err := b.Ungated.Reading().Add(ctx, app.ReadingAddRequest_builder{
		Robot:   app.RobotRef_builder{Id: robot.GetId()}.Build(),
		Celsius: 3,
	}.Build())
	x.NoError(err)

	ref := app.ReadingGetRequest_builder{
		Ref: app.ReadingRef_builder{Id: v.GetId()}.Build(),
	}.Build()

	// Somebody in the tenant it was stamped with reads it.
	_, err = b.Walled.Reading().Get(b.as(ctx), ref)
	x.NoError(err)

	// And somebody in another does not.
	beta, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)
	bk, err := pdid.From(beta.GetId())
	x.NoError(err)

	outsider, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: beta.GetId()}.Build(),
		Alias:  "outsider",
	}.Build())
	x.NoError(err)
	ok, err := pdid.From(outsider.GetId())
	x.NoError(err)

	f := frame.New(ok, bk, frame.Whole()).WithScope(frame.Only(bk))
	_, err = b.Walled.Reading().Get(frame.Into(ctx, f), ref)
	x.ErrCode(codes.NotFound, err)
}

// TestAReadingWithNoRobotIsRefused, because a row with no edge has no tenant to
// stamp -- and an empty stamp is a row the wall hides from everybody.
func TestAReadingWithNoRobotIsRefused(t *testing.T) {
	x := pdtest.NewX(t)
	b, ctx := build(t)

	_, err := b.Ungated.Reading().Add(ctx, app.ReadingAddRequest_builder{Celsius: 1}.Build())
	x.ErrCode(codes.InvalidArgument, err)
}

// TestPathsThatMustAgree is what `agrees:` adds to what the gate already does.
//
// The generated gate reads one edge of an Add through the wall: the first hop
// of the tenancy path, and the set at field 3 where an app declares one. Here
// that is `lead`, so an ordinary caller cannot point a Pairing at a lead it
// cannot see -- and `follow` is not read at all, because an edge to some other
// row is referential rather than tenancy. What no gate can see is a caller who
// may see **both**: an operator whose scope covers several tenants, and the
// deployment writing through the server the wall was never installed on.
func TestPathsThatMustAgree(t *testing.T) {
	x := pdtest.NewX(t)
	b, ctx := build(t)

	beta, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "beta"}.Build())
	x.NoError(err)

	mine, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "lead-01",
	}.Build())
	x.NoError(err)

	also, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "follow-01",
	}.Build())
	x.NoError(err)

	theirs, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: beta.GetId()}.Build(),
		Alias:  "follow-02",
	}.Build())
	x.NoError(err)

	pair := func(lead, follow []byte) *app.PairingAddRequest {
		return app.PairingAddRequest_builder{
			Lead:   app.RobotRef_builder{Id: lead}.Build(),
			Follow: app.RobotRef_builder{Id: follow}.Build(),
		}.Build()
	}

	t.Run("two in the same tenant", func(t *testing.T) {
		x := pdtest.NewX(t)

		_, err := b.Ungated.Pairing().Add(ctx, pair(mine.GetId(), also.GetId()))
		x.NoError(err)
	})

	// Through the server with no wall and no gate, which is the path that
	// nothing else was going to check.
	t.Run("and two that are not, written from outside every tenant", func(t *testing.T) {
		x := pdtest.NewX(t)

		_, err := b.Ungated.Pairing().Add(ctx, pair(mine.GetId(), theirs.GetId()))
		x.ErrCode(codes.InvalidArgument, err)
		x.Contains(err.Error(), "another tenant")
	})

	// And by somebody who can see both, which is the case the gate passes.
	t.Run("and by a caller who holds both tenants", func(t *testing.T) {
		x := pdtest.NewX(t)

		bk, err := pdid.From(beta.GetId())
		x.NoError(err)

		f := frame.New(b.Holder, b.Tenant, frame.Whole()).
			WithScope(frame.Only(b.Tenant, bk))

		_, err = b.Walled.Pairing().Add(frame.Into(ctx, f), pair(mine.GetId(), theirs.GetId()))
		x.ErrCode(codes.InvalidArgument, err)
	})
}
