package cmd_test

import (
	"encoding/json"
	"testing"

	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/page"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
)

// TestAListTakesASelect: a List's select means what a Get's does, for each
// row: which fields, and which edges filled. A list of robots with their
// tenants' names is one call, where it was the list and then a Get a row.
func TestAListTakesASelect(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:  "arm-00",
		Secret: []byte("hunter2"),
	}.Build())
	x.NoError(err)
	b.sow(ctx, x, b.Tenant, 4, "arm-")

	withTenant := app.RobotSelect_builder{
		Alias:  new(true),
		Tenant: app.TenantSelect_builder{Alias: new(true)}.Build(),
	}.Build()

	t.Run("each row is what a Get of it with the select answers", func(t *testing.T) {
		x := require.New(t)

		res, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{Select: withTenant}.Build())
		x.NoError(err)
		x.Len(res.GetItems(), 5)
		for _, v := range res.GetItems() {
			x.NotEmpty(v.GetAlias())
			x.Equal("acme", v.GetTenant().GetAlias(), "the edge is filled, as the select asked")

			one, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{Ref: v.Ref(), Select: withTenant}.Build())
			x.NoError(err)
			x.Equal(one.GetAlias(), v.GetAlias())
			x.Equal(one.GetTenant().GetAlias(), v.GetTenant().GetAlias())
		}
	})

	t.Run("and answers no field it did not select, as a Get does", func(t *testing.T) {
		x := require.New(t)

		res, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{
			Order:  app.RobotListOrder_ROBOT_LIST_ORDER_ALIAS,
			Size:   2,
			Select: app.RobotSelect_builder{DateCreated: new(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.Len(res.GetItems(), 2)
		x.NotEmpty(res.GetNext(), "the cursor is made of the alias the select left out")
		for _, v := range res.GetItems() {
			x.True(v.HasDateCreated())
			x.False(v.HasDateUpdated(), "a stamp nobody selected is not year one")
			x.False(v.HasDateErased())
			x.Empty(v.GetAlias(), "the alias the order read is not answered")
		}

		res, err = b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{Select: withTenant}.Build())
		x.NoError(err)
		for _, v := range res.GetItems() {
			x.False(v.HasDateUpdated())
			x.False(v.GetTenant().HasDateUpdated(), "nor of an edge's")
		}
	})

	t.Run("and none is today's answer", func(t *testing.T) {
		x := require.New(t)

		res, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(res.GetItems(), 5)
		for _, v := range res.GetItems() {
			x.NotEmpty(v.GetAlias())
			x.True(v.HasDateCreated())
		}
	})

	t.Run("a secret is not answered with however it is asked for", func(t *testing.T) {
		x := require.New(t)

		res, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{
			Select: app.RobotSelect_builder{All: new(true), Secret: new(true)}.Build(),
		}.Build())
		x.NoError(err)
		for _, v := range res.GetItems() {
			x.Empty(v.GetSecret())
		}
	})

	t.Run("and the wall is the wall, with a select as without", func(t *testing.T) {
		x := require.New(t)

		// Another tenant's rows are not this caller's, selected or not. Each
		// edge is read through its entity's narrowing by the same function a
		// Get's select is (bare.RobotSelect).
		_, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
		x.NoError(err)
		res, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{
			Select: withTenant,
			Filters: []*app.RobotFilter{app.RobotFilter_builder{
				Tenant: app.TenantRef_builder{Alias: new("other")}.Build(),
			}.Build()},
		}.Build())
		x.NoError(err)
		x.Empty(res.GetItems())
	})

	t.Run("a page selected carries on in its order, whatever the select left out", func(t *testing.T) {
		x := require.New(t)

		// The order walks the alias, and the select does not ask for it: the
		// cursor is made of it all the same.
		seen := []string{}
		after := ""
		for range 10 {
			res, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{
				Size:   2,
				After:  after,
				Order:  app.RobotListOrder_ROBOT_LIST_ORDER_ALIAS,
				Select: app.RobotSelect_builder{DateCreated: new(true)}.Build(),
			}.Build())
			x.NoError(err)
			for _, v := range res.GetItems() {
				seen = append(seen, string(v.GetId()))
				x.True(v.HasDateCreated())
			}
			if after = res.GetNext(); after == "" {
				break
			}
		}
		x.Len(seen, 5, "every row once")

		ids := map[string]bool{}
		for _, v := range seen {
			ids[v] = true
		}
		x.Len(ids, 5)
	})

	t.Run("the page iterator passes it through", func(t *testing.T) {
		x := require.New(t)

		n := 0
		for v, err := range page.All(b.as(ctx), b.Walled.Robot().List,
			app.RobotListRequest_builder{Size: 2, Select: withTenant}.Build()) {
			x.NoError(err)
			x.Equal("acme", v.GetTenant().GetAlias())
			n++
		}
		x.Equal(5, n)
	})

	t.Run("over the wire, and the command line's list takes it as its get does", func(t *testing.T) {
		x := require.New(t)

		conn := pdtest.Serve(t, b.grpc(t, pdtest.Logging(t)))
		res, err := app.NewRobotServiceClient(conn).List(b.travels(ctx), app.RobotListRequest_builder{Select: withTenant}.Build())
		x.NoError(err)
		x.Len(res.GetItems(), 5)
		x.Equal("acme", res.GetItems()[0].GetTenant().GetAlias())

		got := xlitest.Harness{Cmd: rooted(t, conn), Ctx: b.travels(ctx)}.
			Run(t, "robot", "list", "-o", "json", `{"select":{"alias":true,"tenant":{"alias":true}}}`)
		x.NoError(got.Err)
		var out struct {
			Items []struct {
				Tenant struct {
					Alias string `json:"alias"`
				} `json:"tenant"`
			} `json:"items"`
		}
		x.NoError(json.Unmarshal([]byte(got.Stdout), &out))
		x.Len(out.Items, 5)
		x.Equal("acme", out.Items[0].Tenant.Alias)

		// And a select of a field the entity does not have is refused as a
		// request that does not read.
		bad := xlitest.Harness{Cmd: rooted(t, conn), Ctx: b.travels(ctx)}.
			Run(t, "robot", "list", `{"select":{"nope":true}}`)
		x.Error(bad.Err)
	})
}
