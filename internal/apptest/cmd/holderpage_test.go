package cmd_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// TestHolderListPagesToo, which is the same claim [TestListReadsThroughInPages]
// makes about a robot.
//
// It is a second test rather than a case in that one because what it is really
// asking is whether the cursor is the entity's or the generator's: the panel
// pages a robot and stops, and pages a holder forever.
func TestHolderListPagesToo(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	const n = 7
	for i := range n {
		_, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  fmt.Sprintf("op-%03d", i),
		}.Build())
		x.NoError(err)
	}

	seen := map[string]int{}
	after := ""
	pages := 0
	for {
		res, err := b.Walled.Holder().List(b.as(ctx), app.HolderListRequest_builder{
			Size:  3,
			After: after,
		}.Build())
		x.NoError(err)

		pages++
		for _, v := range res.GetItems() {
			seen[v.GetAlias()]++
		}

		next := res.GetNext()
		x.NotEqual(after, next, "page %d answered with the cursor it was given, so the next page is this page", pages)

		if after = next; after == "" {
			break
		}
		x.Less(pages, 10, "the cursor never ended")
	}

	for k, v := range seen {
		x.Equal(1, v, "%s came back on %d pages", k, v)
	}
}

// TestAHolderPageIsWhatTheOverlaySays: the app's overlay of payday's Holder
// says a page of forty and a cap of a hundred and twenty, and that is the page
// -- in process, over the wire, and at the command line, which asks the
// server. It was payday's twenty and hundred whatever an overlay said, and
// nothing said the overlay had done nothing.
func TestAHolderPageIsWhatTheOverlaySays(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	x.Equal(40, pd.HolderPageSize, "the overlay's size, not payday's 20")
	x.Equal(120, pd.HolderPageLimit, "the overlay's max, not payday's 100")
	x.Equal(20, pd.TenantPageSize, "and Tenant's, which the overlay does not say, is payday's")
	x.Equal(100, pd.TenantPageLimit)

	sow := func(from, to int) {
		for i := from; i < to; i++ {
			_, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  fmt.Sprintf("op-%03d", i),
			}.Build())
			x.NoError(err)
		}
	}

	// What the build put there already, so that what follows counts rows.
	had, err := b.Walled.Holder().List(b.as(ctx), app.HolderListRequest_builder{Size: 120}.Build())
	x.NoError(err)
	x.Empty(had.GetNext())
	sow(0, 30-len(had.GetItems()))

	conn := pdtest.Serve(t, b.grpc(t, pdtest.Logging(t)))
	wire := app.NewHolderServiceClient(conn)
	cli := func(t *testing.T, args ...string) (int, string) {
		t.Helper()
		got := xlitest.Harness{Cmd: rooted(t, conn), Ctx: b.travels(ctx)}.
			Run(t, append([]string{"holder", "list", "-o", "json"}, args...)...)
		require.NoError(t, got.Err)
		var res struct {
			Items []json.RawMessage `json:"items"`
			Next  string            `json:"next"`
		}
		require.NoError(t, json.Unmarshal([]byte(got.Stdout), &res))
		return len(res.Items), res.Next
	}

	t.Run("thirty holders are one page to a request that says nothing", func(t *testing.T) {
		x := require.New(t)

		res, err := b.Walled.Holder().List(b.as(ctx), app.HolderListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(res.GetItems(), 30)
		x.Empty(res.GetNext(), "and there is no second page")

		res, err = wire.List(b.travels(ctx), app.HolderListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(res.GetItems(), 30)
		x.Empty(res.GetNext())

		n, next := cli(t)
		x.Equal(30, n)
		x.Empty(next)
	})

	sow(30, 130)

	t.Run("a page of a hundred and thirty is the overlay's forty, unasked", func(t *testing.T) {
		x := require.New(t)

		res, err := b.Walled.Holder().List(b.as(ctx), app.HolderListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(res.GetItems(), 40)
		x.NotEmpty(res.GetNext())

		n, next := cli(t)
		x.Equal(40, n)
		x.NotEmpty(next)
	})

	t.Run("and asked for more than the overlay's cap, the cap", func(t *testing.T) {
		x := require.New(t)

		// Clamped and not refused, as every entity's page is: asking for more
		// rows than there may be is a caller being generous with themselves.
		res, err := b.Walled.Holder().List(b.as(ctx), app.HolderListRequest_builder{Size: 1000}.Build())
		x.NoError(err)
		x.Len(res.GetItems(), 120, "more than payday's hundred, and no more than the overlay's hundred and twenty")
		x.NotEmpty(res.GetNext())

		res, err = wire.List(b.travels(ctx), app.HolderListRequest_builder{Size: 1000}.Build())
		x.NoError(err)
		x.Len(res.GetItems(), 120)

		n, next := cli(t, "-n", "1000")
		x.Equal(120, n)
		x.NotEmpty(next)

		n, _ = cli(t, "-n", "110")
		x.Equal(110, n, "and anything up to it is what was asked")
	})
}
