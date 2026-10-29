package notify_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/notify"
	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGlobalNotificationsRetainAuthenticationAndPolicy(t *testing.T) {
	f := setup(t)
	f.options.Global = true
	f.tenant = pdid.Nil
	f.ctx = frame.Into(t.Context(), frame.New(f.reader, pdid.Nil, frame.Whole()))
	var err error
	f.s, err = notify.New(f.store, f.watch, f.policy, f.options)
	require.NoError(t, err)
	v := f.add(t, "global")
	require.True(t, v.Tenant.IsZero())
	_, err = f.s.Add(t.Context(), notify.AddRequest{Topic: f.topic, Kind: "done", IdempotencyKey: "unauthenticated"})
	expectCode(t, err, codes.Unauthenticated)
	_, err = f.s.Add(f.ctx, notify.AddRequest{Tenant: pdid.New(tenantDomain), Topic: f.topic, Kind: "done", IdempotencyKey: "tenant"})
	expectCode(t, err, codes.InvalidArgument)
	r, err := f.s.Mark(f.ctx, notify.MarkRequest{Refs: []pdid.Id{v.ID}})
	require.NoError(t, err)
	require.True(t, r.Items[0].Tenant.IsZero())
	require.Equal(t, f.reader, r.Reader)
	f.policy.read = func(context.Context, pdid.Id, pdid.Id, notify.Topic) error {
		return status.Error(codes.PermissionDenied, "revoked")
	}
	_, err = f.s.Get(f.ctx, v.ID)
	expectCode(t, err, codes.NotFound)
	_, err = f.s.Count(f.ctx, notify.CountRequest{Topics: []notify.Topic{f.topic}})
	expectCode(t, err, codes.PermissionDenied)
}

func TestReadFiltersBeforePagingAndCountsDistinctSources(t *testing.T) {
	f := setup(t)
	other := notify.Named("another")
	zero := notify.Named("empty")
	source := pdid.New(jobDomain)
	var refs []pdid.Id
	for i := range 125 {
		f.clock = f.clock.Add(time.Second)
		v, err := f.s.Add(f.ctx, notify.AddRequest{Tenant: f.tenant, Topic: f.topic, Kind: "task.done", Source: source, IdempotencyKey: fmt.Sprint(i)})
		require.NoError(t, err)
		refs = append(refs, v.ID)
	}
	for start := 1; start < len(refs); start += 100 {
		_, err := f.s.Mark(f.ctx, notify.MarkRequest{Tenant: f.tenant, Refs: refs[start:min(start+100, len(refs))]})
		require.NoError(t, err)
	}
	filter := notify.ReadFilter{Kinds: []string{"task.done"}, Sources: []pdid.Id{source}, UnreadOnly: true}
	page, err := f.s.Read(f.ctx, notify.Query{Tenant: f.tenant, Topics: []notify.Topic{f.topic}, Size: 1, Filter: filter})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, refs[0], page.Items[0].Notification.ID, "older unread results must not disappear behind a page of read rows")
	require.Empty(t, page.Next)
	for i, input := range []notify.AddRequest{
		{Tenant: f.tenant, Topic: other, Kind: "task.done", Source: source},
		{Tenant: f.tenant, Topic: other, Kind: "task.done"},
		{Tenant: f.tenant, Topic: f.topic, Kind: "session.done", Source: pdid.New(jobDomain)},
	} {
		input.IdempotencyKey = fmt.Sprintf("extra-%d", i)
		_, err := f.s.Add(f.ctx, input)
		require.NoError(t, err)
	}
	counts, err := f.s.Count(f.ctx, notify.CountRequest{Tenant: f.tenant, Topics: []notify.Topic{zero, other, f.topic}, Filter: notify.ReadFilter{Kinds: []string{"task.done"}, UnreadOnly: true}})
	require.NoError(t, err)
	require.EqualValues(t, 3, counts.Count)
	require.EqualValues(t, 1, counts.SourceCount)
	require.Equal(t, f.reader, counts.Reader)
	require.Equal(t, []notify.TopicCount{{Topic: zero}, {Topic: other, Count: 2, SourceCount: 1}, {Topic: f.topic, Count: 1, SourceCount: 1}}, counts.Topics)
	second := pdid.New(readerDomain)
	ctx := frame.Into(t.Context(), frame.New(second, f.tenant, frame.Whole()).WithScope(frame.Only(f.tenant)))
	counts, err = f.s.Count(ctx, notify.CountRequest{Tenant: f.tenant, Topics: []notify.Topic{f.topic}, Filter: filter})
	require.NoError(t, err)
	require.EqualValues(t, 125, counts.Count, "one reader's receipts must not filter another reader's records")
}

func TestBatchMarkIsAtomicOrderedAndPreservesFirstRead(t *testing.T) {
	f := setup(t)
	a, b := f.add(t, "a"), f.add(t, "b")
	other := notify.Named("forbidden")
	c, err := f.s.Add(f.ctx, notify.AddRequest{Tenant: f.tenant, Topic: other, Kind: "done", IdempotencyKey: "c"})
	require.NoError(t, err)
	f.policy.read = func(_ context.Context, _, _ pdid.Id, topic notify.Topic) error {
		if topic == other {
			return status.Error(codes.PermissionDenied, "denied")
		}
		return nil
	}
	_, err = f.s.Mark(f.ctx, notify.MarkRequest{Tenant: f.tenant, Refs: []pdid.Id{a.ID, c.ID}})
	expectCode(t, err, codes.NotFound)
	require.Empty(t, f.store.data.receipts)
	request := notify.MarkRequest{Tenant: f.tenant, Refs: []pdid.Id{b.ID, a.ID}}
	marked, err := f.s.Mark(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, []pdid.Id{b.ID, a.ID}, []pdid.Id{marked.Items[0].Notification, marked.Items[1].Notification})
	f.clock = f.clock.Add(time.Hour)
	arrived := f.add(t, "arrived-after-view")
	retry, err := f.s.Mark(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, marked, retry)
	page, err := f.s.Read(f.ctx, notify.Query{Tenant: f.tenant, Topics: []notify.Topic{f.topic}, Filter: notify.ReadFilter{UnreadOnly: true}})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, arrived.ID, page.Items[0].Notification.ID)
	for _, refs := range [][]pdid.Id{nil, {a.ID, a.ID}, slices.Repeat([]pdid.Id{a.ID}, 101), {pdid.Nil}} {
		_, err = f.s.Mark(f.ctx, notify.MarkRequest{Tenant: f.tenant, Refs: refs})
		expectCode(t, err, codes.InvalidArgument)
	}
}

func TestFilteredCursorBindsFilterAndAppNamespace(t *testing.T) {
	f := setup(t)
	f.add(t, "a")
	f.add(t, "b")
	q := notify.Query{Tenant: f.tenant, Topics: []notify.Topic{f.topic}, Size: 1, Filter: notify.ReadFilter{Kinds: []string{"job.completed"}}}
	page, err := f.s.Read(f.ctx, q)
	require.NoError(t, err)
	require.NotEmpty(t, page.Next)
	q.Cursor = page.Next
	q.Filter.UnreadOnly = true
	_, err = f.s.Read(f.ctx, q)
	expectCode(t, err, codes.InvalidArgument)
	q.Filter.UnreadOnly = false
	f.options.Namespace = "another-app"
	other, err := notify.New(f.store, f.watch, f.policy, f.options)
	require.NoError(t, err)
	_, err = other.Read(f.ctx, q)
	expectCode(t, err, codes.InvalidArgument)
}
