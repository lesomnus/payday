package resource

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/watch"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type testStream struct {
	grpc.ServerStream
	ctx     context.Context
	ready   chan struct{}
	sent    chan *pdpb.WatchEvent
	block   chan struct{}
	entered chan struct{}
}

func (s *testStream) Context() context.Context     { return s.ctx }
func (s *testStream) SendHeader(metadata.MD) error { close(s.ready); return nil }
func (s *testStream) Send(v *pdpb.WatchEvent) error {
	if s.block != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-s.block:
		}
	}
	s.sent <- v
	return nil
}

func testServer(t *testing.T) (*Server, *watch.Watch, *pdpb.WatchEvent) {
	t.Helper()
	w := watch.New(watch.Memory())
	s, err := New(&pdpb.UnimplementedBatchServiceServer{}, w, func(_ context.Context, v proto.Message) (proto.Message, error) { return v, nil })
	require.NoError(t, err)
	id := pdid.New(1)
	v, err := Event(id[:], time.Now(), "/app/Add", "/app/Add", pdpb.ChangeKind_CHANGE_KIND_CREATED, nil, pdpb.WatchEvent_builder{Id: id[:], Method: "public"}.Build())
	require.NoError(t, err)
	return s, w, v
}
func start(t *testing.T, s *Server, filter string, stream *testStream) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.Watch(pdpb.WatchRequest_builder{Filter: filter}.Build(), stream) }()
	select {
	case <-stream.ready:
	case err := <-done:
		t.Fatalf("watch failed before ready: %v", err)
	case <-time.After(time.Second):
		t.Fatal("watch was not ready")
	}
	return done
}
func newStream(t *testing.T) *testStream {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	return &testStream{ctx: ctx, ready: make(chan struct{}), sent: make(chan *pdpb.WatchEvent, 256)}
}
func result(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not end")
		return nil
	}
}
func publish(w *watch.Watch, v *pdpb.WatchEvent) {
	w.Broker().Publish(context.Background(), watch.Event{Changes: []watch.Change{{Resource: v}}})
}

func TestInvalidFilters(t *testing.T) {
	s, _, _ := testServer(t)
	for _, filter := range []string{"42", "event.value", "event.", strings.Repeat(" ", MaxExpression+1), strings.Repeat("(", MaxDepth+1) + "true" + strings.Repeat(")", MaxDepth+1)} {
		_, err := s.compile(filter)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}
}

func TestRuntimeErrorsAndLimits(t *testing.T) {
	xs := "[" + strings.Repeat("0,", 49) + "0]"
	costly := xs + ".all(a," + xs + ".all(b," + xs + ".all(c," + xs + ".all(d,true))))"
	for _, test := range []struct {
		name, filter string
		code         codes.Code
	}{
		{"dynamic field", "event.value.missing == 1", codes.InvalidArgument},
		{"cost", costly, codes.ResourceExhausted},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, w, event := testServer(t)
			stream := newStream(t)
			done := start(t, s, test.filter, stream)
			publish(w, event)
			err := result(t, done)
			require.Equal(t, test.code, status.Code(err))
			require.NotContains(t, err.Error(), "public")
		})
	}
}

func TestDeniedImagesNeverReachCEL(t *testing.T) {
	s, w, event := testServer(t)
	s.project = func(_ context.Context, v proto.Message) (proto.Message, error) {
		if v.(*pdpb.WatchEvent).GetMethod() == "public" {
			return nil, nil
		}
		return v, nil
	}
	stream := newStream(t)
	done := start(t, s, `event.value.method == "visible" || event.value.missing == 1`, stream)
	publish(w, event)
	id := pdid.New(1)
	visible, err := Event(id[:], time.Now(), "/app/Add", "/app/Add", pdpb.ChangeKind_CHANGE_KIND_CREATED, nil, pdpb.WatchEvent_builder{Id: id[:], Method: "visible"}.Build())
	require.NoError(t, err)
	publish(w, visible)
	select {
	case got := <-stream.sent:
		require.True(t, proto.Equal(visible, got))
	case err := <-done:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("authorized event did not arrive")
	}
}

func TestBacklogIsAnError(t *testing.T) {
	s, w, event := testServer(t)
	stream := newStream(t)
	stream.block = make(chan struct{})
	stream.entered = make(chan struct{}, 1)
	done := start(t, s, "", stream)
	publish(w, event)
	<-stream.entered
	for range watch.Backlog + 1 {
		publish(w, event)
	}
	close(stream.block)
	require.Equal(t, codes.ResourceExhausted, status.Code(result(t, done)))
}

func TestDisabledBroker(t *testing.T) {
	s, _, _ := testServer(t)
	s.watch = watch.New(nil)
	err := s.Watch(&pdpb.WatchRequest{}, newStream(t))
	require.Equal(t, codes.Unavailable, status.Code(err))
}

func TestAnUpdateRequiresBothImages(t *testing.T) {
	s, _, created := testServer(t)
	old := &pdpb.WatchEvent{}
	require.NoError(t, created.GetValue().UnmarshalTo(old))
	next := proto.Clone(old).(*pdpb.WatchEvent)
	next.SetMethod("another-tenant")
	update, err := Event(created.GetId(), time.Now(), "/app/Move", "/app/Move", pdpb.ChangeKind_CHANGE_KIND_UPDATED, old, next)
	require.NoError(t, err)
	s.project = func(_ context.Context, v proto.Message) (proto.Message, error) {
		if v.(*pdpb.WatchEvent).GetMethod() == "another-tenant" {
			return nil, nil
		}
		return v, nil
	}
	visible, err := s.public(t.Context(), update)
	require.NoError(t, err)
	require.Nil(t, visible)
}
