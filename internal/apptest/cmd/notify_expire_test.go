package cmd_test

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/notify"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/payday/internal/apptest"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	entnotificationreceiptrecord "github.com/lesomnus/payday/internal/apptest/internal/ent/notificationreceiptrecord"
	entnotificationrecord "github.com/lesomnus/payday/internal/apptest/internal/ent/notificationrecord"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// What a notification store keeps, and for how long (payday#194): this app
// declares `keep: {days: 90}`, so `pd.NotificationExpirer` erases what was
// published more than 90 days ago, with every receipt of it, each erasure on
// the trail.

// publishedAgo is the fixture's notification server with a clock `ago` in
// the past, which is how a test publishes a notification that old.
func (f *notificationFixture) publishedAgo(t *testing.T, ago time.Duration) *pd.NotificationServer {
	t.Helper()

	s, err := pd.Notifications(f.b.Walled, f.b.Drv, f.b.Watch, notificationJobPolicy{db: f.b.Ent}, notify.Options{
		CursorKey: bytes.Repeat([]byte{73}, 32),
		Clock:     func() time.Time { return time.Now().Add(-ago) },
	})
	require.NoError(t, err)

	return s
}

func (f *notificationFixture) mark(t *testing.T, ctx context.Context, id []byte) *app.NotificationReceipt {
	t.Helper()

	r, err := notificationMarked(f.server.Mark(ctx, app.NotificationMarkRequest_builder{Refs: []*app.NotificationRef{app.NotificationRef_builder{Id: id}.Build()}, TenantId: f.b.Tenant.Bytes()}.Build()))
	require.NoError(t, err)

	return r
}

func TestNotifyExpiresWhatItKeepsNoLonger(t *testing.T) {
	x := require.New(t)
	b, _, d := digesting(t)
	f := notificationSetup(t, b)
	ctx := b.as(t.Context())
	x.Equal(90*24*time.Hour, pd.NotificationKeep)

	maintenance := app.NotificationTopic_builder{Name: z.Ptr("maintenance")}.Build()
	day := 24 * time.Hour
	old, err := f.publishedAgo(t, 200*day).Add(ctx, f.add("old"))
	x.NoError(err)
	oldNamed := f.add("old-named")
	oldNamed.SetTopic(maintenance)
	other, err := f.publishedAgo(t, 91*day).Add(ctx, oldNamed)
	x.NoError(err)
	young, err := f.publishedAgo(t, 89*day).Add(ctx, f.add("young"))
	x.NoError(err)
	fresh, err := f.server.Add(ctx, f.add("fresh"))
	x.NoError(err)

	issuer := notificationAs(t.Context(), b.Tenant, f.issuer.GetId())
	f.mark(t, ctx, old.GetId())
	f.mark(t, issuer, old.GetId())
	f.mark(t, ctx, other.GetId())
	kept := f.mark(t, ctx, young.GetId())

	// A stream on both topics, which an expiry tells of what it took.
	listening, cancel := context.WithTimeout(b.travels(t.Context()), 10*time.Second)
	defer cancel()
	stream, err := f.client.Listen(listening, app.NotificationListenRequest_builder{TenantId: b.Tenant.Bytes(), Topics: []*app.NotificationTopic{f.topic, maintenance}}.Build())
	x.NoError(err)
	ready, err := stream.Recv()
	x.NoError(err)
	x.True(ready.HasReady())

	expirer := pd.NotificationExpirer(b.Drv, b.Watch, b.Contents)
	x.Equal(notify.ExpireName, expirer.SpinName())
	n, err := expirer.Pass(t.Context())
	x.NoError(err)
	x.Equal(2, n)

	gone := func(v *app.Notification) bool {
		k := mustId(x, v.GetId())
		return !b.Ent.NotificationRecord.Query().Where(entnotificationrecord.IdEQ(k.Uuid())).ExistX(t.Context())
	}
	receipts := func(v *app.Notification) int {
		k := mustId(x, v.GetId())
		return b.Ent.NotificationReceiptRecord.Query().Where(entnotificationreceiptrecord.NotificationIdEQ(k.Uuid())).CountX(t.Context())
	}
	x.True(gone(old), "a notification of 200 days is kept")
	x.True(gone(other), "a notification of 91 days is kept")
	x.Zero(receipts(old), "a receipt outlived its notification")
	x.Zero(receipts(other), "a receipt outlived its notification")
	x.False(gone(young), "a notification of 89 days went")
	x.False(gone(fresh))
	x.Equal(1, receipts(young))

	// Each erasure is a line of the trail, by the act it was, under the
	// tenant it was in, and the rows are what the trail says they are.
	for _, v := range []*app.Notification{old, other} {
		k := mustId(x, v.GetId())
		lines := b.Ent.Audit.Query().Where(entaudit.ObjectIdEQ(k.Uuid()), entaudit.ActionEQ(notify.ExpireName)).AllX(t.Context())
		x.Len(lines, 1, "the expiry of %s is not on the trail", k)
		x.Equal(b.Tenant.Uuid(), lines[0].TenantId)
	}
	rows, err := pd.Digests(t.Context(), b.Ent, d, trail.Policy{})
	x.NoError(err)
	x.True(rows.Ok(), "%+v", rows)

	// The stream is told of both topics, and finds them as they are now.
	told := map[string]bool{}
	for len(told) < 2 {
		hint, err := stream.Recv()
		x.NoError(err)
		for _, v := range hint.GetInvalidated().GetTopics() {
			told[string(v.GetResourceId())+"/"+v.GetName()] = true
		}
	}
	page, err := f.client.Read(b.travels(t.Context()), app.NotificationReadRequest_builder{TenantId: b.Tenant.Bytes(), Topics: []*app.NotificationTopic{f.topic, maintenance}}.Build())
	x.NoError(err)
	x.Len(page.GetItems(), 2)
	found := false
	for _, item := range page.GetItems() {
		if bytes.Equal(item.GetNotification().GetId(), young.GetId()) {
			found = true
			x.True(proto.Equal(kept, item.GetReceipt()), "the receipt of what is kept is not the one it had")
		}
	}
	x.True(found, "the notification of 89 days is not read")

	t.Run("and a second pass takes nothing", func(t *testing.T) {
		x := require.New(t)
		n, err := expirer.Pass(t.Context())
		x.NoError(err)
		x.Zero(n)
	})

	// The key went with its notification: a retry of it now is a new
	// publication, which is why a producer retries within what is kept.
	t.Run("and a key that went is a new publication", func(t *testing.T) {
		x := require.New(t)
		again, err := f.server.Add(ctx, f.add("old"))
		x.NoError(err)
		x.NotEqual(old.GetId(), again.GetId())
	})
}

// TestNotifyAnExpiryWaitsForTheMarkOfWhatItErases: a Mark holds each
// notification it reads until its transaction ends, so an expiry erasing one
// waits for the receipt being written of it and erases that too, and no
// receipt outlives its notification. PostgreSQL's alone: SQLite has one writer
// at a time.
func TestNotifyAnExpiryWaitsForTheMarkOfWhatItErases(t *testing.T) {
	if os.Getenv(pdtest.Postgres) == "" {
		t.Skip("PDTEST_POSTGRES is not set")
	}
	x := require.New(t)
	f := notifications(t)
	ctx := f.b.as(t.Context())
	old, err := f.publishedAgo(t, 200*24*time.Hour).Add(ctx, f.add("old"))
	x.NoError(err)

	drv, tx, err := dialect.BeginTx(ctx, f.b.Drv)
	x.NoError(err)
	defer tx.Rollback()
	writer, err := f.server.WithDriver(drv)
	x.NoError(err)
	_, err = notificationMarked(writer.Mark(ctx, app.NotificationMarkRequest_builder{Refs: []*app.NotificationRef{app.NotificationRef_builder{Id: old.GetId()}.Build()}, TenantId: f.b.Tenant.Bytes()}.Build()))
	x.NoError(err)

	done := make(chan int, 1)
	go func() {
		n, err := pd.NotificationExpirer(f.b.Drv, f.b.Watch, f.b.Contents).Pass(t.Context())
		if err != nil {
			n = -1
		}
		done <- n
	}()
	select {
	case <-done:
		t.Fatal("the expiry erased a notification a Mark was writing a receipt of")
	case <-time.After(500 * time.Millisecond):
	}

	x.NoError(tx.Commit())
	select {
	case n := <-done:
		x.Equal(1, n)
	case <-time.After(10 * time.Second):
		t.Fatal("the expiry did not finish once the Mark committed")
	}
	k := mustId(x, old.GetId())
	x.False(f.b.Ent.NotificationRecord.Query().Where(entnotificationrecord.IdEQ(k.Uuid())).ExistX(t.Context()))
	x.Zero(f.b.Ent.NotificationReceiptRecord.Query().Where(entnotificationreceiptrecord.NotificationIdEQ(k.Uuid())).CountX(t.Context()),
		"the receipt the Mark wrote outlived its notification")
}
