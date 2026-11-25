package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/pdtest"
)

// cellsWithin is a select of a cell and the cells it is within, `n` levels
// deep: the cell's own fields are the first.
func cellsWithin(n int) *app.CellSelect {
	v := app.CellSelect_builder{Alias: new(true)}.Build()
	for range n - 1 {
		v = app.CellSelect_builder{Alias: new(true), Within: v}.Build()
	}
	return v
}

// TestASelectGoesEightLevelsDeepAtMost: a select is held to eight levels -- the
// entity's own fields and seven edges below them -- on a Get and a List, at
// every port and in process alike, and a deeper one is refused before anything
// is read. `Cell.within` is a cell, so a select of it goes as deep as it is
// written.
func TestASelectGoesEightLevelsDeepAtMost(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	tenant := app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build()
	var under *app.Cell
	for _, alias := range []string{"hall", "bay"} {
		req := app.CellAddRequest_builder{Tenant: tenant, Alias: alias}
		if under != nil {
			req.Within = app.CellRef_builder{Id: under.GetId()}.Build()
		}
		v, err := b.Ungated.Cell().Add(ctx, req.Build())
		x.NoError(err)
		under = v
	}
	_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: tenant, Alias: "arm-01", Cell: app.CellRef_builder{Id: under.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	refused := func(t *testing.T, err error) {
		t.Helper()
		x := require.New(t)

		x.Equal(codes.InvalidArgument, status.Code(err), err)
		x.Contains(status.Convert(err).Message(), "select")
		x.Contains(status.Convert(err).Message(), "8 at most")
		x.Contains(status.Convert(err).Message(), "9 levels deep", "how deep it was, as far as it was counted")
	}

	cellGet := func(n int) error {
		_, err := b.Walled.Cell().Get(b.as(ctx), app.CellGetRequest_builder{
			Ref: app.CellRef_builder{Id: under.GetId()}.Build(), Select: cellsWithin(n),
		}.Build())
		return err
	}
	robotSelect := func(n int) *app.RobotSelect {
		return app.RobotSelect_builder{Alias: new(true), Cell: cellsWithin(n - 1)}.Build()
	}
	robotGet := func(n int) error {
		_, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
			Ref: app.RobotRef_builder{Slug: app.RobotRefBySlug_builder{
				Alias: new("arm-01"), Tenant: app.TenantRef_builder{Alias: new("acme")}.Build(),
			}.Build()}.Build(),
			Select: robotSelect(n),
		}.Build())
		return err
	}
	robotList := func(n int) error {
		_, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{Select: robotSelect(n)}.Build())
		return err
	}

	t.Run("a Get of a cell, its own kind seven times below it", func(t *testing.T) {
		x := require.New(t)
		x.NoError(cellGet(8))
		refused(t, cellGet(9))

		v, err := b.Walled.Cell().Get(b.as(ctx), app.CellGetRequest_builder{
			Ref: app.CellRef_builder{Id: under.GetId()}.Build(), Select: cellsWithin(2),
		}.Build())
		x.NoError(err)
		x.Equal("bay", v.GetAlias())
		x.Equal("hall", v.GetWithin().GetAlias(), "and the edge to its own kind is filled")
	})
	t.Run("a Get of a robot", func(t *testing.T) {
		require.NoError(t, robotGet(8))
		refused(t, robotGet(9))
	})
	t.Run("a List of robots", func(t *testing.T) {
		require.NoError(t, robotList(8))
		refused(t, robotList(9))
	})
	t.Run("one a thousand deep is counted no further than one past the bound", func(t *testing.T) {
		refused(t, cellGet(1000))
	})
	t.Run("and over the wire as in process", func(t *testing.T) {
		x := require.New(t)
		conn := pdtest.Serve(t, b.grpc(t, pdtest.Logging(t)))
		c := app.NewRobotServiceClient(conn)

		_, err := c.List(b.travels(ctx), app.RobotListRequest_builder{Select: robotSelect(8)}.Build())
		x.NoError(err)
		_, err = c.List(b.travels(ctx), app.RobotListRequest_builder{Select: robotSelect(9)}.Build())
		refused(t, err)
	})
}
