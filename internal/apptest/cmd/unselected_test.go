package cmd_test

import (
	"testing"

	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	app "github.com/lesomnus/payday/internal/apptest"
)

// TestWhatAReadDidNotSelectIsAbsent: a field a select did not ask for is not
// in the answer, a stamp as much as any other.
//
// It was its zero, and a stamp's zero is year one: `"dateUpdated":
// "0001-01-01T00:00:00Z"`, which is the version a caller sends back with a
// write, and was refused.
func TestWhatAReadDidNotSelectIsAbsent(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	ref := app.RobotRef_builder{Slug: app.RobotRefBySlug_builder{
		Alias:  new("arm-01"),
		Tenant: app.TenantRef_builder{Alias: new("acme")}.Build(),
	}.Build()}.Build()

	t.Run("a read of one field answers that field and the identifier", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
			Ref:    ref,
			Select: app.RobotSelect_builder{Alias: new(true)}.Build(),
		}.Build())
		x.NoError(err)

		x.Equal("arm-01", v.GetAlias())
		x.NotEmpty(v.GetId())
		x.False(v.HasDateUpdated(), "a stamp nobody selected is not year one")
		x.False(v.HasDateCreated())
		x.False(v.HasDateErased())

		b, err := protojson.Marshal(v)
		x.NoError(err)
		x.NotContains(string(b), "0001-01-01")
		x.NotContains(string(b), "dateUpdated")
	})

	t.Run("and an edge it selected answers what that edge's select asked", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
			Ref: ref,
			Select: app.RobotSelect_builder{
				DateUpdated: new(true),
				Tenant:      app.TenantSelect_builder{Alias: new(true)}.Build(),
			}.Build(),
		}.Build())
		x.NoError(err)

		x.True(v.HasDateUpdated(), "what was selected is there")
		x.Empty(v.GetAlias(), "and what was not is not")
		x.Equal("acme", v.GetTenant().GetAlias())
		x.False(v.GetTenant().HasDateUpdated())
		x.False(v.GetTenant().HasDateCreated())
	})

	t.Run("a read that selects nothing answers everything", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{Ref: ref}.Build())
		x.NoError(err)
		x.True(v.HasDateUpdated())
		x.True(v.HasDateCreated())
		x.Equal("arm-01", v.GetAlias())

		w, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
			Ref: ref, Select: app.RobotSelect_builder{All: new(true)}.Build(),
		}.Build())
		x.NoError(err)
		x.True(w.HasDateUpdated())
		x.Equal("arm-01", w.GetAlias())
	})

	t.Run("and a printer prints what it was sent", func(t *testing.T) {
		x := require.New(t)
		conn := b.dialed(t, ctx)
		run := func(args ...string) string {
			got := xlitest.Harness{Cmd: rooted(t, conn), Ctx: b.travels(ctx)}.Run(t, args...)
			x.NoError(got.Err)
			return got.Stdout
		}

		sel := `{"select":{"alias":true}}`
		js := run("robot", "get", "-o", "json", "@acme/arm-01", sel)
		x.Contains(js, `"alias": "arm-01"`)
		x.NotContains(js, "dateUpdated")
		x.NotContains(js, "dateCreated")

		x.NotContains(run("robot", "get", "-o", "protojson", "@acme/arm-01", sel), "0001-01-01")
		x.NotContains(run("robot", "get", "-o", "prototext", "@acme/arm-01", sel), "-62135596800")
		x.NotContains(run("robot", "get", "@acme/arm-01", sel), "0001-01-01")
		x.Regexp(`arm-01\s+-\s*$`, run("robot", "get", "-o", "table", "@acme/arm-01", sel), "an AGE of nothing")
	})
}
