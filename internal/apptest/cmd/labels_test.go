package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/payday/internal/apptest"
)

// sowLabelled puts a holder in the tenant with the labels given.
func (b *built) sowLabelled(ctx context.Context, x *require.Assertions, alias string, labels map[string]string) {
	_, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  alias,
		Labels: labels,
	}.Build())
	x.NoError(err)
}

// aliases is what a list answered with, in the order it answered.
func (b *built) aliases(ctx context.Context, x *require.Assertions, labels map[string]string) []string {
	res, err := b.Walled.Holder().List(b.as(ctx), app.HolderListRequest_builder{
		Size:    50,
		Filters: []*app.HolderFilter{app.HolderFilter_builder{Labels: labels}.Build()},
	}.Build())
	x.NoError(err)

	vs := make([]string, 0, len(res.GetItems()))
	for _, v := range res.GetItems() {
		vs = append(vs, v.GetAlias())
	}

	return vs
}

// TestListByLabels, which is the filter an app modelling anything
// Kubernetes-shaped wants and could not have.
//
// A `map<string, string>` is TYPE_JSON like everything else that lands in a
// JSON column, so `list: by:` refused it and the answer was to filter on the
// client. That is linear, and it is wrong in a way that hides rows: filtering
// the first page is the obvious implementation, and with a hundred newer rows
// belonging to somebody else the answer is "nothing" while the matches sit on
// page two.
func TestListByLabels(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sowLabelled(ctx, x, "one", map[string]string{"team": "core", "tier": "gold"})
	b.sowLabelled(ctx, x, "two", map[string]string{"team": "core"})
	b.sowLabelled(ctx, x, "three", map[string]string{"team": "edge", "tier": "gold"})
	b.sowLabelled(ctx, x, "bare", nil)
	b.sowLabelled(ctx, x, "empty", map[string]string{"team": ""})

	t.Run("one pair", func(t *testing.T) {
		x := require.New(t)
		x.ElementsMatch([]string{"one", "two"}, b.aliases(ctx, x, map[string]string{"team": "core"}))
	})

	t.Run("every pair, not any", func(t *testing.T) {
		x := require.New(t)

		// `two` has the team and not the tier, and a filter naming both is
		// asking for rows that carry both.
		x.ElementsMatch([]string{"one"}, b.aliases(ctx, x, map[string]string{"team": "core", "tier": "gold"}))
	})

	// The first of the two traps this was worth writing down for. Reading a map
	// by index answers with the zero value for a key that is not there, so
	// `team=""` matches every row that has never heard of `team` -- a filter
	// that widens instead of narrowing. `HasKey` is what makes the predicate
	// ask the question it means.
	t.Run("a missing key is not an empty value", func(t *testing.T) {
		x := require.New(t)

		got := b.aliases(ctx, x, map[string]string{"team": ""})
		x.ElementsMatch([]string{"empty"}, got,
			"only the row that says team is empty; `bare` and `three` have no team at all")
	})

	t.Run("a key nothing carries answers with nothing", func(t *testing.T) {
		x := require.New(t)
		x.Empty(b.aliases(ctx, x, map[string]string{"nobody": "here"}))
	})

	// And the second. `{}` is no constraint rather than "has no labels", which
	// is how an absent scalar filter already reads -- and the difference is
	// invisible until somebody sends an empty map.
	//
	// It contributes nothing, so a filter that names nothing else is a filter
	// that names nothing, and that was already refused. Both halves are here
	// because either alone reads as the other rule.
	t.Run("an empty map is no constraint", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Holder().List(b.as(ctx), app.HolderListRequest_builder{
			Filters: []*app.HolderFilter{app.HolderFilter_builder{Labels: map[string]string{}}.Build()},
		}.Build())
		x.ErrorContains(err, "names nothing", "an empty map constrains nothing, so this filter says nothing")

		// Beside something that does constrain, it is simply absent -- and
		// not "has no labels", which would answer with `bare` alone.
		res, err := b.Walled.Holder().List(b.as(ctx), app.HolderListRequest_builder{
			Size: 50,
			Filters: []*app.HolderFilter{app.HolderFilter_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Labels: map[string]string{},
			}.Build()},
		}.Build())
		x.NoError(err)
		x.Len(res.GetItems(), 6, "the five sown and the admin the harness put there, labelled or not")
	})
}
