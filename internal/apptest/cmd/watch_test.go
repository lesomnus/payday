package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/pdtest"
)

// watching opens a Watch and answers with what it sends, one message at a time.
//
// It is served over a real connection, since a stream is the one thing a direct
// call cannot travel: there is no ServerStream to hand a handler.
func (b *built) watching(t *testing.T, req *app.RobotWatchRequest) (app.Client, <-chan *app.RobotWatchResponse, func()) {
	t.Helper()
	x := require.New(t)

	conn := pdtest.Serve(t, b.grpc(t, pdtest.Logging(t)))
	ctx, cancel := context.WithCancel(b.travels(t.Context()))
	t.Cleanup(cancel)

	c0 := app.NewClient(conn)
	out, err := c0.Robot().Watch(ctx, req)
	x.NoError(err)

	c := make(chan *app.RobotWatchResponse, 8)
	go func() {
		defer close(c)
		for {
			v, err := out.Recv()
			if err != nil {
				return
			}
			c <- v
		}
	}()

	return c0, c, cancel
}

// next reads one message, or fails the test rather than hanging.
//
// Generic over the response because a watch is not one entity's shape: the
// tests below are mostly about Robot, and the one about Holder reads the same
// way for the same reasons.
func next[T any](t *testing.T, c <-chan *T) *T {
	t.Helper()

	select {
	case v, ok := <-c:
		if !ok {
			t.Fatal("the stream ended")
		}
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("nothing arrived")
		return nil
	}
}

// TestWatchBeginsWithWhatIsThere is why a client does not have to List and then
// subscribe and race the two.
func TestWatchBeginsWithWhatIsThere(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	vs := b.sow(ctx, x, b.Tenant, 1, "arm-")

	c0, c, stop := b.watching(t, app.RobotWatchRequest_builder{
		Filters: []*app.RobotFilter{
			app.RobotFilter_builder{Ref: app.RobotRef_builder{Id: vs[0].GetId()}.Build()}.Build(),
		},
	}.Build())
	defer stop()

	_ = c0
	res := next(t, c)
	x.Len(res.GetItems(), 1)
	x.Equal(vs[0].GetId(), res.GetItems()[0].GetId())
	x.Equal("arm-a", res.GetItems()[0].GetValue().GetAlias())

	// No action: the first message is not something anybody asked for, it is
	// what is already there.
	x.Empty(res.GetItems()[0].GetAction())
}

// TestWatchSendsStateAndNotADelta is the decision the rest follows from.
//
// A client keeps what it was last told about a row and replaces it, so a stream
// that missed something is still correct.
func TestWatchSendsStateAndNotADelta(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	vs := b.sow(ctx, x, b.Tenant, 1, "arm-")

	c0, c, stop := b.watching(t, app.RobotWatchRequest_builder{
		Filters: []*app.RobotFilter{
			app.RobotFilter_builder{Ref: app.RobotRef_builder{Id: vs[0].GetId()}.Build()}.Build(),
		},
	}.Build())
	defer stop()

	next(t, c) // the snapshot

	_, err := c0.Robot().Patch(b.travels(ctx), app.RobotPatchRequest_builder{
		Ref:         app.RobotRef_builder{Id: vs[0].GetId()}.Build(),
		Alias:       z.Ptr("renamed"),
		DateUpdated: vs[0].GetDateUpdated(),
	}.Build())
	x.NoError(err)

	res := next(t, c)
	x.Len(res.GetItems(), 1)

	// The whole row, not what changed about it.
	x.Equal("renamed", res.GetItems()[0].GetValue().GetAlias())
	x.Equal(b.Tenant.Bytes(), res.GetItems()[0].GetValue().GetTenant().GetId())

	// And what the caller of that RPC asked for, by the name gRPC knows it by.
	x.Equal(app.RobotService_Patch_FullMethodName, res.GetItems()[0].GetAction())
}

// TestARemovalIsSaidByAbsence is the whole of how a removal is said.
//
// There is no flag and no tombstone. The row is named, the value is absent, and
// the RPC that did it says what happened -- a client that read the API docs
// knows what `Erase` means, and would know what `Deactivate` meant too.
func TestARemovalIsSaidByAbsence(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	vs := b.sow(ctx, x, b.Tenant, 1, "arm-")

	c0, c, stop := b.watching(t, app.RobotWatchRequest_builder{
		Filters: []*app.RobotFilter{
			app.RobotFilter_builder{Ref: app.RobotRef_builder{Id: vs[0].GetId()}.Build()}.Build(),
		},
	}.Build())
	defer stop()

	next(t, c)

	_, err := c0.Robot().Erase(b.travels(ctx), app.RobotRef_builder{Id: vs[0].GetId()}.Build())
	x.NoError(err)

	res := next(t, c)
	x.Len(res.GetItems(), 1)
	x.Equal(vs[0].GetId(), res.GetItems()[0].GetId(), "one that is gone still has to be named")
	x.Nil(res.GetItems()[0].GetValue(), "absence is how a removal is said")
	x.Equal(app.RobotService_Erase_FullMethodName, res.GetItems()[0].GetAction())
}

// TestAnErasedHolderIsGoneOnTheStream is [TestARemovalIsSaidByAbsence] for the
// entity payday itself declared watchable.
//
// `Holder` says `watch: {}` in payday's own schema -- an app could not, since
// an overlay merges fields and not the entity option; see docs/migrating.md.
// The stream is what that bought: a holder is a credential's anchor, so its
// goneness is news a client wants pushed rather than discovered when the next
// call as that holder fails. Every other watch test exercises the app's own
// entity, so this is the one place the generated `HolderService/Watch` is
// actually opened.
func TestAnErasedHolderIsGoneOnTheStream(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// Somebody other than the watcher: erasing the credential this test calls
	// with would make the erase the last call that works, which is a different
	// story from the one the stream tells.
	bob, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "bob",
	}.Build())
	x.NoError(err)

	conn := pdtest.Serve(t, b.grpc(t, pdtest.Logging(t)))
	wctx, cancel := context.WithCancel(b.travels(t.Context()))
	defer cancel()

	c0 := app.NewClient(conn)
	out, err := c0.Holder().Watch(wctx, app.HolderWatchRequest_builder{
		Filters: []*app.HolderFilter{
			app.HolderFilter_builder{Ref: app.HolderRef_builder{Id: bob.GetId()}.Build()}.Build(),
		},
	}.Build())
	x.NoError(err)

	c := make(chan *app.HolderWatchResponse, 8)
	go func() {
		defer close(c)
		for {
			v, err := out.Recv()
			if err != nil {
				return
			}
			c <- v
		}
	}()

	// The snapshot: the row as it is, which is the state the absence below
	// replaces.
	res := next(t, c)
	x.Len(res.GetItems(), 1)
	x.Equal("bob", res.GetItems()[0].GetValue().GetAlias())

	_, err = c0.Holder().Erase(b.travels(ctx), app.HolderRef_builder{Id: bob.GetId()}.Build())
	x.NoError(err)

	res = next(t, c)
	x.Len(res.GetItems(), 1)
	x.Equal(bob.GetId(), res.GetItems()[0].GetId(), "one that is gone still has to be named")
	x.Nil(res.GetItems()[0].GetValue(), "absence is how a removal is said")
	x.Equal(app.HolderService_Erase_FullMethodName, res.GetItems()[0].GetAction())
}

// TestAWatchSaysWhichRowsItIsAbout is the one shape refused.
//
// A list runs its filters once; a watch runs them again for every write, for as
// long as the stream is open. One with no filters is the whole table, forever.
func TestAWatchSaysWhichRowsItIsAbout(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	conn := pdtest.Serve(t, b.grpc(t, pdtest.Logging(t)))
	out, err := app.NewClient(conn).Robot().Watch(b.travels(t.Context()), app.RobotWatchRequest_builder{}.Build())
	x.NoError(err)

	_, err = out.Recv()
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(err.Error(), "the whole table")
}

// TestWatchIsNotToldAboutSomebodyElse is the wall, on a stream.
//
// The read-back goes through the same Get every other read does, with the
// caller's own context, so a row they may not see comes back NotFound and is
// never sent.
func TestWatchIsNotToldAboutSomebodyElse(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)

	theirs, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
		Alias:  "theirs",
	}.Build())
	x.NoError(err)

	// Naming a row the caller may not see is refused when the stream opens,
	// with the same answer any other read of it gives.
	conn := pdtest.Serve(t, b.grpc(t, pdtest.Logging(t)))
	out, err := app.NewClient(conn).Robot().Watch(b.travels(t.Context()), app.RobotWatchRequest_builder{
		Filters: []*app.RobotFilter{
			app.RobotFilter_builder{Ref: app.RobotRef_builder{Id: theirs.GetId()}.Build()}.Build(),
		},
	}.Build())
	x.NoError(err)

	_, err = out.Recv()
	x.Equal(codes.NotFound, status.Code(err))
}
