package cmd_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/config/brokerpg"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/notify"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/internal/ent"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	entjob "github.com/lesomnus/payday/internal/apptest/internal/ent/job"
	entrobot "github.com/lesomnus/payday/internal/apptest/internal/ent/robot"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// The policy reads real Job and Robot relationships. A resource's domain only
// dispatches the rule; it does not grant access or expand a recipient list.
type notificationJobPolicy struct{ db *ent.Client }

// Policy queries must share the business transaction as well as the records:
// a new issuer can mark an existing notification before that role change commits.
func (p notificationJobPolicy) WithDriver(driver dialect.Driver) (notify.Policy, error) {
	return notificationJobPolicy{db: ent.NewClient(ent.Driver(driver))}, nil
}

func (p notificationJobPolicy) Publisher(ctx context.Context, _ pdid.Id, _ notify.Topic) (string, error) {
	f, ok := frame.From(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "who is asking?")
	}
	if f.Actor.IsZero() {
		return "", status.Error(codes.FailedPrecondition, "this app requires a persistent publisher")
	}
	return f.Actor.String(), nil
}

func (p notificationJobPolicy) Reader(ctx context.Context, _ pdid.Id) (pdid.Id, error) {
	f, ok := frame.From(ctx)
	if !ok {
		return pdid.Nil, status.Error(codes.Unauthenticated, "who is asking?")
	}
	if f.Actor.IsZero() {
		return pdid.Nil, status.Error(codes.FailedPrecondition, "this app requires a persistent reader")
	}
	return f.Actor, nil
}

func (p notificationJobPolicy) Read(ctx context.Context, tenant, reader pdid.Id, topic notify.Topic) error {
	deny := status.Error(codes.PermissionDenied, "this caller cannot read this topic")
	if topic.Name == "maintenance" || topic.Resource == tenant {
		return nil // Explicit application policy, including the Tenant topic.
	}
	if topic.Resource.Domain() != pd.JobDomain {
		return deny
	}
	job, err := p.db.Job.Query().Where(entjob.IdEQ(topic.Resource.Uuid()), entjob.TenantIdEQ(tenant.Uuid())).Only(ctx)
	if err != nil {
		return deny
	}
	if job.RobotId == reader.Uuid() || job.IssuerId == reader.Uuid() {
		return nil
	}
	robot, err := p.db.Robot.Query().Where(entrobot.IdEQ(job.RobotId), entrobot.TenantIdEQ(tenant.Uuid())).Only(ctx)
	if err == nil && robot.OwnerId != nil && *robot.OwnerId == reader.Uuid() {
		return nil
	}
	return deny
}

type notificationFixture struct {
	b       *built
	server  *pd.NotificationServer
	client  app.NotificationServiceClient
	issuer  *app.Holder
	outside *app.Holder
	robot   *app.Robot
	job     *app.Job
	topic   *app.NotificationTopic
}

func notificationServer(t *testing.T, b *built) *pd.NotificationServer {
	t.Helper()
	s, err := pd.Notifications(b.Walled, b.Drv, b.Watch, notificationJobPolicy{db: b.Ent}, notify.Options{CursorKey: bytes.Repeat([]byte{73}, 32)})
	require.NoError(t, err)
	return s
}

func notificationSetup(t *testing.T, b *built) *notificationFixture {
	t.Helper()
	x := require.New(t)
	ctx := b.as(t.Context())
	issuer, err := b.Walled.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "issuer"}.Build())
	x.NoError(err)
	outside, err := b.Walled.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "outside"}.Build())
	x.NoError(err)
	robot, err := b.Walled.Robot().Add(ctx, app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: "executor", OwnerId: b.Holder.Bytes()}.Build())
	x.NoError(err)
	job, err := b.Walled.Job().Add(ctx, app.JobAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), RobotId: robot.GetId(), IssuerId: issuer.GetId()}.Build())
	x.NoError(err)
	s := notificationServer(t, b)
	g := b.grpc(t)
	app.RegisterNotificationServiceServer(g, s)
	client := app.NewNotificationServiceClient(pdtest.Serve(t, g))
	return &notificationFixture{b: b, server: s, client: client, issuer: issuer, outside: outside, robot: robot, job: job,
		topic: app.NotificationTopic_builder{ResourceId: job.GetId()}.Build()}
}

func notifications(t *testing.T) *notificationFixture {
	t.Helper()
	b, _ := build(t)
	return notificationSetup(t, b)
}

func (f *notificationFixture) add(key string) *app.NotificationAddRequest {
	return app.NotificationAddRequest_builder{TenantId: f.b.Tenant.Bytes(), Topic: f.topic, Kind: "job.completed", SourceId: f.job.GetId(), IdempotencyKey: key}.Build()
}

func (f *notificationFixture) read() *app.NotificationReadRequest {
	return app.NotificationReadRequest_builder{TenantId: f.b.Tenant.Bytes(), Topics: []*app.NotificationTopic{f.topic}}.Build()
}

func notificationAs(ctx context.Context, tenant pdid.Id, actor []byte) context.Context {
	id, _ := pdid.From(actor)
	return frame.Into(ctx, frame.New(id, tenant, frame.Whole()).WithScope(frame.Only(tenant)))
}

func notificationCredential(ctx context.Context, alias string) context.Context {
	return auth.PlainProvider("@acme/" + alias).Provide(ctx)
}

func TestNotifyStoresOnePublicationAndLazyIndependentReceipts(t *testing.T) {
	x := require.New(t)
	f := notifications(t)
	ctx := f.b.travels(t.Context())
	for i := range 8 {
		_, err := f.b.Walled.Holder().Add(f.b.as(t.Context()), app.HolderAddRequest_builder{Tenant: app.TenantRef_builder{Id: f.b.Tenant.Bytes()}.Build(), Alias: fmt.Sprintf("reader-%d", i)}.Build())
		x.NoError(err)
	}
	request := f.add("finished")
	request.SetPayload(&anypb.Any{TypeUrl: "type.googleapis.com/app.FutureJobResult", Value: []byte{10, 1, 65}})
	type publication struct {
		value *app.Notification
		err   error
	}
	published := make(chan publication, 6)
	for range 6 {
		go func() {
			value, err := f.client.Add(ctx, proto.CloneOf(request))
			published <- publication{value, err}
		}()
	}
	var v *app.Notification
	for range 6 {
		result := <-published
		x.NoError(result.err)
		if v == nil {
			v = result.value
		} else {
			x.True(proto.Equal(v, result.value), "concurrent Add must create one publication with stable timestamps")
		}
	}
	retry, err := f.client.Add(ctx, proto.CloneOf(request))
	x.NoError(err)
	x.True(proto.Equal(v, retry), "a retry must preserve the publication, including its timestamps")
	changed := proto.CloneOf(request)
	changed.SetKind("job.failed")
	_, err = f.client.Add(ctx, changed)
	x.Equal(codes.AlreadyExists, status.Code(err))
	x.Equal(1, f.b.Ent.NotificationRecord.Query().CountX(t.Context()))
	x.Zero(f.b.Ent.NotificationReceiptRecord.Query().CountX(t.Context()))

	page, err := f.client.Read(ctx, f.read())
	x.NoError(err)
	x.Len(page.GetItems(), 1)
	x.False(page.GetItems()[0].HasReceipt())
	x.True(proto.Equal(request.GetPayload(), page.GetItems()[0].GetNotification().GetPayload()), "unknown payloads remain opaque and readable")
	x.Zero(f.b.Ent.NotificationReceiptRecord.Query().CountX(t.Context()), "Read must not create a receipt")

	type marked struct {
		value *app.NotificationReceipt
		err   error
	}
	results := make(chan marked, 6)
	for range 6 {
		go func() {
			value, err := f.client.Mark(ctx, app.NotificationMarkRequest_builder{Ref: app.NotificationRef_builder{Id: v.GetId()}.Build()}.Build())
			results <- marked{value, err}
		}()
	}
	var first *app.NotificationReceipt
	for range 6 {
		r := <-results
		x.NoError(r.err)
		if first == nil {
			first = r.value
		} else {
			x.True(proto.Equal(first, r.value), "concurrent Mark must return the same first receipt")
		}
	}
	x.Equal(1, f.b.Ent.NotificationReceiptRecord.Query().CountX(t.Context()))
	x.Equal(f.b.Holder.Bytes(), first.GetReaderId())

	issuerCtx := notificationCredential(t.Context(), "issuer")
	issuerPage, err := f.client.Read(issuerCtx, f.read())
	x.NoError(err)
	x.False(issuerPage.GetItems()[0].HasReceipt())
	issuerReceipt, err := f.client.Mark(issuerCtx, app.NotificationMarkRequest_builder{Ref: app.NotificationRef_builder{Id: v.GetId()}.Build()}.Build())
	x.NoError(err)
	x.Equal(f.issuer.GetId(), issuerReceipt.GetReaderId())
	x.NotEqual(first.GetId(), issuerReceipt.GetId())

	robotCtx := notificationAs(t.Context(), f.b.Tenant, f.robot.GetId())
	robotReceipt, err := f.server.Mark(robotCtx, app.NotificationMarkRequest_builder{Ref: app.NotificationRef_builder{Id: v.GetId()}.Build()}.Build())
	x.NoError(err)
	x.Equal(f.robot.GetId(), robotReceipt.GetReaderId(), "a persistent reader need not be a Holder")
	x.Equal(3, f.b.Ent.NotificationReceiptRecord.Query().CountX(t.Context()))
	ownerPage, err := f.client.Read(ctx, f.read())
	x.NoError(err)
	x.True(proto.Equal(first, ownerPage.GetItems()[0].GetReceipt()))
	key := mustId(x, v.GetId())
	x.Equal(1, f.b.Ent.Audit.Query().Where(entaudit.ObjectIdEQ(key.Uuid())).CountX(t.Context()), "publication uses the ordinary recording path")
}

func TestNotifyPagesExplicitTopicsAndBindsCursors(t *testing.T) {
	x := require.New(t)
	f := notifications(t)
	ctx := f.b.travels(t.Context())
	maintenance := app.NotificationTopic_builder{Name: z.Ptr("maintenance")}.Build()
	for i := range 5 {
		request := f.add(fmt.Sprintf("page-%d", i))
		if i%2 == 0 {
			request.SetTopic(maintenance)
		}
		_, err := f.client.Add(ctx, request)
		x.NoError(err)
	}
	request := app.NotificationListRequest_builder{TenantId: f.b.Tenant.Bytes(), Topics: []*app.NotificationTopic{f.topic, maintenance}, Size: 2}.Build()
	first, err := f.client.List(ctx, request)
	x.NoError(err)
	x.Len(first.GetItems(), 2)
	x.NotEmpty(first.GetNext())
	seen := map[string]bool{}
	for _, row := range first.GetItems() {
		seen[string(row.GetId())] = true
	}
	request.SetTopics([]*app.NotificationTopic{maintenance, f.topic})
	request.SetCursor(first.GetNext())
	for {
		page, err := f.client.List(ctx, request)
		x.NoError(err)
		for _, row := range page.GetItems() {
			x.False(seen[string(row.GetId())], "paging repeated a publication")
			seen[string(row.GetId())] = true
		}
		if page.GetNext() == "" {
			break
		}
		request.SetCursor(page.GetNext())
	}
	x.Len(seen, 5)
	request.SetCursor(first.GetNext())
	_, err = f.client.List(notificationCredential(t.Context(), "issuer"), request)
	x.Equal(codes.InvalidArgument, status.Code(err), "a cursor is bound to its reader")
	request.SetTopics([]*app.NotificationTopic{maintenance})
	_, err = f.client.List(ctx, request)
	x.Equal(codes.InvalidArgument, status.Code(err), "a cursor is bound to its topic set")
	_, err = f.client.Read(ctx, app.NotificationReadRequest_builder{TenantId: f.b.Tenant.Bytes(), Topics: []*app.NotificationTopic{f.topic, maintenance}, Cursor: first.GetNext(), Size: 2}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "List cursors cannot be used by Read")
	_, err = f.client.List(ctx, app.NotificationListRequest_builder{TenantId: f.b.Tenant.Bytes()}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "there is no implicit all-topic query")
}

func TestNotifyPolicyCannotBeBypassedByOtherRoutes(t *testing.T) {
	x := require.New(t)
	f := notifications(t)
	v, err := f.client.Add(f.b.travels(t.Context()), f.add("private"))
	x.NoError(err)
	outside := notificationCredential(t.Context(), "outside")
	_, err = f.client.Get(outside, app.NotificationGetRequest_builder{Ref: app.NotificationRef_builder{Id: v.GetId()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err))
	_, err = f.client.Mark(outside, app.NotificationMarkRequest_builder{Ref: app.NotificationRef_builder{Id: v.GetId()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err))
	_, err = f.client.Read(outside, f.read())
	x.Equal(codes.PermissionDenied, status.Code(err))
	_, err = f.client.List(outside, app.NotificationListRequest_builder{TenantId: f.b.Tenant.Bytes(), Topics: []*app.NotificationTopic{f.topic}}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
	stream, err := f.client.Listen(outside, app.NotificationListenRequest_builder{TenantId: f.b.Tenant.Bytes(), Topics: []*app.NotificationTopic{f.topic}}.Build())
	x.NoError(err)
	_, err = stream.Recv()
	x.Equal(codes.PermissionDenied, status.Code(err))

	request := app.NotificationRecordAddRequest_builder{TenantId: f.b.Tenant.Bytes(), Kind: "forged", TopicKey: "n:maintenance", Producer: "caller", IdempotencyKey: "forged", PayloadValue: []byte{}}.Build()
	_, err = f.b.Walled.NotificationRecord().Add(f.b.as(t.Context()), request)
	x.Equal(codes.PermissionDenied, status.Code(err))
	_, err = f.b.Walled.NotificationRecord().Get(f.b.as(t.Context()), app.NotificationRecordGetRequest_builder{Ref: app.NotificationRecordRef_builder{Id: v.GetId()}.Build()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
	_, err = f.b.batched(t, closedGuard).Do(f.b.as(t.Context()), pdpb.BatchRequest_builder{Ops: []*pdpb.Op{op(t, app.NotificationRecordService_Add_FullMethodName, request)}}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err), "Batch must enter the same sealed record path")

	receipt, err := f.client.Mark(f.b.travels(t.Context()), app.NotificationMarkRequest_builder{Ref: app.NotificationRef_builder{Id: v.GetId()}.Build()}.Build())
	x.NoError(err)
	forged := app.NotificationReceiptRecordAddRequest_builder{TenantId: f.b.Tenant.Bytes(), NotificationId: v.GetId(), ReaderId: f.outside.GetId(), DataRead: receipt.GetDataRead()}.Build()
	_, err = f.b.Walled.NotificationReceiptRecord().Add(f.b.as(t.Context()), forged)
	x.Equal(codes.PermissionDenied, status.Code(err), "raw receipt creation must not choose another reader")
	_, err = f.b.Walled.NotificationReceiptRecord().Get(f.b.as(t.Context()), app.NotificationReceiptRecordGetRequest_builder{Ref: app.NotificationReceiptRecordRef_builder{Id: receipt.GetId()}.Build()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
	project := pd.ResourceProjector(f.b.Ent, pd.Wall(), closedGuard)
	n := f.b.Ent.NotificationRecord.Query().OnlyX(t.Context()).Proto()
	public, err := project(f.b.as(t.Context()), n)
	x.NoError(err)
	x.Nil(public, "common ResourceService.Watch cannot disclose a private notification record")
	r := f.b.Ent.NotificationReceiptRecord.Query().OnlyX(t.Context()).Proto()
	public, err = project(f.b.as(t.Context()), r)
	x.NoError(err)
	x.Nil(public, "another stream cannot disclose a reader's receipt record")

	// The optional feature must stay closed even if wiring omits an outer
	// Audit layer or the public notification service entirely.
	sink, err := pd.NewSink(f.b.Ent, bare.WithMinter(pd.Minter()), bare.WithScope(pd.Wall()))
	x.NoError(err)
	_, err = sink.NotificationRecord().Add(f.b.as(t.Context()), request)
	x.Equal(codes.PermissionDenied, status.Code(err))
	_, err = sink.NotificationRecord().Get(f.b.as(t.Context()), app.NotificationRecordGetRequest_builder{Ref: app.NotificationRecordRef_builder{Id: v.GetId()}.Build()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
	_, err = sink.NotificationReceiptRecord().Add(f.b.as(t.Context()), forged)
	x.Equal(codes.PermissionDenied, status.Code(err))
	_, err = sink.NotificationReceiptRecord().Get(f.b.as(t.Context()), app.NotificationReceiptRecordGetRequest_builder{Ref: app.NotificationReceiptRecordRef_builder{Id: receipt.GetId()}.Build()}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))
}

func TestNotifySharesTheBusinessTransaction(t *testing.T) {
	x := require.New(t)
	f := notifications(t)
	ctx := f.b.as(t.Context())
	changes, stop := f.b.Watch.Subscribe()
	defer stop()
	refused := errors.New("domain operation refused after publication")
	complete := func(commit bool) error {
		return f.b.Watch.Transaction(ctx, "/app.JobService/Complete", func(ctx context.Context) error {
			drv, tx, err := dialect.BeginTx(ctx, f.b.Drv)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			stack, err := enttx.Rebind(f.b.Walled, drv)
			if err != nil {
				return err
			}
			if _, err := stack.Job().Patch(ctx, app.JobPatchRequest_builder{Ref: f.job.Ref(), DateUpdated: f.job.GetDateUpdated(), Completed: z.Ptr(true)}.Build()); err != nil {
				return err
			}
			writer, err := f.server.WithDriver(drv)
			if err != nil {
				return err
			}
			if _, err := writer.Add(ctx, f.add("complete")); err != nil {
				return err
			}
			if !commit {
				return refused
			}
			return tx.Commit()
		})
	}
	x.ErrorIs(complete(false), refused)
	x.Zero(f.b.Ent.NotificationRecord.Query().CountX(t.Context()))
	x.False(f.b.Ent.Job.GetX(t.Context(), mustId(x, f.job.GetId()).Uuid()).Completed)
	select {
	case <-changes:
		t.Fatal("a rolled-back business transaction published a hint")
	default:
	}
	x.NoError(complete(true))
	x.Equal(1, f.b.Ent.NotificationRecord.Query().CountX(t.Context()))
	x.True(f.b.Ent.Job.GetX(t.Context(), mustId(x, f.job.GetId()).Uuid()).Completed)
	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("a committed business transaction published no hint")
	}
}

func TestNotifyListenReconcilesCreationReceiptsAndPolicyRevocation(t *testing.T) {
	x := require.New(t)
	f := notifications(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	maintenance := app.NotificationTopic_builder{Name: z.Ptr("maintenance")}.Build()
	request := app.NotificationListenRequest_builder{TenantId: f.b.Tenant.Bytes(), Topics: []*app.NotificationTopic{f.topic, maintenance}}.Build()
	owner, err := f.client.Listen(f.b.travels(ctx), request)
	x.NoError(err)
	issuer, err := f.client.Listen(notificationCredential(ctx, "issuer"), request)
	x.NoError(err)
	ready, err := owner.Recv()
	x.NoError(err)
	x.True(ready.HasReady())
	x.Equal(f.b.Holder.Bytes(), ready.GetReady().GetReaderId())
	ready, err = issuer.Recv()
	x.NoError(err)
	x.True(ready.HasReady())
	x.Equal(f.issuer.GetId(), ready.GetReady().GetReaderId())
	initial, err := f.client.Read(f.b.travels(ctx), f.read())
	x.NoError(err)
	x.Empty(initial.GetItems())

	v, err := f.client.Add(f.b.travels(ctx), f.add("stream"))
	x.NoError(err)
	for _, stream := range []app.NotificationService_ListenClient{owner, issuer} {
		hint, err := stream.Recv()
		x.NoError(err)
		x.Len(hint.GetInvalidated().GetTopics(), 1)
		x.True(proto.Equal(f.topic, hint.GetInvalidated().GetTopics()[0]))
	}
	_, err = f.client.Mark(f.b.travels(ctx), app.NotificationMarkRequest_builder{Ref: app.NotificationRef_builder{Id: v.GetId()}.Build()}.Build())
	x.NoError(err)
	hint, err := owner.Recv()
	x.NoError(err)
	x.True(proto.Equal(f.topic, hint.GetInvalidated().GetTopics()[0]), "first receipt creation must reach the reader's other session")

	// A different topic is a deterministic barrier: if the issuer had received
	// the owner's private receipt hint, its next event would be the Job topic.
	worker := f.add("worker")
	worker.SetTopic(maintenance)
	_, err = f.server.Add(f.b.as(ctx), worker)
	x.NoError(err)
	for _, stream := range []app.NotificationService_ListenClient{owner, issuer} {
		hint, err = stream.Recv()
		x.NoError(err)
		x.Len(hint.GetInvalidated().GetTopics(), 1)
		x.True(proto.Equal(maintenance, hint.GetInvalidated().GetTopics()[0]), "background publication must not expose another reader's receipt hint")
	}
	_, err = f.b.Walled.Job().Patch(f.b.as(ctx), app.JobPatchRequest_builder{Ref: f.job.Ref(), DateUpdated: f.job.GetDateUpdated(), IssuerId: f.outside.GetId()}.Build())
	x.NoError(err)
	x.NoError(f.server.Invalidate(f.b.as(ctx), f.b.Tenant, []notify.Topic{notify.Resource(mustId(x, f.job.GetId()))}))
	_, err = issuer.Recv()
	x.Equal(codes.PermissionDenied, status.Code(err), "an otherwise idle stream must reauthorize after an app policy change")
	_, err = f.client.Get(notificationCredential(ctx, "issuer"), app.NotificationGetRequest_builder{Ref: app.NotificationRef_builder{Id: v.GetId()}.Build()}.Build())
	x.Equal(codes.NotFound, status.Code(err), "old access and receipts must not preserve visibility")
}

func TestNotifyCrossReplicaListen(t *testing.T) {
	if os.Getenv(pdtest.Postgres) == "" {
		t.Skip("PDTEST_POSTGRES is not set")
	}
	x := require.New(t)
	db := dbOf(t)
	broker := config.WatchConfig{Broker: brokerpg.Name}
	one, _ := buildOn(t, db, broker)
	f := notificationSetup(t, one)
	two, _ := buildOn(t, db, broker)
	writer := notificationServer(t, two)
	g := two.grpc(t)
	app.RegisterNotificationServiceServer(g, writer)
	remote := app.NewNotificationServiceClient(pdtest.Serve(t, g))
	ctx, cancel := context.WithTimeout(one.travels(t.Context()), 10*time.Second)
	defer cancel()
	request := app.NotificationListenRequest_builder{TenantId: one.Tenant.Bytes(), Topics: []*app.NotificationTopic{f.topic}}.Build()
	stream, err := f.client.Listen(ctx, request)
	x.NoError(err)
	issuer, err := f.client.Listen(notificationCredential(ctx, "issuer"), request)
	x.NoError(err)
	for _, subscriber := range []app.NotificationService_ListenClient{stream, issuer} {
		ready, err := subscriber.Recv()
		x.NoError(err)
		x.True(ready.HasReady())
	}
	v, err := remote.Add(two.travels(t.Context()), f.add("other-replica"))
	x.NoError(err)
	for _, subscriber := range []app.NotificationService_ListenClient{stream, issuer} {
		hint, err := subscriber.Recv()
		x.NoError(err)
		x.True(proto.Equal(f.topic, hint.GetInvalidated().GetTopics()[0]))
	}
	got, err := f.client.Get(ctx, app.NotificationGetRequest_builder{Ref: app.NotificationRef_builder{Id: v.GetId()}.Build()}.Build())
	x.NoError(err)
	x.Equal(v.GetId(), got.GetId())

	receipt, err := remote.Mark(two.travels(t.Context()), app.NotificationMarkRequest_builder{Ref: app.NotificationRef_builder{Id: v.GetId()}.Build()}.Build())
	x.NoError(err)
	x.Equal(one.Holder.Bytes(), receipt.GetReaderId())
	hint, err := stream.Recv()
	x.NoError(err)
	x.True(proto.Equal(f.topic, hint.GetInvalidated().GetTopics()[0]), "a private receipt signal must reach the same reader on another replica")
	page, err := f.client.Read(ctx, f.read())
	x.NoError(err)
	x.True(proto.Equal(receipt, page.GetItems()[0].GetReceipt()))

	topics := []notify.Topic{notify.Resource(mustId(x, f.job.GetId()))}
	x.NoError(writer.Invalidate(two.as(ctx), two.Tenant, topics))
	for _, subscriber := range []app.NotificationService_ListenClient{stream, issuer} {
		hint, err = subscriber.Recv()
		x.NoError(err)
		x.True(proto.Equal(f.topic, hint.GetInvalidated().GetTopics()[0]), "a server-authored policy signal must cross the broker")
	}
	_, err = two.Walled.Job().Patch(notificationAs(ctx, two.Tenant, one.Holder.Bytes()), app.JobPatchRequest_builder{
		Ref: f.job.Ref(), DateUpdated: f.job.GetDateUpdated(), IssuerId: f.outside.GetId(),
	}.Build())
	x.NoError(err)
	x.NoError(writer.Invalidate(two.as(ctx), two.Tenant, topics))
	_, err = issuer.Recv()
	x.Equal(codes.PermissionDenied, status.Code(err), "a remote policy change must revoke the existing issuer subscription")
}

func TestNotifyPolicyReadsTheBusinessTransaction(t *testing.T) {
	x := require.New(t)
	f := notifications(t)
	owner := f.b.as(t.Context())
	v, err := f.server.Add(owner, f.add("new-issuer"))
	x.NoError(err)
	ref := app.NotificationRef_builder{Id: v.GetId()}.Build()
	outside := notificationAs(t.Context(), f.b.Tenant, f.outside.GetId())
	_, err = f.server.Get(outside, app.NotificationGetRequest_builder{Ref: ref}.Build())
	x.Equal(codes.NotFound, status.Code(err), "the next issuer has no access before the business change")

	var receipt *app.NotificationReceipt
	x.NoError(f.b.Watch.Transaction(owner, "/app.JobService/Transfer", func(ctx context.Context) error {
		drv, tx, err := dialect.BeginTx(ctx, f.b.Drv)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		stack, err := enttx.Rebind(f.b.Walled, drv)
		if err != nil {
			return err
		}
		if _, err := stack.Job().Patch(ctx, app.JobPatchRequest_builder{
			Ref: f.job.Ref(), DateUpdated: f.job.GetDateUpdated(), IssuerId: f.outside.GetId(),
		}.Build()); err != nil {
			return err
		}
		writer, err := f.server.WithDriver(drv)
		if err != nil {
			return err
		}
		// This query must observe the pending issuer change. Using a policy on
		// the original driver would deny access or read outside this transaction.
		receipt, err = writer.Mark(notificationAs(ctx, f.b.Tenant, f.outside.GetId()), app.NotificationMarkRequest_builder{Ref: ref}.Build())
		if err != nil {
			return err
		}
		return tx.Commit()
	}))
	x.Equal(f.outside.GetId(), receipt.GetReaderId())
	x.Equal(1, f.b.Ent.NotificationReceiptRecord.Query().CountX(t.Context()))
	_, err = f.server.Get(notificationAs(t.Context(), f.b.Tenant, f.issuer.GetId()), app.NotificationGetRequest_builder{Ref: ref}.Build())
	x.Equal(codes.NotFound, status.Code(err), "the previous issuer loses access after the same commit")
}
