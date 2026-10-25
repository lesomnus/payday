package notify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/notify"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/watch"
)

const (
	tenantDomain  pdid.Domain = 1
	readerDomain  pdid.Domain = 201
	noticeDomain  pdid.Domain = 202
	receiptDomain pdid.Domain = 203
	jobDomain     pdid.Domain = 204
)

var register sync.Once

type policy struct {
	publisher func(context.Context, pdid.Id, notify.Topic) (string, error)
	reader    func(context.Context, pdid.Id) (pdid.Id, error)
	read      func(context.Context, pdid.Id, pdid.Id, notify.Topic) error
}

func (p *policy) Publisher(ctx context.Context, tenant pdid.Id, topic notify.Topic) (string, error) {
	if p.publisher != nil {
		return p.publisher(ctx, tenant, topic)
	}
	return "test-worker", nil
}
func (p *policy) Reader(ctx context.Context, tenant pdid.Id) (pdid.Id, error) {
	if p.reader != nil {
		return p.reader(ctx, tenant)
	}
	return frame.Must(ctx).Actor, nil
}
func (p *policy) Read(ctx context.Context, tenant, reader pdid.Id, topic notify.Topic) error {
	if p.read != nil {
		return p.read(ctx, tenant, reader, topic)
	}
	return nil
}

type state struct {
	notifications map[pdid.Id]notify.Notification
	receipts      map[[2]pdid.Id]notify.Receipt
}

func (s *state) copy() *state {
	o := &state{notifications: make(map[pdid.Id]notify.Notification), receipts: make(map[[2]pdid.Id]notify.Receipt)}
	for k, v := range s.notifications {
		o.notifications[k] = v
	}
	for k, v := range s.receipts {
		o.receipts[k] = v
	}
	return o
}

// This fake exercises the runtime's transaction and policy boundaries. It is
// deliberately not a SQL implementation or evidence for PostgreSQL behavior.
type memory struct {
	data        *state
	latest      *memoryTx
	rebound     dialect.Driver
	commitError error
	insertError error
	selected    int
	watch       *watch.Watch
}

type memoryTx struct {
	owner *memory
	data  *state
}

func (m *memory) Tx(context.Context) (dialect.Tx, error) {
	m.latest = &memoryTx{owner: m, data: m.data.copy()}
	return m.latest, nil
}
func (m *memory) Exec(context.Context, string, any, any) error    { return nil }
func (m *memory) Query(context.Context, string, any, any) error   { return nil }
func (m *memory) Close() error                                    { return nil }
func (m *memory) Dialect() string                                 { return dialect.SQLite }
func (m *memoryTx) Exec(context.Context, string, any, any) error  { return nil }
func (m *memoryTx) Query(context.Context, string, any, any) error { return nil }
func (m *memoryTx) Commit() error {
	if m.owner.commitError != nil {
		return m.owner.commitError
	}
	*m.owner.data = *m.data
	return nil
}
func (m *memoryTx) Rollback() error { return nil }

type store struct {
	owner *memory
	data  *state
	drv   dialect.Driver
}

func (s store) Driver() dialect.Driver { return s.drv }
func (s store) Rebind(drv dialect.Driver) (notify.Store, error) {
	s.owner.rebound = drv
	data := s.data
	if dialect.InTx(drv) {
		data = s.owner.latest.data
	}
	return store{owner: s.owner, data: data, drv: drv}, nil
}
func (s store) Insert(ctx context.Context, v notify.Notification) (notify.Notification, error) {
	v.ID = pdid.New(noticeDomain)
	s.data.notifications[v.ID] = v
	if s.owner.watch != nil {
		s.owner.watch.Note(ctx, watch.Change{By: "test-notification", Key: v.ID})
	}
	return v, s.owner.insertError
}
func (s store) Find(_ context.Context, id pdid.Id) (notify.Notification, error) {
	v, ok := s.data.notifications[id]
	if !ok {
		return notify.Notification{}, status.Error(codes.NotFound, "notification not found")
	}
	return v, nil
}
func (s store) Select(_ context.Context, q notify.Selection) ([]notify.Notification, error) {
	s.owner.selected++
	var out []notify.Notification
	for _, v := range s.data.notifications {
		if v.Tenant != q.Tenant || !slices.Contains(q.Topics, v.Topic) {
			continue
		}
		if q.Position != nil && (v.DateCreated.After(q.Position.DateCreated) ||
			(v.DateCreated.Equal(q.Position.DateCreated) && bytes.Compare(v.ID.Bytes(), q.Position.ID.Bytes()) >= 0)) {
			continue
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b notify.Notification) int {
		if !a.DateCreated.Equal(b.DateCreated) {
			return b.DateCreated.Compare(a.DateCreated)
		}
		return bytes.Compare(b.ID.Bytes(), a.ID.Bytes())
	})
	return out[:min(len(out), q.Limit)], nil
}
func (s store) FindPublication(_ context.Context, tenant pdid.Id, producer, key string) (notify.Notification, bool, error) {
	for _, v := range s.data.notifications {
		if v.Tenant == tenant && v.Producer == producer && v.IdempotencyKey == key {
			return v, true, nil
		}
	}
	return notify.Notification{}, false, nil
}
func (s store) Receipts(_ context.Context, tenant, reader pdid.Id, ids []pdid.Id) ([]notify.Receipt, error) {
	var out []notify.Receipt
	for _, id := range ids {
		if v, ok := s.data.receipts[[2]pdid.Id{id, reader}]; ok && v.Tenant == tenant {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s store) InsertReceipt(ctx context.Context, v notify.Receipt) (notify.Receipt, error) {
	v.ID = pdid.New(receiptDomain)
	s.data.receipts[[2]pdid.Id{v.Notification, v.Reader}] = v
	if s.owner.watch != nil {
		s.owner.watch.Note(ctx, watch.Change{By: "test-receipt", Key: v.ID})
	}
	return v, nil
}
func (s store) Change(_ context.Context, change watch.Change) (notify.Hint, bool, error) {
	if change.By != "test-hint" {
		return notify.Hint{}, false, nil
	}
	var h notify.Hint
	err := json.Unmarshal(change.Patch, &h)
	return h, true, err
}

type fixture struct {
	s       *notify.Service
	store   store
	policy  *policy
	watch   *watch.Watch
	tenant  pdid.Id
	reader  pdid.Id
	topic   notify.Topic
	ctx     context.Context
	clock   time.Time
	options notify.Options
}

func setup(t *testing.T) *fixture {
	t.Helper()
	register.Do(func() {
		pdid.Register("notify_test.Tenant", tenantDomain, "notify-test-tenant")
		pdid.RegisterTenant(tenantDomain)
		pdid.Register("notify_test.Reader", readerDomain, "notify-test-reader")
		pdid.Register("notify_test.Notification", noticeDomain, "notify-test-notification")
		pdid.Register("notify_test.Receipt", receiptDomain, "notify-test-receipt")
		pdid.Register("notify_test.Job", jobDomain, "notify-test-job")
	})
	f := &fixture{tenant: pdid.New(tenantDomain), reader: pdid.New(readerDomain), topic: notify.Resource(pdid.New(jobDomain)),
		clock: time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC), policy: &policy{}, watch: watch.New(watch.Memory())}
	f.ctx = frame.Into(t.Context(), frame.New(f.reader, f.tenant, frame.Whole()).WithScope(frame.Only(f.tenant)))
	m := &memory{data: &state{notifications: map[pdid.Id]notify.Notification{}, receipts: map[[2]pdid.Id]notify.Receipt{}}, watch: f.watch}
	f.store = store{owner: m, data: m.data, drv: m}
	f.options = notify.Options{CursorKey: bytes.Repeat([]byte{7}, 32), Clock: func() time.Time { return f.clock }}
	var err error
	f.s, err = notify.New(f.store, f.watch, f.policy, f.options)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func expectCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("code = %s, want %s: %v", got, want, err)
	}
}

func (f *fixture) add(t *testing.T, key string) notify.Notification {
	t.Helper()
	v, err := f.s.Add(f.ctx, notify.AddRequest{Tenant: f.tenant, Topic: f.topic, Kind: "job.completed", IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTopicsAndTenantAreCheckedBeforeReading(t *testing.T) {
	f := setup(t)
	for _, topics := range [][]notify.Topic{nil, {f.topic, f.topic}, {notify.Named("")}, {{Resource: f.topic.Resource, Name: "also-set"}}, {notify.Named(string([]byte{255}))}, {notify.Named("a\x00b")}} {
		_, err := f.s.List(f.ctx, notify.Query{Tenant: f.tenant, Topics: topics})
		expectCode(t, err, codes.InvalidArgument)
	}
	_, err := f.s.List(f.ctx, notify.Query{Tenant: pdid.New(tenantDomain), Topics: []notify.Topic{f.topic}})
	expectCode(t, err, codes.PermissionDenied)
	f.policy.read = func(_ context.Context, _, _ pdid.Id, topic notify.Topic) error {
		if topic.Name == "forbidden" {
			return status.Error(codes.PermissionDenied, "denied")
		}
		return nil
	}
	_, err = f.s.List(f.ctx, notify.Query{Tenant: f.tenant, Topics: []notify.Topic{f.topic, notify.Named("forbidden")}})
	expectCode(t, err, codes.PermissionDenied)
	if f.store.owner.selected != 0 {
		t.Fatal("a query ran before complete authorization")
	}
	for _, topic := range []notify.Topic{f.topic, notify.Named(f.topic.Resource.String()), notify.Named(" Maintenance / A ")} {
		parsed, err := notify.ParseTopicKey(topic.Key())
		if err != nil || parsed != topic {
			t.Fatalf("topic did not round trip exactly: %+v, %v", parsed, err)
		}
	}
	if f.topic.Key() == notify.Named(f.topic.Resource.String()).Key() {
		t.Fatal("resource and named topics collided")
	}
}

func TestCursorIsBoundToMethodReaderCredentialAndTopicSet(t *testing.T) {
	f := setup(t)
	for _, key := range []string{"one", "two", "three"} {
		f.add(t, key)
	}
	q := notify.Query{Tenant: f.tenant, Topics: []notify.Topic{f.topic, notify.Named("maintenance")}, Size: 1}
	first, err := f.s.List(f.ctx, q)
	if err != nil || len(first.Items) != 1 || first.Next == "" {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	q.Cursor = first.Next
	slices.Reverse(q.Topics)
	next, err := f.s.List(f.ctx, q)
	if err != nil || len(next.Items) != 1 || first.Items[0].ID == next.Items[0].ID {
		t.Fatalf("continuation: %+v, %v", next, err)
	}
	_, err = f.s.Read(f.ctx, q)
	expectCode(t, err, codes.InvalidArgument)
	changed := frame.Into(f.ctx, frame.New(f.reader, f.tenant, frame.Whole().To("/app.NotificationService/List")).WithScope(frame.Only(f.tenant)))
	_, err = f.s.List(changed, q)
	expectCode(t, err, codes.InvalidArgument)
	f.policy.reader = func(context.Context, pdid.Id) (pdid.Id, error) { return pdid.New(readerDomain), nil }
	_, err = f.s.List(f.ctx, q)
	expectCode(t, err, codes.InvalidArgument)
	f.policy.reader = nil
	q.Topics = []notify.Topic{f.topic}
	_, err = f.s.List(f.ctx, q)
	expectCode(t, err, codes.InvalidArgument)
	q.Topics = []notify.Topic{f.topic, notify.Named("maintenance")}
	q.Cursor = "A" + q.Cursor[1:]
	_, err = f.s.List(f.ctx, q)
	expectCode(t, err, codes.InvalidArgument)
}

func TestPublicationRetryAndLazyReadStateAreIndependent(t *testing.T) {
	f := setup(t)
	payload := &anypb.Any{TypeUrl: "example.invalid/Unknown", Value: []byte{1, 2, 3}}
	req := notify.AddRequest{Tenant: f.tenant, Topic: f.topic, Kind: "job.completed", IdempotencyKey: "occurrence", Payload: payload}
	first, err := f.s.Add(f.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.store.data.receipts) != 0 {
		t.Fatal("publication eagerly created a receipt")
	}
	f.clock = f.clock.Add(time.Hour)
	retry, err := f.s.Add(f.ctx, req)
	if err != nil || retry.ID != first.ID || retry.DateCreated != first.DateCreated || len(f.store.data.notifications) != 1 {
		t.Fatalf("retry changed the publication: %+v, %v", retry, err)
	}
	payload.Value[0] = 9
	first.Payload.Value[1] = 8
	got, err := f.s.Get(f.ctx, first.ID)
	if err != nil || !bytes.Equal(got.Payload.Value, []byte{1, 2, 3}) {
		t.Fatalf("payload was aliased or decoded: %+v, %v", got.Payload, err)
	}
	_, err = f.s.Add(f.ctx, req)
	expectCode(t, err, codes.AlreadyExists)
	marked, err := f.s.Mark(f.ctx, first.ID)
	if err != nil || !marked.DataRead.Equal(f.clock) {
		t.Fatalf("first mark: %+v, %v", marked, err)
	}
	f.clock = f.clock.Add(time.Hour)
	markedAgain, err := f.s.Mark(f.ctx, first.ID)
	if err != nil || markedAgain != marked || len(f.store.data.receipts) != 1 {
		t.Fatalf("retry changed the receipt: %+v, %v", markedAgain, err)
	}
	page, err := f.s.Read(f.ctx, notify.Query{Tenant: f.tenant, Topics: []notify.Topic{f.topic}})
	if err != nil || len(page.Items) != 1 || page.Items[0].Receipt == nil || page.Items[0].Receipt.ID != marked.ID {
		t.Fatalf("reader projection: %+v, %v", page, err)
	}
	other := pdid.New(readerDomain)
	ctx := frame.Into(f.ctx, frame.New(other, f.tenant, frame.Whole()).WithScope(frame.Only(f.tenant)))
	page, err = f.s.Read(ctx, notify.Query{Tenant: f.tenant, Topics: []notify.Topic{f.topic}})
	if err != nil || page.Reader != other || page.Items[0].Receipt != nil {
		t.Fatalf("another reader received the receipt: %+v, %v", page, err)
	}
	f.policy.read = func(context.Context, pdid.Id, pdid.Id, notify.Topic) error {
		return errors.New("a private reason this topic is no longer readable")
	}
	_, err = f.s.Get(f.ctx, first.ID)
	expectCode(t, err, codes.NotFound)
	_, err = f.s.Mark(f.ctx, first.ID)
	expectCode(t, err, codes.NotFound)
}

func TestFailedCommitPublishesNothing(t *testing.T) {
	f := setup(t)
	changes, stop := f.watch.Subscribe()
	defer stop()
	f.store.owner.commitError = errors.New("commit refused")
	_, err := f.s.Add(f.ctx, notify.AddRequest{Tenant: f.tenant, Topic: f.topic, Kind: "job.completed", IdempotencyKey: "one"})
	if err == nil || len(f.store.data.notifications) != 0 {
		t.Fatal("failed commit retained its notification")
	}
	select {
	case <-changes:
		t.Fatal("failed commit was published")
	default:
	}
	f.store.owner.commitError = nil
	f.add(t, "one")
	select {
	case event := <-changes:
		if len(event.Changes) != 1 {
			t.Fatalf("successful write published %d changes", len(event.Changes))
		}
	default:
		t.Fatal("successful worker transaction was not published")
	}
}

func TestPublicationPolicyAndPersistentReaderRemainSeparate(t *testing.T) {
	f := setup(t)
	serviceContext := frame.Into(f.ctx, frame.New(pdid.Nil, f.tenant, frame.Whole()).WithScope(frame.Only(f.tenant)))
	req := notify.AddRequest{Tenant: f.tenant, Topic: f.topic, Kind: "job.completed", IdempotencyKey: "service-publication"}
	v, err := f.s.Add(serviceContext, req)
	if err != nil {
		t.Fatalf("authenticated publisher needed a receipt owner: %v", err)
	}
	_, err = f.s.Read(serviceContext, notify.Query{Tenant: f.tenant, Topics: []notify.Topic{f.topic}})
	expectCode(t, err, codes.FailedPrecondition)
	_, err = f.s.Get(serviceContext, v.ID)
	expectCode(t, err, codes.FailedPrecondition)
	_, err = f.s.Mark(serviceContext, v.ID)
	expectCode(t, err, codes.FailedPrecondition)
	robot := pdid.New(jobDomain)
	f.policy.reader = func(context.Context, pdid.Id) (pdid.Id, error) { return robot, nil }
	r, err := f.s.Mark(f.ctx, v.ID)
	if err != nil || r.Reader != robot || r.Reader == f.reader {
		t.Fatalf("explicit server delegation was not used: %+v, %v", r, err)
	}
	f.policy.publisher = func(context.Context, pdid.Id, notify.Topic) (string, error) {
		return "", status.Error(codes.PermissionDenied, "publication revoked")
	}
	_, err = f.s.Add(serviceContext, req)
	expectCode(t, err, codes.PermissionDenied)
}

func TestAbsentAndEmptyPayloadAreDifferentPublications(t *testing.T) {
	f := setup(t)
	req := notify.AddRequest{Tenant: f.tenant, Topic: f.topic, Kind: "job.completed", IdempotencyKey: "one"}
	_, err := f.s.Add(f.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Payload = &anypb.Any{}
	_, err = f.s.Add(f.ctx, req)
	expectCode(t, err, codes.AlreadyExists)
}

func TestJoinedWriteWaitsForTheOuterCommit(t *testing.T) {
	f := setup(t)
	changes, stop := f.watch.Subscribe()
	defer stop()
	err := f.watch.Transaction(f.ctx, "test.outer", func(ctx context.Context) error {
		drv, tx, err := dialect.BeginTx(ctx, f.store.Driver())
		if err != nil {
			return err
		}
		defer tx.Rollback()
		st, err := f.store.Rebind(drv)
		if err != nil {
			return err
		}
		service, err := notify.New(st, f.watch, f.policy, f.options)
		if err != nil {
			return err
		}
		_, err = service.Add(ctx, notify.AddRequest{Tenant: f.tenant, Topic: f.topic, Kind: "job.completed", IdempotencyKey: "one"})
		if err != nil {
			return err
		}
		if len(f.store.data.notifications) != 0 {
			t.Fatal("joined write committed its outer transaction")
		}
		select {
		case <-changes:
			t.Fatal("joined write published before outer commit")
		default:
		}
		return tx.Commit()
	})
	if err != nil || len(f.store.data.notifications) != 1 {
		t.Fatalf("outer commit: %v", err)
	}
	select {
	case <-changes:
	default:
		t.Fatal("outer transaction did not publish its committed change")
	}
}

func TestJoinedFailureRefusesAnOuterPartialCommit(t *testing.T) {
	f := setup(t)
	drv, tx, err := dialect.BeginTx(f.ctx, f.store.Driver())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	st, err := f.store.Rebind(drv)
	if err != nil {
		t.Fatal(err)
	}
	service, err := notify.New(st, f.watch, f.policy, f.options)
	if err != nil {
		t.Fatal(err)
	}
	f.store.owner.insertError = errors.New("recording failed after the write")
	_, err = service.Add(f.ctx, notify.AddRequest{Tenant: f.tenant, Topic: f.topic, Kind: "job.completed", IdempotencyKey: "one"})
	if err == nil {
		t.Fatal("failed recording was accepted")
	}
	if err := tx.Commit(); err == nil || len(f.store.data.notifications) != 0 {
		t.Fatal("outer transaction committed a partially failed operation")
	}
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not make progress")
		var zero T
		return zero
	}
}

func TestListenSendsReadinessOwnReceiptHintsAndPolicyRevocation(t *testing.T) {
	f := setup(t)
	var allowed atomic.Bool
	allowed.Store(true)
	f.policy.read = func(context.Context, pdid.Id, pdid.Id, notify.Topic) error {
		if !allowed.Load() {
			return status.Error(codes.PermissionDenied, "revoked")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	events := make(chan notify.Event, 4)
	done := make(chan error, 1)
	go func() {
		done <- f.s.Listen(ctx, notify.ListenRequest{Tenant: f.tenant, Topics: []notify.Topic{f.topic}}, func(v notify.Event) error {
			events <- v
			return nil
		})
	}()
	ready := receive(t, events)
	if !ready.Ready || ready.Reader != f.reader || len(ready.Topics) != 0 {
		t.Fatalf("first response was not readiness: %+v", ready)
	}
	for _, reader := range []pdid.Id{pdid.New(readerDomain), f.reader} {
		b, _ := json.Marshal(notify.Hint{Tenant: f.tenant, Reader: reader, Topics: []notify.Topic{f.topic}})
		f.watch.Broker().Publish(ctx, watch.Event{Changes: []watch.Change{{By: "test-hint", Patch: b}}})
	}
	hint := receive(t, events)
	if hint.Ready || len(hint.Topics) != 1 || hint.Topics[0] != f.topic {
		t.Fatalf("own receipt did not invalidate the requested topic: %+v", hint)
	}
	allowed.Store(false)
	if err := f.s.Invalidate(f.ctx, f.tenant, []notify.Topic{f.topic}); err != nil {
		t.Fatal(err)
	}
	expectCode(t, receive(t, done), codes.PermissionDenied)
	select {
	case v := <-events:
		t.Fatalf("another reader or revoked topic was disclosed: %+v", v)
	default:
	}
}

func TestListenReportsBacklogAndDisabledBroker(t *testing.T) {
	f := setup(t)
	disabled, err := notify.New(f.store, watch.New(nil), f.policy, f.options)
	if err != nil {
		t.Fatal(err)
	}
	err = disabled.Listen(f.ctx, notify.ListenRequest{Tenant: f.tenant, Topics: []notify.Topic{f.topic}}, func(notify.Event) error { return nil })
	expectCode(t, err, codes.Unavailable)
	ready := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.s.Listen(f.ctx, notify.ListenRequest{Tenant: f.tenant, Topics: []notify.Topic{f.topic}}, func(v notify.Event) error {
			if v.Ready {
				close(ready)
				<-resume
			}
			return nil
		})
	}()
	receive(t, ready)
	for range watch.Backlog + 1 {
		f.watch.Broker().Publish(f.ctx, watch.Event{})
	}
	close(resume)
	expectCode(t, receive(t, done), codes.ResourceExhausted)
}
