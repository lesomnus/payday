package cmd_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/config/brokerpg"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/predicate"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/robot"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/resource"
	"github.com/lesomnus/payday/watch"
)

func TestResourceWatchImages(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := pdtest.Serve(t, b.grpc(t))
	c := app.NewClient(conn)
	resources := pdpb.NewResourceServiceClient(conn)
	ctx, cancel := context.WithTimeout(b.travels(ctx), 10*time.Second)
	defer cancel()
	stream, err := resources.Watch(ctx, pdpb.WatchRequest_builder{Filter: `(has(event.value) && type(event.value) == app.Robot) || (has(event.previous) && type(event.previous) == app.Robot)`}.Build())
	x.NoError(err)
	_, err = stream.Header()
	x.NoError(err)
	first, err := c.Robot().Add(ctx, app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "first", Secret: []byte("hidden")}.Build())
	x.NoError(err)
	second, err := c.Robot().Patch(ctx, app.RobotPatchRequest_builder{Ref: first.Ref(), Alias: z.Ptr("second"), DateUpdated: first.GetDateUpdated()}.Build())
	x.NoError(err)
	_, err = c.Robot().Patch(ctx, app.RobotPatchRequest_builder{Ref: first.Ref(), Alias: z.Ptr("third"), DateUpdated: second.GetDateUpdated()}.Build())
	x.NoError(err)
	_, err = c.Robot().Erase(ctx, first.Ref())
	x.NoError(err)
	// All mutations, including deletion, finish before consuming the stream. A
	// current-row re-read would lose these values and cannot pass this test.
	ids := map[string]bool{}
	for i, want := range []struct {
		kind     pdpb.ChangeKind
		old, new string
	}{
		{pdpb.ChangeKind_CHANGE_KIND_CREATED, "", "first"},
		{pdpb.ChangeKind_CHANGE_KIND_UPDATED, "", "second"},
		{pdpb.ChangeKind_CHANGE_KIND_UPDATED, "", "third"},
		{pdpb.ChangeKind_CHANGE_KIND_DELETED, "third", ""},
	} {
		event, err := stream.Recv()
		x.NoError(err, "event %d", i)
		x.Equal(want.kind, event.GetKind())
		x.Len(event.GetId(), 16)
		x.False(ids[string(event.GetId())])
		ids[string(event.GetId())] = true
		x.NoError(event.GetDateCreated().CheckValid())
		x.Equal(event.GetMethod(), event.GetCallMethod())
		for _, side := range []struct {
			raw     interface{ UnmarshalTo(proto.Message) error }
			present bool
			alias   string
		}{
			{event.GetPrevious(), event.HasPrevious(), want.old}, {event.GetValue(), event.HasValue(), want.new},
		} {
			x.Equal(side.alias != "", side.present)
			if side.present {
				v := &app.Robot{}
				x.NoError(side.raw.UnmarshalTo(v))
				x.Equal(side.alias, v.GetAlias())
				x.Equal(first.GetId(), v.GetId())
				x.Empty(v.GetSecret())
			}
		}
	}
}

// Another recorder may need Previous; that must not implicitly turn it on in
// the public event stream or increase the authorization requirements of updates.
type previousConsumer struct{}

func (previousConsumer) Snapshots(string) bare.SnapshotOptions {
	return bare.SnapshotOptions{Previous: true}
}
func (previousConsumer) Record(context.Context, bare.Server, bare.Change) error { return nil }

func TestResourceWatchPreviousOptIn(t *testing.T) {
	for _, previous := range []bool{false, true} {
		t.Run(fmt.Sprint(previous), func(t *testing.T) {
			x := require.New(t)
			b, ctx := build(t)
			ctx, cancel := context.WithTimeout(b.travels(ctx), 10*time.Second)
			defer cancel()
			client := pdpb.NewResourceServiceClient(pdtest.Serve(t, b.grpc(t)))
			stream, err := client.Watch(ctx, pdpb.WatchRequest_builder{Filter: `event.kind == payday.CHANGE_KIND_UPDATED && has(event.value) && type(event.value) == app.Robot`}.Build())
			x.NoError(err)
			_, err = stream.Header()
			x.NoError(err)
			r := pd.ResourceRecorder(b.Watch, true, nil, resource.RecorderOptions{Previous: previous})
			s, err := bare.NewServer(b.Ent, bare.WithMinter(pd.Minter()), bare.WithRecorder(bare.Recorders{previousConsumer{}, r}))
			x.NoError(err)
			var original *app.Robot
			x.NoError(b.Watch.Transaction(ctx, "/jobs/change", func(ctx context.Context) error {
				tx, err := b.Ent.Tx(ctx)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				writer, err := s.WithDriver(tx.Client().Driver())
				if err != nil {
					return err
				}
				v, err := writer.Robot().Add(ctx, app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "before"}.Build())
				if err != nil {
					return err
				}
				original = v
				_, err = writer.Robot().Patch(ctx, app.RobotPatchRequest_builder{Ref: v.Ref(), DateUpdated: v.GetDateUpdated(), Alias: z.Ptr("after")}.Build())
				if err != nil {
					return err
				}
				return tx.Commit()
			}))
			event, err := stream.Recv()
			x.NoError(err)
			x.Equal(pdpb.ChangeKind_CHANGE_KIND_UPDATED, event.GetKind())
			x.Equal(previous, event.HasPrevious())
			current := &app.Robot{}
			x.NoError(event.GetValue().UnmarshalTo(current))
			x.Equal("after", current.GetAlias())
			if previous {
				old := &app.Robot{}
				x.NoError(event.GetPrevious().UnmarshalTo(old))
				x.Equal(original.GetAlias(), old.GetAlias())
			}
			rows, err := b.Ent.Outbox.Query().All(t.Context())
			x.NoError(err)
			found := false
			for _, row := range rows {
				if row.Event == nil {
					continue
				}
				stored := &pdpb.WatchEvent{}
				x.NoError(proto.Unmarshal(*row.Event, stored))
				if bytes.Equal(stored.GetId(), event.GetId()) {
					found = true
					x.True(proto.Equal(event, stored))
				}
			}
			x.True(found, "live and outbox must share the optional-image envelope")
		})
	}
}

func TestResourceCELAndTenantScope(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := pdtest.Serve(t, b.grpc(t))
	c := app.NewClient(conn)
	resources := pdpb.NewResourceServiceClient(conn)
	ctx, cancel := context.WithTimeout(b.travels(ctx), 10*time.Second)
	defer cancel()
	stream, err := resources.Watch(ctx, pdpb.WatchRequest_builder{Filter: `has(event.value) && type(event.value) == app.Robot && event.kind == payday.CHANGE_KIND_CREATED && event.value.alias.startsWith("wanted") && event.value.secret == b""`}.Build())
	x.NoError(err)
	_, err = stream.Header()
	x.NoError(err)
	other, err := b.Ungated.Tenant().Add(t.Context(), app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)
	x.NoError(b.Watch.Transaction(t.Context(), "/direct/seed", func(ctx context.Context) error {
		_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{Tenant: other.Ref(), Alias: "wanted-hidden"}.Build())
		return err
	}))
	_, err = c.Robot().Add(ctx, app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "unwanted"}.Build())
	x.NoError(err)
	visible, err := c.Robot().Add(ctx, app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "wanted-visible", Secret: []byte("never-in-cel")}.Build())
	x.NoError(err)
	event, err := stream.Recv()
	x.NoError(err)
	got := &app.Robot{}
	x.NoError(event.GetValue().UnmarshalTo(got))
	x.Equal(visible.GetId(), got.GetId())
}

func TestResourceWatchRejectsInvalidFilters(t *testing.T) {
	b, ctx := build(t)
	conn := pdtest.Serve(t, b.grpc(t))
	c := pdpb.NewResourceServiceClient(conn)
	for _, filter := range []string{"event.", "42", "event.method", string(make([]byte, 17000))} {
		t.Run(filter[:min(len(filter), 20)], func(t *testing.T) {
			x := require.New(t)
			stream, err := c.Watch(b.travels(ctx), pdpb.WatchRequest_builder{Filter: filter}.Build())
			x.NoError(err)
			_, err = stream.Recv()
			x.Equal(codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestResourceBatchAndOutboxShareEnvelope(t *testing.T) {
	x := require.New(t)
	b, ctx := queued(t)
	conn := pdtest.Serve(t, b.grpc(t))
	c := pdpb.NewResourceServiceClient(conn)
	events, stop := b.Watch.Subscribe()
	defer stop()
	req := func(alias string) *pdpb.Op {
		return op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: alias}.Build())
	}
	_, err := c.Batch(b.travels(ctx), pdpb.BatchRequest_builder{Ops: []*pdpb.Op{req("one"), req("two")}}.Build())
	x.NoError(err)
	live := <-events
	x.Len(live.Changes, 2)
	for _, change := range live.Changes {
		x.Equal(app.RobotService_Add_FullMethodName, change.Resource.GetMethod())
		x.Equal(pdpb.ResourceService_Batch_FullMethodName, change.Resource.GetCallMethod())
	}
	x.NotEqual(live.Changes[0].Resource.GetId(), live.Changes[1].Resource.GetId())
	x.NoError(drainOnce(t, b))
	for i := range 2 {
		replayed := <-events
		x.True(proto.Equal(live.Changes[i].Resource, replayed.Changes[0].Resource))
		x.Equal(live.Method, replayed.Method)
	}
	_, err = c.Batch(b.travels(ctx), pdpb.BatchRequest_builder{Ops: []*pdpb.Op{req("rollback"), req("rollback")}}.Build())
	x.Error(err)
	select {
	case <-events:
		t.Fatal("rolled-back batch published a change")
	default:
	}
	n, err := b.Ent.Outbox.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n)
}

func TestResourceWatchCrossReplicaLargePayload(t *testing.T) {
	if os.Getenv(pdtest.Postgres) == "" {
		t.Skip("PDTEST_POSTGRES is not set")
	}
	x := require.New(t)
	db := dbOf(t)
	w := config.WatchConfig{Broker: brokerpg.Name}
	one, ctx := buildOn(t, db, w)
	two, _ := buildOn(t, db, w)
	reader := pdpb.NewResourceServiceClient(pdtest.Serve(t, one.grpc(t)))
	writer := app.NewClient(pdtest.Serve(t, two.grpc(t)))
	ctx, cancel := context.WithTimeout(one.travels(ctx), 10*time.Second)
	defer cancel()
	stream, err := reader.Watch(ctx, pdpb.WatchRequest_builder{Filter: `has(event.value) && type(event.value) == shared.Thing`}.Build())
	x.NoError(err)
	_, err = stream.Header()
	x.NoError(err)
	large := strings.Repeat("public-payload-", 2000)
	v, err := writer.Thing().Add(two.travels(t.Context()), app.ThingAddRequest_builder{Alias: "large", Description: z.Ptr(large)}.Build())
	x.NoError(err)
	event, err := stream.Recv()
	x.NoError(err)
	got := &app.Thing{}
	x.NoError(event.GetValue().UnmarshalTo(got))
	x.Equal(v.GetId(), got.GetId())
	x.Equal(large, got.GetDescription())
	x.Equal(app.ThingService_Add_FullMethodName, event.GetMethod())
	x.Equal(event.GetMethod(), event.GetCallMethod())
}

type historicalScope struct{ bare.Unscoped }

func (historicalScope) RobotScope(context.Context) (predicate.Robot, error) {
	return robot.AliasEQ("allowed"), nil
}

func TestResourceProjectorUsesHistoricalCustomScope(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	first, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "allowed"}.Build())
	x.NoError(err)
	changed, err := b.Walled.Robot().Patch(b.as(ctx), app.RobotPatchRequest_builder{Ref: first.Ref(), Alias: z.Ptr("denied"), DateUpdated: first.GetDateUpdated()}.Build())
	x.NoError(err)
	project := pd.ResourceProjector(b.Ent, bare.Scopes{pd.Wall(), historicalScope{}}, closedGuard)
	got, err := project(b.as(ctx), first)
	x.NoError(err)
	x.NotNil(got)
	got, err = project(b.as(ctx), changed)
	x.NoError(err)
	x.Nil(got)
	// Losing current-row visibility or removing the row must not rewrite history.
	_, err = b.Walled.Robot().Erase(b.as(ctx), first.Ref())
	x.NoError(err)
	got, err = project(b.as(ctx), first)
	x.NoError(err)
	x.NotNil(got)
}

func TestResourcePublicationPolicyAppliesToBothPaths(t *testing.T) {
	x := require.New(t)
	b, ctx := queued(t)
	before, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	calls := 0
	recorder := pd.ResourceRecorder(b.Watch, true, func(_ context.Context, event *pdpb.WatchEvent) (bool, error) {
		calls++
		v := &app.Robot{}
		if err := event.GetValue().UnmarshalTo(v); err != nil {
			return false, err
		}
		return v.GetAlias() == "published", nil
	}, resource.RecorderOptions{})
	s, err := bare.NewServer(b.Ent, bare.WithMinter(pd.Minter()), bare.WithRecorder(bare.Recorders{pd.Recorder(audit.Contents{}), recorder}))
	x.NoError(err)
	events, stop := b.Watch.Subscribe()
	defer stop()
	x.NoError(b.Watch.Transaction(b.as(ctx), "/jobs/Import", func(ctx context.Context) error {
		for _, alias := range []string{"suppressed", "published"} {
			_, err := s.Robot().Add(ctx, app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: alias}.Build())
			if err != nil {
				return err
			}
		}
		return nil
	}))
	x.Equal(2, calls)
	live := <-events
	x.Len(live.Changes, 1)
	x.Equal("/jobs/Import", live.Changes[0].Resource.GetCallMethod())
	n, err := b.Ent.Outbox.Query().Count(ctx)
	x.NoError(err)
	x.Equal(1, n)
	n, err = b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.Equal(before+2, n)
	x.NoError(drainOnce(t, b))
	retry := <-events
	x.True(proto.Equal(live.Changes[0].Resource, retry.Changes[0].Resource))
}

type failedCommitDriver struct{ dialect.Driver }

func (d failedCommitDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return failedCommitTx{tx}, nil
}

type failedCommitTx struct{ dialect.Tx }

func (t failedCommitTx) Commit() error { _ = t.Tx.Rollback(); return errors.New("commit refused") }

func TestResourceBatchFailedCommitDoesNotPublish(t *testing.T) {
	x := require.New(t)
	b, ctx := queued(t)
	server, err := pd.Resources(b.Walled, failedCommitDriver{b.Drv}, closedGuard, b.Watch, pd.ResourceProjector(b.Ent, pd.Wall(), closedGuard))
	x.NoError(err)
	events, stop := b.Watch.Subscribe()
	defer stop()
	req := pdpb.BatchRequest_builder{Ops: []*pdpb.Op{op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "uncommitted"}.Build())}}.Build()
	_, err = b.Watch.Unary()(b.as(ctx), req, &grpc.UnaryServerInfo{FullMethod: pdpb.ResourceService_Batch_FullMethodName}, func(ctx context.Context, _ any) (any, error) { return server.Batch(ctx, req) })
	x.ErrorContains(err, "commit refused")
	n, err := b.Ent.Robot.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n)
	n, err = b.Ent.Outbox.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n)
	select {
	case <-events:
		t.Fatal("failed commit published")
	default:
	}
}

func TestResourceHardDeletionUsesCurrentRelationScope(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	robots := b.sow(ctx, x, b.Tenant, 2, "member-")
	pair, err := b.Walled.Pairing().Add(b.as(ctx), app.PairingAddRequest_builder{Lead: robots[0].Ref(), Follow: robots[1].Ref()}.Build())
	x.NoError(err)
	_, err = b.Walled.Pairing().Erase(b.as(ctx), pair.Ref())
	x.NoError(err)
	project := pd.ResourceProjector(b.Ent, pd.Wall(), closedGuard)
	got, err := project(b.as(ctx), pair)
	x.NoError(err)
	x.NotNil(got, "the hard-deleted root is authorized from its recorded foreign keys")
	// Related predicates follow current authorization data, not a current root Get.
	err = b.Ent.Robot.DeleteOneId(mustId(x, robots[0].GetId()).Uuid()).Exec(ctx)
	x.NoError(err)
	got, err = project(b.as(ctx), pair)
	x.NoError(err)
	x.Nil(got)
	// An all-tenant policy also exercises a hard-deleted resource whose narrowed
	// root predicate is nil; it must remain a valid query.
	guard := closedGuard
	guard.Policy = console{}
	got, err = pd.ResourceProjector(b.Ent, pd.Wall(), guard)(b.as(ctx), pair)
	x.NoError(err)
	x.NotNil(got)
}

type secretReadScope struct{ bare.Unscoped }

func (secretReadScope) RobotScope(context.Context) (predicate.Robot, error) {
	return robot.SecretIsNil(), nil
}

func TestResourceSnapshotCannotAuthorizeUsingRedactedSecret(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "secret-row", Secret: []byte("private")}.Build())
	x.NoError(err)
	// The public image has no secret, but that cannot make an IS NULL read rule
	// grant access. The omitted SQL column makes the unsupported rule fail closed.
	v.SetSecret(nil)
	got, err := pd.ResourceProjector(b.Ent, secretReadScope{}, closedGuard)(b.as(ctx), v)
	x.Error(err)
	x.Nil(got)
}

type refusedPublisher struct {
	watch.Broker
	attempted chan watch.Event
}

func (p refusedPublisher) PublishConfirmed(_ context.Context, event watch.Event) error {
	select {
	case p.attempted <- event:
	default:
	}
	return errors.New("broker refused")
}

func TestResourceOutboxRetainsFailedPublication(t *testing.T) {
	x := require.New(t)
	b, ctx := queued(t)
	_, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "retry"}.Build())
	x.NoError(err)
	failed := refusedPublisher{Broker: b.Watch.Broker(), attempted: make(chan watch.Event, 1)}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- pd.Drain(b.Ent, failed, time.Millisecond).Spin(ctx) }()
	var attempted watch.Event
	select {
	case attempted = <-failed.attempted:
	case <-ctx.Done():
		t.Fatal("outbox did not try to publish")
	}
	cancel()
	x.NoError(<-done)
	n, err := b.Ent.Outbox.Query().Count(t.Context())
	x.NoError(err)
	x.Equal(1, n)
	events, stop := b.Watch.Subscribe()
	defer stop()
	x.NoError(drainOnce(t, b))
	replayed := <-events
	x.True(proto.Equal(attempted.Changes[0].Resource, replayed.Changes[0].Resource))
}

func TestResourceAndLegacyBatchShareOperationLimit(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	cfg := cmd0
	cfg.Server.Limit = config.LimitConfig{Rate: 0.000001, Burst: 3}
	server, err := b.Grpc(ctx, cfg)
	x.NoError(err)
	conn := pdtest.Serve(t, server)
	request := func(names ...string) *pdpb.BatchRequest {
		ops := make([]*pdpb.Op, 0, len(names))
		for _, alias := range names {
			ops = append(ops, op(t, app.RobotService_Add_FullMethodName, app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: alias}.Build()))
		}
		return pdpb.BatchRequest_builder{Ops: ops}.Build()
	}
	_, err = pdpb.NewBatchServiceClient(conn).Do(b.travels(ctx), request("one", "two"))
	x.NoError(err)
	_, err = pdpb.NewResourceServiceClient(conn).Batch(b.travels(ctx), request("three", "four"))
	x.Equal(codes.ResourceExhausted, status.Code(err))
	n, err := b.Ent.Robot.Query().Count(ctx)
	x.NoError(err)
	x.Equal(2, n)
}
