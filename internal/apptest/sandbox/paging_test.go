package sandbox_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/sandbox"
)

// TestTheScriptPages, on the database the script makes.
//
// The panel pages a list by being scrolled, and against the sandbox it paged
// forever: every scroll answered with the same fifty rows and the same cursor,
// so the table grew without ever reaching a second page. The same paging is
// tested in `cmd` and passes there, which is what makes this worth its own
// test -- what differs is not the code but the rows, and the rows come from
// here.
func TestTheScriptPages(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, db := built(t, x)
	x.NoError(sandbox.Load(ctx, db))

	// Ungated, because what is being tested is the cursor and not the wall.
	seen := map[string]int{}
	after := ""
	pages := 0
	for {
		res, err := s.Ungated.Holder().List(ctx, app.HolderListRequest_builder{
			Size:  50,
			After: after,
		}.Build())
		x.NoError(err)

		pages++
		for _, v := range res.GetItems() {
			seen[v.GetAlias()]++
		}

		next := res.GetNext()
		x.NotEqual(after, next,
			"page %d answered with the cursor it was given, so the next page is this page again", pages)

		if after = next; after == "" {
			break
		}
		x.Less(pages, 20, "the cursor never ended")
	}

	// 201: `admin` and the two hundred the script seeds.
	x.Len(seen, 201)
	for k, v := range seen {
		x.Equal(1, v, "%s came back on %d pages", k, v)
	}
}
