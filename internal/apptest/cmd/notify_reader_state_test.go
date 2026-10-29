package cmd_test

import (
	"fmt"
	"testing"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (f *notificationFixture) markRefs(values ...*app.Notification) *app.NotificationMarkRequest {
	refs := make([]*app.NotificationRef, len(values))
	for i, v := range values {
		refs[i] = app.NotificationRef_builder{Id: v.GetId()}.Build()
	}
	return app.NotificationMarkRequest_builder{TenantId: f.b.Tenant.Bytes(), Refs: refs}.Build()
}

func TestNotifyUnreadPagesAndCountsUseStoredReaderState(t *testing.T) {
	f := notifications(t)
	ctx := f.b.as(t.Context())
	values := make([]*app.Notification, 125)
	for i := range values {
		v, err := f.server.Add(ctx, f.add(fmt.Sprintf("history-%d", i)))
		require.NoError(t, err)
		values[i] = v
	}
	for start := 1; start < len(values); start += 100 {
		_, err := f.server.Mark(ctx, f.markRefs(values[start:min(start+100, len(values))]...))
		require.NoError(t, err)
	}
	filter := app.NotificationReadFilter_builder{Kinds: []string{"job.completed"}, SourceIds: [][]byte{f.job.GetId()}, UnreadOnly: true}.Build()
	read := f.read()
	read.SetSize(1)
	read.SetFilter(filter)
	page, err := f.server.Read(ctx, read)
	require.NoError(t, err)
	require.Len(t, page.GetItems(), 1)
	require.Equal(t, values[0].GetId(), page.GetItems()[0].GetNotification().GetId())
	require.Empty(t, page.GetNext())
	maintenance := app.NotificationTopic_builder{Name: ptr("maintenance")}.Build()
	for i, source := range [][]byte{f.job.GetId(), nil} {
		_, err = f.server.Add(ctx, app.NotificationAddRequest_builder{TenantId: f.b.Tenant.Bytes(), Topic: maintenance, Kind: "job.completed", SourceId: source, IdempotencyKey: fmt.Sprintf("named-%d", i)}.Build())
		require.NoError(t, err)
	}
	zero := app.NotificationTopic_builder{ResourceId: f.b.Tenant.Bytes()}.Build()
	counts, err := f.server.Count(ctx, app.NotificationCountRequest_builder{
		TenantId: f.b.Tenant.Bytes(), Topics: []*app.NotificationTopic{zero, maintenance, f.topic},
		Filter: app.NotificationReadFilter_builder{Kinds: []string{"job.completed"}, UnreadOnly: true}.Build(),
	}.Build())
	require.NoError(t, err)
	require.EqualValues(t, 3, counts.GetCount())
	require.EqualValues(t, 1, counts.GetSourceCount())
	require.Equal(t, f.b.Holder.Bytes(), counts.GetReaderId())
	require.Len(t, counts.GetTopics(), 3)
	require.True(t, proto.Equal(zero, counts.GetTopics()[0].GetTopic()))
	require.Zero(t, counts.GetTopics()[0].GetCount())
	require.EqualValues(t, 2, counts.GetTopics()[1].GetCount())
	require.EqualValues(t, 1, counts.GetTopics()[2].GetCount())
	issuer := notificationAs(t.Context(), f.b.Tenant, f.issuer.GetId())
	other, err := f.server.Count(issuer, app.NotificationCountRequest_builder{TenantId: f.b.Tenant.Bytes(), Topics: []*app.NotificationTopic{f.topic}, Filter: filter}.Build())
	require.NoError(t, err)
	require.EqualValues(t, 125, other.GetCount())
	require.EqualValues(t, 1, other.GetSourceCount())
}

func TestNotifyBatchMarkIsAtomicAndConcurrentRetriesAgree(t *testing.T) {
	f := notifications(t)
	ctx := f.b.travels(t.Context())
	a, err := f.client.Add(ctx, f.add("a"))
	require.NoError(t, err)
	b, err := f.client.Add(ctx, f.add("b"))
	require.NoError(t, err)
	badRequest := f.add("forbidden")
	badRequest.SetTopic(app.NotificationTopic_builder{Name: ptr("forbidden")}.Build())
	bad, err := f.client.Add(ctx, badRequest)
	require.NoError(t, err)
	_, err = f.client.Mark(ctx, f.markRefs(a, bad))
	require.Equal(t, codes.NotFound, status.Code(err))
	require.Zero(t, f.b.Ent.NotificationReceiptRecord.Query().CountX(t.Context()))
	type result struct {
		value *app.NotificationMarkResponse
		err   error
	}
	done := make(chan result, 6)
	for range 6 {
		go func() { v, err := f.client.Mark(ctx, f.markRefs(b, a)); done <- result{v, err} }()
	}
	var first *app.NotificationMarkResponse
	for range 6 {
		r := <-done
		require.NoError(t, r.err)
		if first == nil {
			first = r.value
		} else {
			require.True(t, proto.Equal(first, r.value))
		}
	}
	require.Len(t, first.GetItems(), 2)
	require.Equal(t, b.GetId(), first.GetItems()[0].GetNotificationId())
	require.Equal(t, a.GetId(), first.GetItems()[1].GetNotificationId())
	arrived, err := f.client.Add(ctx, f.add("arrived"))
	require.NoError(t, err)
	_, err = f.client.Mark(ctx, f.markRefs(b, a))
	require.NoError(t, err)
	read := f.read()
	read.SetFilter(app.NotificationReadFilter_builder{UnreadOnly: true}.Build())
	page, err := f.client.Read(ctx, read)
	require.NoError(t, err)
	require.Len(t, page.GetItems(), 1)
	require.Equal(t, arrived.GetId(), page.GetItems()[0].GetNotification().GetId())
}
