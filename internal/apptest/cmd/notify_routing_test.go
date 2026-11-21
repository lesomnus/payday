package cmd_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lesomnus/payday/internal/apptest/internal/ent/notificationrecord"
	"github.com/lesomnus/payday/notify"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/watch"
	"github.com/stretchr/testify/require"
)

func TestNotificationRoutingSurvivesOutboxAndRemovalOfSource(t *testing.T) {
	x := require.New(t)
	b, ctx := queued(t)
	f := notificationSetup(t, b)
	x.NoError(drainOnce(t, b))
	v, err := f.client.Add(b.travels(ctx), f.add("route"))
	x.NoError(err)
	rows, err := b.Ent.Outbox.Query().All(ctx)
	x.NoError(err)
	var found bool
	for _, row := range rows {
		if pdid.Id(row.ObjectId).Domain() != pdid.Id(must(pdid.From(v.GetId()))).Domain() {
			continue
		}
		found = true
		x.NotNil(row.Routes)
		x.NotNil(row.Signal)
		hint, err := notify.Signal(*row.Signal)
		x.NoError(err)
		x.Equal(b.Tenant, hint.Tenant)
		x.Equal([]notify.Topic{{Resource: must(pdid.From(f.job.GetId()))}}, hint.Topics)
		var routes []watch.Route
		x.NoError(json.Unmarshal(*row.Routes, &routes))
		x.Equal(notify.Routes("github.com/lesomnus/payday/internal/apptest", hint), routes)
	}
	x.True(found, "notification recorder captured no routing metadata")
	_, err = b.Ent.NotificationRecord.Delete().Where(notificationrecord.IdEQ(must(pdid.From(v.GetId())).Uuid())).Exec(ctx)
	x.NoError(err)
	sub, err := b.Watch.Open(ctx, watch.Interest{Routes: notify.Routes("github.com/lesomnus/payday/internal/apptest", notify.Hint{Tenant: b.Tenant, Topics: []notify.Topic{{Resource: must(pdid.From(f.job.GetId()))}}})})
	x.NoError(err)
	defer sub.Close()
	x.NoError(drainOnce(t, b))
	select {
	case e := <-sub.Events:
		x.NotEmpty(e.Changes[0].Signal)
		hint, err := notify.Signal(e.Changes[0].Signal)
		x.NoError(err)
		x.Equal(b.Tenant, hint.Tenant)
	case <-time.After(3 * time.Second):
		t.Fatal("queued notification lost its route when the row was removed")
	}
}
