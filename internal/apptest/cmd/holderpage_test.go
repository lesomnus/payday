package cmd_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/payday/internal/apptest"
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
