//go:build !js

package watchnats

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesomnus/payday/notify"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/resource"
	"github.com/lesomnus/payday/watch"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func start(t testing.TB, options *server.Options) *server.Server {
	t.Helper()
	if options == nil {
		options = &server.Options{Host: "127.0.0.1", Port: -1}
	}
	options.NoSigs = true
	options.NoLog = true
	s, err := server.NewServer(options)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	return s
}
func broker(t testing.TB, s *server.Server, namespace string) *Broker {
	t.Helper()
	b, err := New(t.Context(), s.ClientURL(), namespace)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	return b
}
func opened(t testing.TB, b *Broker, scope watch.Interest) *watch.Subscription {
	t.Helper()
	s, err := b.Open(t.Context(), scope)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	return s
}
func next(t testing.TB, s *watch.Subscription) watch.Event {
	t.Helper()
	select {
	case v, ok := <-s.Events:
		require.True(t, ok, "subscription closed")
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("publication was not delivered")
		return watch.Event{}
	}
}
func none(t testing.TB, s *watch.Subscription) {
	t.Helper()
	select {
	case v := <-s.Events:
		t.Fatalf("unexpected event: %+v", v)
	default:
	}
}
func change(route watch.Route) watch.Event {
	return watch.Event{Method: "/app.JobService/Patch", Changes: []watch.Change{{By: "/app.JobService/Patch", Key: pdid.New(3), Routes: []watch.Route{route}}}}
}

func TestThreeReplicasReceiveOnlyTheirInterests(t *testing.T) {
	s := start(t, nil)
	publisher := broker(t, s, "production")
	a := broker(t, s, "production")
	b := broker(t, s, "production")
	c := broker(t, s, "production")
	tenant := pdid.New(1)
	reader := pdid.New(2)
	other := pdid.New(2)
	shared := notify.Hint{Tenant: tenant, Topics: []notify.Topic{{Name: "free form.*.>"}}}
	x := notify.Routes("app", shared)[0]
	shared.Reader = reader
	r := notify.Routes("app", shared)[0]
	shared.Reader = other
	o := notify.Routes("app", shared)[0]
	first := opened(t, a, watch.Interest{Routes: []watch.Route{x, r}})
	second := opened(t, b, watch.Interest{Routes: []watch.Route{watch.RouteKey("app", "Y")}})
	third := opened(t, c, watch.Interest{Routes: []watch.Route{x, o}})
	initial := b.conn.Stats().InMsgs
	v := change(x)
	require.NoError(t, publisher.PublishConfirmed(t.Context(), v))
	require.Equal(t, v.Changes[0].Key, next(t, first).Changes[0].Key)
	next(t, third)
	require.Equal(t, initial, b.conn.Stats().InMsgs, "replica B must receive no packet, not merely filter it locally")
	receipt := change(r)
	require.NoError(t, publisher.PublishConfirmed(t.Context(), receipt))
	next(t, first)
	require.NoError(t, c.flush(t.Context()))
	none(t, third)
	none(t, second)
	// Namespaces and topic kinds are routing boundaries even with the same text.
	isolated := broker(t, s, "staging")
	outside := opened(t, isolated, watch.Interest{})
	require.NoError(t, publisher.PublishConfirmed(t.Context(), change(x)))
	next(t, first)
	next(t, third)
	require.NoError(t, isolated.flush(t.Context()))
	none(t, outside)
	// Missing optional metadata conservatively broadcasts; explicit empty does not.
	legacy := change(x)
	legacy.Changes[0].Routes = nil
	require.NoError(t, publisher.PublishConfirmed(t.Context(), legacy))
	next(t, first)
	next(t, second)
	next(t, third)
	legacy.Changes[0].Routes = []watch.Route{}
	require.NoError(t, publisher.PublishConfirmed(t.Context(), legacy))
	require.NoError(t, a.flush(t.Context()))
	none(t, first)
}

func TestRefcountsAndDistinctMutationsWithOverlappingNativeInterests(t *testing.T) {
	s := start(t, nil)
	b := broker(t, s, "app")
	publisher := broker(t, s, "app")
	route := watch.RouteKey("app", "topic")
	scope := watch.Interest{Routes: []watch.Route{route}}
	baseline := s.NumSubscriptions()
	a := opened(t, b, scope)
	count := s.NumSubscriptions()
	require.Positive(t, count)
	c := opened(t, b, scope)
	require.Equal(t, count, s.NumSubscriptions())
	broad := opened(t, b, watch.Interest{})
	value := change(route)
	value.Changes[0].Routes = append(value.Changes[0].Routes, watch.RouteKey("app", "second"))
	for range 2 {
		require.NoError(t, publisher.PublishConfirmed(t.Context(), value))
		next(t, a)
		next(t, c)
		next(t, broad)
	}
	require.NoError(t, b.flush(t.Context()))
	none(t, a)
	none(t, c)
	none(t, broad)
	broad.Close()
	a.Close()
	require.NoError(t, b.flush(t.Context()))
	require.Equal(t, count, s.NumSubscriptions())
	c.Close()
	require.NoError(t, b.flush(t.Context()))
	require.Equal(t, baseline, s.NumSubscriptions())
	reopened := opened(t, b, scope)
	require.NoError(t, publisher.PublishConfirmed(t.Context(), value))
	next(t, reopened)
}

func TestServiceAndKeysDoNotUseTheCallersTenant(t *testing.T) {
	s := start(t, nil)
	b := broker(t, s, "app")
	p := broker(t, s, "app")
	v := change(watch.RouteKey("app", "topic"))
	v.Tenant = pdid.New(1)
	sub := opened(t, b, watch.Interest{Service: "/app.JobService/", Keys: []pdid.Id{v.Changes[0].Key}})
	require.NoError(t, p.PublishConfirmed(t.Context(), v))
	next(t, sub)
	v.Changes[0].By = "/app.RobotService/Patch"
	require.NoError(t, p.PublishConfirmed(t.Context(), v))
	require.NoError(t, b.flush(t.Context()))
	none(t, sub)
	v.Changes[0].By = ""
	require.NoError(t, p.PublishConfirmed(t.Context(), v))
	next(t, sub)
}

func TestLargeMutationIsReassembledAndKeepsItsEnvelope(t *testing.T) {
	s := start(t, nil)
	b := broker(t, s, "app")
	p := broker(t, s, "app")
	sub := opened(t, b, watch.Interest{})
	key := pdid.New(3)
	id := pdid.New(4)
	image := pdpb.WatchEvent_builder{Id: key[:], Method: strings.Repeat("value", 300000)}.Build()
	envelope, err := resource.Event(id[:], time.Now(), "/app.JobService/Patch", "/app.JobService/Run", pdpb.ChangeKind_CHANGE_KIND_UPDATED, nil, image)
	require.NoError(t, err)
	v := watch.Event{Changes: []watch.Change{{Key: key, By: "/app.JobService/Patch", Resource: envelope, Patch: []byte("secret")}}}
	require.NoError(t, p.PublishConfirmed(t.Context(), v))
	got := next(t, sub)
	require.True(t, proto.Equal(envelope, got.Changes[0].Resource))
	require.Empty(t, got.Changes[0].Patch)
}

func TestDisconnectCutsOldSubscriptionsAndReconnectInstallsFreshOnes(t *testing.T) {
	s := start(t, nil)
	url := s.ClientURL()
	port := s.Addr().(*net.TCPAddr).Port
	b := broker(t, s, "app")
	p := broker(t, s, "app")
	old := opened(t, b, watch.Interest{})
	s.Shutdown()
	s.WaitForShutdown()
	select {
	case _, ok := <-old.Events:
		require.False(t, ok)
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect left the old stream alive")
	}
	require.Error(t, p.PublishConfirmed(t.Context(), change("topic")))
	s2 := start(t, &server.Options{Host: "127.0.0.1", Port: port})
	require.Equal(t, url, s2.ClientURL())
	require.Eventually(t, func() bool { return b.conn.IsConnected() && p.conn.IsConnected() }, 5*time.Second, 10*time.Millisecond)
	fresh := opened(t, b, watch.Interest{})
	// Closing the previous epoch cannot remove the new epoch's interests.
	old.Close()
	require.NoError(t, p.PublishConfirmed(t.Context(), change("topic")))
	next(t, fresh)
}

func TestRegistrationFailureNeverReportsReady(t *testing.T) {
	s := start(t, &server.Options{Host: "127.0.0.1", Port: -1, Users: []*server.User{{Username: "reader", Password: "password", Permissions: &server.Permissions{Subscribe: &server.SubjectPermission{Deny: []string{">"}}}}}})
	b, err := New(t.Context(), s.ClientURL(), "app", nats.UserInfo("reader", "password"))
	require.NoError(t, err)
	defer b.Close()
	_, err = b.Open(t.Context(), watch.Interest{})
	require.Error(t, err)
	b.mu.Lock()
	require.Nil(t, b.current)
	b.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = b.Open(ctx, watch.Interest{})
	require.Equal(t, codes.Canceled, status.Code(err))
}

func TestPublicationDenialIsNotAnOutboxAcknowledgement(t *testing.T) {
	s := start(t, &server.Options{Host: "127.0.0.1", Port: -1, Users: []*server.User{{Username: "reader", Password: "password", Permissions: &server.Permissions{Publish: &server.SubjectPermission{Deny: []string{">"}}}}}})
	b, err := New(t.Context(), s.ClientURL(), "app", nats.UserInfo("reader", "password"))
	require.NoError(t, err)
	defer b.Close()
	require.Error(t, b.PublishConfirmed(t.Context(), change("topic")))
}

func TestFragmentLossAndMalformedInputAreObservable(t *testing.T) {
	s := start(t, nil)
	b := broker(t, s, "app")
	v := change("topic")
	v.Changes[0].Signal = []byte(strings.Repeat("x", chunkSize*3))
	ps, _, err := b.encode(v)
	require.NoError(t, err)
	require.Greater(t, len(ps[0].frames), 1)
	var r receiver
	event, err := r.accept(ps[0].frames[0])
	require.NoError(t, err)
	require.Nil(t, event)
	require.True(t, r.expired(time.Now().Add(fragmentTimeout)))
	for i := len(ps[0].frames) - 1; i >= 0; i-- {
		event, err = r.accept(ps[0].frames[i])
		require.NoError(t, err)
		if event != nil {
			require.Equal(t, v.Changes[0].Signal, event.Changes[0].Signal)
		}
	}
	require.Zero(t, r.size)
	require.Empty(t, r.pending)
	_, err = r.accept([]byte("garbage"))
	require.Error(t, err)
	frame := append([]byte{}, ps[0].frames[0]...)
	binary.BigEndian.PutUint32(frame[20:24], ^uint32(0))
	_, err = r.accept(frame)
	require.Error(t, err)
	// Corrupt transport data closes the actual stream, rather than just logging.
	sub := opened(t, b, watch.Interest{})
	require.NoError(t, b.conn.Publish(b.subjects(v.Changes[0])[0], []byte("garbage")))
	select {
	case _, ok := <-sub.Events:
		require.False(t, ok)
	case <-time.After(3 * time.Second):
		t.Fatal("malformed message left stream alive")
	}
}

func TestSlowLocalReaderDoesNotCutFastReader(t *testing.T) {
	s := start(t, nil)
	b := broker(t, s, "app")
	p := broker(t, s, "app")
	slow := opened(t, b, watch.Interest{})
	fast := opened(t, b, watch.Interest{})
	for range watch.Backlog + 1 {
		require.NoError(t, p.PublishConfirmed(t.Context(), change("topic")))
		next(t, fast)
	}
	for range slow.Events {
	}
	require.NoError(t, p.PublishConfirmed(t.Context(), change("topic")))
	next(t, fast)
}

// Hold one real server PONG to exercise the registration barrier without
// assuming that a fast local connection happens to race a publication.
type heldPong struct {
	net.Conn
	hold    atomic.Bool
	entered chan struct{}
	resume  chan struct{}
}

func (c *heldPong) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if bytes.Contains(p[:n], []byte("PONG\r\n")) && c.hold.CompareAndSwap(true, false) {
		close(c.entered)
		<-c.resume
	}
	return n, err
}

type pongDialer struct{ conn *heldPong }

func (d *pongDialer) Dial(network, address string) (net.Conn, error) {
	c, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	d.conn = &heldPong{Conn: c, entered: make(chan struct{}), resume: make(chan struct{})}
	return d.conn, nil
}
func TestPendingReadinessDoesNotBlockPublishOrSurviveAGap(t *testing.T) {
	s := start(t, nil)
	dial := &pongDialer{}
	b, err := New(t.Context(), s.ClientURL(), "app", nats.SetCustomDialer(dial))
	require.NoError(t, err)
	resume := sync.OnceFunc(func() { close(dial.conn.resume) })
	t.Cleanup(func() { resume(); _ = b.Close() })
	dial.conn.hold.Store(true)
	result := make(chan error, 1)
	go func() {
		sub, err := b.Open(t.Context(), watch.Interest{})
		if sub != nil {
			sub.Close()
		}
		result <- err
	}()
	select {
	case <-dial.conn.entered:
	case <-time.After(time.Second):
		t.Fatal("registration did not reach its flush")
	}
	published := make(chan struct{})
	go func() { b.Publish(t.Context(), change("topic")); b.fail(nil); close(published) }()
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("pending Open blocked publication or disconnect handling")
	}
	require.Eventually(t, func() bool { return b.conn.Stats().OutMsgs > 0 }, time.Second, time.Millisecond)
	deadline, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	// Another confirmed publish must respect its own cancellation while the
	// first sender is waiting for the held PONG.
	require.ErrorIs(t, b.PublishConfirmed(deadline, change("topic")), context.DeadlineExceeded)
	resume()
	select {
	case err := <-result:
		require.Equal(t, codes.ResourceExhausted, status.Code(err))
	case <-time.After(time.Second):
		t.Fatal("registration did not stop after its epoch was lost")
	}
}
