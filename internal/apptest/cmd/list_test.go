package cmd_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
	"github.com/lesomnus/payday/pdid"
)

// sow puts `n` robots in the given tenant.
func (b *built) sow(ctx context.Context, x *require.Assertions, tenant pdid.Id, n int, prefix string) []*app.Robot {
	vs := make([]*app.Robot, n)
	for i := range vs {
		v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
			Alias:  prefix + string(rune('a'+i)),
		}.Build())
		x.NoError(err)

		vs[i] = v
	}

	return vs
}

// TestListReadsThroughInPages is the paging half, which is the half people get
// wrong and so is the half worth generating.
func TestListReadsThroughInPages(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	want := b.sow(ctx, x, b.Tenant, 7, "arm-")

	var got []string
	after := ""
	pages := 0
	for {
		res, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{
			Size:  3,
			After: after,
		}.Build())
		x.NoError(err)

		pages++
		for _, v := range res.GetItems() {
			got = append(got, v.GetAlias())
		}
		if after = res.GetNext(); after == "" {
			break
		}
		x.Less(pages, 10, "the cursor never ended")
	}

	x.Len(got, len(want))
	x.Equal(3, pages, "seven rows, three at a time")

	// Every row once and in the declared order, which is what the cursor is
	// for -- an offset would be wrong under writes and slower the further in
	// a caller reads.
	for i, v := range want {
		x.Equal(v.GetAlias(), got[i])
	}
}

// TestAFullLastPageEndsTheCursor is what the extra row read is for.
//
// One row more than the page is asked for, so "is there another" is answered
// without a second query and without a count -- and a page that filled exactly
// answers with no cursor rather than sending the caller back for an empty one.
func TestAFullLastPageEndsTheCursor(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sow(ctx, x, b.Tenant, 4, "arm-")

	res, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{Size: 4}.Build())
	x.NoError(err)
	x.Len(res.GetItems(), 4)
	x.Empty(res.GetNext(), "the page filled exactly and there is nothing after it")
}

// TestListIsNarrowedInTheQuery is why a list belongs at the sink.
//
// A list cut short at a limit and filtered afterwards is one that any tenant
// can push another's rows out of by making enough of its own. So the wall is in
// the query, before the limit.
func TestListIsNarrowedInTheQuery(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)
	ok, err := pdid.From(other.GetId())
	x.NoError(err)

	// Twenty of somebody else's, then two of ours. Asked for a page of five,
	// a list that filtered after the limit would answer with none of ours.
	b.sow(ctx, x, ok, 20, "theirs-")
	mine := b.sow(ctx, x, b.Tenant, 2, "mine-")

	res, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{Size: 5}.Build())
	x.NoError(err)
	x.Len(res.GetItems(), 2)
	x.Equal(mine[0].GetAlias(), res.GetItems()[0].GetAlias())
	x.Empty(res.GetNext())
}

// TestTheSizeIsCappedAndTheFiltersAreNot is the difference between clamping and
// refusing, which is a decision and not an inconsistency.
func TestTheSizeIsCappedAndTheFiltersAreNot(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sow(ctx, x, b.Tenant, 3, "arm-")

	// Asking for more rows than there are is a caller being generous with
	// themselves, so it is clamped rather than refused.
	res, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{
		Size: pd.RobotPageLimit * 10,
	}.Build())
	x.NoError(err)
	x.Len(res.GetItems(), 3)

	// A filter count past the cap is refused, because dropping half of them
	// would answer a question nobody asked.
	fs := make([]*app.RobotFilter, pd.RobotFilterLimit+1)
	for i := range fs {
		fs[i] = app.RobotFilter_builder{
			Ref: app.RobotRef_builder{Id: pdid.New(pd.RobotDomain).Bytes()}.Build(),
		}.Build()
	}

	_, err = b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{Filters: fs}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(err.Error(), "the most one list carries")
}

// TestFiltersSelectWhatTheyName is the declared half of a filter: equality on
// what the schema said, and nothing else.
func TestFiltersSelectWhatTheyName(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	vs := b.sow(ctx, x, b.Tenant, 3, "arm-")

	res, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{
		Filters: []*app.RobotFilter{
			app.RobotFilter_builder{Ref: app.RobotRef_builder{Id: vs[0].GetId()}.Build()}.Build(),
			app.RobotFilter_builder{Ref: app.RobotRef_builder{Id: vs[2].GetId()}.Build()}.Build(),
		},
	}.Build())
	x.NoError(err)
	x.Len(res.GetItems(), 2)

	// A filter that names nothing is refused rather than taken to mean
	// everything: the request said "these ones" and did not say which.
	_, err = b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{
		Filters: []*app.RobotFilter{app.RobotFilter_builder{}.Build()},
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
}

// TestListReadsTheEdgesItWasToldTo is `with:`, and it is here because almost
// everything that reads a list wants to know whose the rows are.
func TestListReadsTheEdgesItWasToldTo(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sow(ctx, x, b.Tenant, 1, "arm-")

	res, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(res.GetItems(), 1)
	x.Equal(b.Tenant.Bytes(), res.GetItems()[0].GetTenant().GetId())
}
