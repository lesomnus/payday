package cmd_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/payday/internal/apptest"
)

// readAll reads a list of robots to its end, a page at a time, as `req` asks,
// and answers with their aliases in the order they came.
func (b *built) readAll(ctx context.Context, x *require.Assertions, req func(after string) *app.RobotListRequest) []string {
	var got []string
	after := ""
	for pages := 0; ; pages++ {
		x.Less(pages, 20, "the cursor never ended")

		res, err := b.Walled.Robot().List(b.as(ctx), req(after))
		x.NoError(err)
		for _, v := range res.GetItems() {
			got = append(got, v.GetAlias())
		}
		if after = res.GetNext(); after == "" {
			return got
		}
	}
}

// TestAListIsReadTheOtherWayRound: `reverse` turns every column of the order
// round at once, and the cursor carries on that way -- the same rows, each
// once, in the opposite order, through the same index walked from the other
// end.
func TestAListIsReadTheOtherWayRound(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	b.sow(ctx, x, b.Tenant, 7, "arm-")

	forward := b.readAll(ctx, x, func(after string) *app.RobotListRequest {
		return app.RobotListRequest_builder{Size: 3, After: after}.Build()
	})
	backward := b.readAll(ctx, x, func(after string) *app.RobotListRequest {
		return app.RobotListRequest_builder{Size: 3, After: after, Reverse: true}.Build()
	})

	x.Equal([]string{"arm-a", "arm-b", "arm-c", "arm-d", "arm-e", "arm-f", "arm-g"}, forward)
	want := slices.Clone(forward)
	slices.Reverse(want)
	x.Equal(want, backward)
}

// TestAListIsReadInAnOrderTheSchemaNamed: an order a request names is one the
// schema declared, read a page at a time like the list's own, and the other way
// round as well. `alias_desc` is of mixed directions -- the name backwards, the
// key forwards -- which `reverse` turns round as a whole.
func TestAListIsReadInAnOrderTheSchemaNamed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Made in an order that is not the names', so that an order by name and the
	// list's own cannot be mistaken for each other.
	for _, alias := range []string{"m-c", "m-a", "m-e", "m-b", "m-d"} {
		_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  alias,
		}.Build())
		x.NoError(err)
	}
	read := func(o app.RobotListOrder, reverse bool) []string {
		return b.readAll(ctx, x, func(after string) *app.RobotListRequest {
			return app.RobotListRequest_builder{Size: 2, After: after, Order: o, Reverse: reverse}.Build()
		})
	}

	x.Equal([]string{"m-c", "m-a", "m-e", "m-b", "m-d"}, read(app.RobotListOrder_ROBOT_LIST_ORDER_UNSPECIFIED, false))
	x.Equal([]string{"m-a", "m-b", "m-c", "m-d", "m-e"}, read(app.RobotListOrder_ROBOT_LIST_ORDER_ALIAS, false))
	x.Equal([]string{"m-e", "m-d", "m-c", "m-b", "m-a"}, read(app.RobotListOrder_ROBOT_LIST_ORDER_ALIAS, true))
	x.Equal([]string{"m-e", "m-d", "m-c", "m-b", "m-a"}, read(app.RobotListOrder_ROBOT_LIST_ORDER_ALIAS_DESC, false))
	x.Equal([]string{"m-a", "m-b", "m-c", "m-d", "m-e"}, read(app.RobotListOrder_ROBOT_LIST_ORDER_ALIAS_DESC, true))
}

// TestACursorIsOfOneOrder: a cursor names a place in one sequence, and carried
// on in another order, or in this one the other way round, it would answer with
// a page that is neither the next nor an error. So it is refused. And the
// cursor of a list's own order read forwards is what every cursor was before
// orders could be named, so one a client was holding carries on.
func TestACursorIsOfOneOrder(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	b.sow(ctx, x, b.Tenant, 5, "arm-")

	first := func(o app.RobotListOrder, reverse bool) string {
		res, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{Size: 2, Order: o, Reverse: reverse}.Build())
		x.NoError(err)
		x.NotEmpty(res.GetNext())
		return res.GetNext()
	}
	carry := func(after string, o app.RobotListOrder, reverse bool) error {
		_, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{Size: 2, After: after, Order: o, Reverse: reverse}.Build())
		return err
	}

	own := first(app.RobotListOrder_ROBOT_LIST_ORDER_UNSPECIFIED, false)
	back := first(app.RobotListOrder_ROBOT_LIST_ORDER_UNSPECIFIED, true)
	alias := first(app.RobotListOrder_ROBOT_LIST_ORDER_ALIAS, false)

	x.NoError(carry(own, app.RobotListOrder_ROBOT_LIST_ORDER_UNSPECIFIED, false))
	x.NoError(carry(back, app.RobotListOrder_ROBOT_LIST_ORDER_UNSPECIFIED, true))
	x.NoError(carry(alias, app.RobotListOrder_ROBOT_LIST_ORDER_ALIAS, false))

	for name, err := range map[string]error{
		"the list's own, read the other way":  carry(own, app.RobotListOrder_ROBOT_LIST_ORDER_UNSPECIFIED, true),
		"the other way, read forwards":        carry(back, app.RobotListOrder_ROBOT_LIST_ORDER_UNSPECIFIED, false),
		"by name, in the list's own":          carry(alias, app.RobotListOrder_ROBOT_LIST_ORDER_UNSPECIFIED, false),
		"by name, in another named order":     carry(alias, app.RobotListOrder_ROBOT_LIST_ORDER_ALIAS_DESC, false),
		"by name, read the other way":         carry(alias, app.RobotListOrder_ROBOT_LIST_ORDER_ALIAS, true),
		"the list's own, in an order by name": carry(own, app.RobotListOrder_ROBOT_LIST_ORDER_ALIAS, false),
	} {
		x.Equal(codes.InvalidArgument, status.Code(err), "%s: %v", name, err)
	}

	// The list's own read forwards is the values and nothing else, as before.
	raw, err := base64.RawURLEncoding.DecodeString(own)
	x.NoError(err)
	var vs []json.RawMessage
	x.NoError(json.Unmarshal(raw, &vs))
	x.Len(vs, 2, "the cursor of the list's own order is not the shape a client already holds")
}

// TestAnOrderNoSchemaNamedIsRefused rather than read as the list's own.
func TestAnOrderNoSchemaNamedIsRefused(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{Order: app.RobotListOrder(99)}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(status.Convert(err).Message(), "order")
}
