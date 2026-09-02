package cmd_test

import (
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
)

// Move is the RPC this app declared, and the thing nothing here demonstrated
// until it existed.
//
// payday generates the CRUD and closes the general writes, so an operation that
// means something is an RPC declared in `proto/ext/app/robot_svc.ext.proto` and
// answered in `server/core`. The claim is that the two halves meet: an overlay
// that adds an **RPC** rather than a field is merged into the generated
// contract, and a layer that answers it stacks with the wall, the gate and the
// trail rather than beside them.
func TestMove(t *testing.T) {
	x := pdtest.NewX(t)
	b, ctx := build(t)
	ctx = b.as(ctx)

	robots := b.Walled.Robot()
	tenant := app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build()

	cell := func(alias string) *app.Cell {
		v, err := b.Ungated.Cell().Add(ctx, app.CellAddRequest_builder{
			Tenant: tenant,
			Alias:  alias,
		}.Build())
		x.NoError(err)

		return v
	}

	floor1 := cell("floor-1")
	floor2 := cell("floor-2")

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: tenant,
		Alias:  "arm-01",
		Cell:   app.CellRef_builder{Id: floor1.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	t.Run("moves it", func(t *testing.T) {
		x := pdtest.NewX(t)

		got, err := robots.Move(ctx, app.RobotMoveRequest_builder{
			Ref: app.RobotRef_builder{Id: v.GetId()}.Build(),
			To:  app.CellRef_builder{Id: floor2.GetId()}.Build(),
		}.Build())
		x.NoError(err)
		x.Equal(floor2.GetId(), got.GetCell().GetId())
	})

	t.Run("and refuses a move to where it already is", func(t *testing.T) {
		x := pdtest.NewX(t)

		_, err := robots.Move(ctx, app.RobotMoveRequest_builder{
			Ref: app.RobotRef_builder{Id: v.GetId()}.Build(),
			To:  app.CellRef_builder{Id: floor2.GetId()}.Build(),
		}.Build())
		x.ErrCode(codes.InvalidArgument, err)
	})

	t.Run("and out of every cell, because the field is nullable", func(t *testing.T) {
		x := pdtest.NewX(t)

		got, err := robots.Move(ctx, app.RobotMoveRequest_builder{
			Ref: app.RobotRef_builder{Id: v.GetId()}.Build(),
		}.Build())
		x.NoError(err)
		x.Empty(got.GetCell().GetId())
	})

	t.Run("and a cell this caller cannot see is NotFound", func(t *testing.T) {
		x := pdtest.NewX(t)

		_, err := robots.Move(ctx, app.RobotMoveRequest_builder{
			Ref: app.RobotRef_builder{Id: v.GetId()}.Build(),
			To:  app.CellRef_builder{Id: b.Holder.Bytes()}.Build(),
		}.Build())
		x.ErrCode(codes.NotFound, err)
	})
}

// TestMoveIsOnTheTrail is the claim that makes a hand-written Rpc worth
// declaring rather than reaching for `Patch`.
//
// The trail records the operation **somebody asked for** and not the leg it
// turned into. `Move` issues a `Patch` below itself, so a trail that recorded
// the leg would answer "who moved this robot" with a method the caller has
// never heard of. `bare.Change.Method` is what avoids that: it carries the RPC
// gRPC dispatched, for the whole request rather than for one leg of it.
//
// So this has to travel a connection. Called directly against the server the
// trail says `Patch`, correctly -- nothing dispatched anything, and the leg is
// then the honest answer. That is the difference being checked here.
func TestMoveIsOnTheTrail(t *testing.T) {
	x := pdtest.NewX(t)
	b, ctx := build(t)
	as := b.travels(ctx)

	tenant := app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build()

	to, err := b.Ungated.Cell().Add(ctx, app.CellAddRequest_builder{
		Tenant: tenant,
		Alias:  "floor-9",
	}.Build())
	x.NoError(err)

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: tenant,
		Alias:  "arm-09",
	}.Build())
	x.NoError(err)

	client := app.NewClient(b.dialed(t, ctx))
	_, err = client.Robot().Move(as, app.RobotMoveRequest_builder{
		Ref: app.RobotRef_builder{Id: v.GetId()}.Build(),
		To:  app.CellRef_builder{Id: to.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	vs, err := b.Ungated.Audit().List(ctx, app.AuditListRequest_builder{
		Filters: []*app.AuditFilter{
			app.AuditFilter_builder{ObjectId: v.GetId()}.Build(),
		},
	}.Build())
	x.NoError(err)

	actions := []string{}
	for _, row := range vs.GetItems() {
		actions = append(actions, row.GetAction())
	}

	x.Contains(actions, "/app.RobotService/Move",
		"the operation somebody asked for, recorded because nothing listed it")
	x.NotContains(actions, "/app.RobotService/Patch",
		"and not the leg it turned into")

	// And the other half of the same rule, so this test says which mechanism it
	// is checking: called directly there is no dispatch, and then the leg is the
	// honest answer rather than a fallback.
	direct, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: tenant,
		Alias:  "arm-10",
	}.Build())
	x.NoError(err)

	_, err = b.Walled.Robot().Move(b.as(ctx), app.RobotMoveRequest_builder{
		Ref: app.RobotRef_builder{Id: direct.GetId()}.Build(),
		To:  app.CellRef_builder{Id: to.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	vs, err = b.Ungated.Audit().List(ctx, app.AuditListRequest_builder{
		Filters: []*app.AuditFilter{
			app.AuditFilter_builder{ObjectId: direct.GetId()}.Build(),
		},
	}.Build())
	x.NoError(err)

	actions = actions[:0]
	for _, row := range vs.GetItems() {
		actions = append(actions, row.GetAction())
	}

	x.Contains(actions, "/app.RobotService/Patch")
	x.NotContains(actions, "/app.RobotService/Move")
}
