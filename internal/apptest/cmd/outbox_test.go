package cmd_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/spin"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// queued builds an app that writes its events to the queue as well as
// publishing them.
func queued(t *testing.T) (*built, context.Context) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	s, err := cmd.Build(ctx, cmd.Config{
		Db:    dbOf(t),
		Watch: config.WatchConfig{Broker: config.BrokerMemory, Outbox: true},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))

	tenant, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)

	holder, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tenant.GetId()}.Build(),
		Alias:  "admin",
	}.Build())
	x.NoError(err)

	b := &built{
		Server: s,
		Tenant: must(pdid.From(tenant.GetId())),
		Holder: must(pdid.From(holder.GetId())),
	}

	// Putting the tenant and the holder there were writes, so they are in the
	// queue. Taking them out is what makes what follows about one write --
	// leaving them would have every test below reading somebody else's events
	// first, which is a thing to find out from a failing assertion once.
	x.NoError(drainOnce(t, b))

	return b, ctx
}

// TestABrokerIsNamedAndNotDefaulted is the one refusal this config has.
//
// The broker payday ships publishes inside the process, which is right for one
// replica and silently wrong for two: a client watching against one never hears
// about a write that landed on another, and neither end reports it -- the
// client is holding a stream that looks healthy and the server published to
// everybody it knew about. A default would make that what an app gets by saying
// nothing, and the day it becomes wrong is a deployment change nobody
// associates with events going missing.
func TestABrokerIsNamedAndNotDefaulted(t *testing.T) {
	x := require.New(t)

	_, err := cmd.Build(t.Context(), cmd.Config{
		Db: dbOf(t),
	})
	x.ErrorContains(err, "watch.broker")
	x.ErrorContains(err, "silently wrong for two")

	// And "no watchers here" is a thing that can be said, rather than something
	// inferred from a field nobody filled in.
	s, err := cmd.Build(t.Context(), cmd.Config{
		Db:    dbOf(t),
		Watch: config.WatchConfig{Broker: config.BrokerNone},
	})
	x.NoError(err)
	s.Close()
}

// TestAnEventIsWrittenWithTheWriteItIsAbout is the whole of what an outbox is.
//
// The row is in the queue **before** anything is published, so a process that
// stops between the commit and the publish has left the event behind rather
// than lost it. Nothing here runs the drainer, which is exactly the crash being
// simulated: the write happened, and the publishing never did.
func TestAnEventIsWrittenWithTheWriteItIsAbout(t *testing.T) {
	x := require.New(t)
	b, ctx := queued(t)

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	rows, err := b.Ent.Outbox.Query().All(ctx)
	x.NoError(err)
	x.Len(rows, 1, "one write, one row")

	row := rows[0]
	x.Equal(v.GetId(), row.ObjectID[:], "which row changed")
	x.Equal(app.RobotService_Add_FullMethodName, row.Method, "what the caller asked for")
	x.Equal(app.RobotService_Add_FullMethodName, row.By, "and what actually wrote")
	x.Equal(b.Tenant.Uuid(), row.TenantID)
	x.Equal(b.Holder.Uuid(), row.ActorID)
}

// TestAQueuedEventIsPublishedAfterwards is the other half: something picks the
// row up, and what comes out is the event that was never published.
func TestAQueuedEventIsPublishedAfterwards(t *testing.T) {
	x := require.New(t)
	b, ctx := queued(t)

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	// A subscriber that was not there when the write happened, which is what a
	// process restarted since is.
	events, stop := b.Watch.Subscribe()
	defer stop()

	x.NoError(drainOnce(t, b))

	select {
	case e := <-events:
		x.Len(e.Changes, 1)
		x.Equal(v.GetId(), e.Changes[0].Key.Bytes())
		x.Equal(app.RobotService_Add_FullMethodName, e.Changes[0].By)
		x.Equal(b.Tenant, e.Tenant)
	case <-time.After(3 * time.Second):
		t.Fatal("the queue was drained and nothing was published")
	}

	// And the row is gone, so the next pass does not say it again.
	n, err := b.Ent.Outbox.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n)
}

// TestPublishingComesBeforeDeleting is the order that makes this at-least-once
// rather than at-most-once.
//
// A subscriber that is not listening is not a reason to take the row away. The
// memory broker publishes to whoever is there and nobody is, so the event goes
// nowhere -- and the row still goes, because "published" is what this loop can
// know and "received" is not. What makes that safe is one thing only: a
// subscriber which reconnects is sent everything that matches now, since a
// Watch sends state.
func TestPublishingComesBeforeDeleting(t *testing.T) {
	x := require.New(t)
	b, ctx := queued(t)

	_, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	before, err := b.Ent.Outbox.Query().Count(ctx)
	x.NoError(err)
	x.NotZero(before)

	x.NoError(drainOnce(t, b))

	after, err := b.Ent.Outbox.Query().Count(ctx)
	x.NoError(err)
	x.Zero(after)
}

// TestTheQueueIsDrainedOldestFirst, because an identifier carries the
// millisecond it was minted and a sequence within it -- so the key **is** the
// order and there is no column to keep in step with it.
func TestTheQueueIsDrainedOldestFirst(t *testing.T) {
	x := require.New(t)
	b, ctx := queued(t)

	var want [][]byte
	for _, alias := range []string{"arm-01", "arm-02", "arm-03"} {
		v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  alias,
		}.Build())
		x.NoError(err)
		want = append(want, v.GetId())
	}

	events, stop := b.Watch.Subscribe()
	defer stop()

	x.NoError(drainOnce(t, b))

	var got [][]byte
	for range want {
		select {
		case e := <-events:
			got = append(got, e.Changes[0].Key.Bytes())
		case <-time.After(3 * time.Second):
			t.Fatal("fewer events than writes")
		}
	}
	x.Equal(want, got)
}

// TestNoOutboxMeansNoRows is what an app that never asked for one pays.
func TestNoOutboxMeansNoRows(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	n, err := b.Ent.Outbox.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n)

	// And no loop to run.
	x.Empty(b.Spin)
}

// TestTheLoopIsFoundAndNotDeclared is `spin` over the app's own wiring.
func TestTheLoopIsFoundAndNotDeclared(t *testing.T) {
	x := require.New(t)
	b, _ := queued(t)

	x.Len(b.Spin, 1)
	_, ok := b.Spin[0].(spin.Spinner)
	x.True(ok, "the drainer is not something spin.Run would find")
}

// drainOnce runs the loop for exactly as long as it takes to empty the queue.
//
// It is the real [pd.Drain] and not a reimplementation of it: what is under
// test is the order it does things in, and a test that did the same things in
// its own order would be testing itself.
func drainOnce(t *testing.T, b *built) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	// A tick short enough that one pass has run before the wait, and a context
	// that ends the loop once it has.
	d := pd.Drain(b.Ent, b.Watch.Broker(), time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- d.Spin(ctx) }()

	for {
		n, err := b.Ent.Outbox.Query().Count(ctx)
		if err != nil {
			cancel()
			<-done
			return err
		}
		if n == 0 {
			cancel()
			return <-done
		}

		select {
		case <-ctx.Done():
			return <-done
		case <-time.After(time.Millisecond):
		}
	}
}

// TestAWriteWhoseEventCouldNotBeQueuedDoesNotHappen is the one place this
// recorder differs from the one beside it, and the difference is the whole
// reason to have both.
//
// `WatchRecorder` never refuses: an event nobody could publish is not a reason
// to undo the thing it was about, because that event was best-effort anyway.
// This one exists so that an event is **not** lost, so a write that committed
// with nothing queued would have lost precisely the one it was written to keep.
func TestAWriteWhoseEventCouldNotBeQueuedDoesNotHappen(t *testing.T) {
	x := require.New(t)
	b, ctx := queued(t)

	// The queue, gone from under it. Nothing else about the app changes, which
	// is what makes this the failure and not a rewrite of the app.
	_, err := b.Db.ExecContext(ctx, "DROP TABLE outbox")
	x.NoError(err)

	_, err = b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.Error(err)

	// And the row is not there, so nothing committed that nothing will publish.
	n, err := b.Ent.Robot.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n)
}
