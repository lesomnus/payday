package resourcecel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/resource"
	"github.com/lesomnus/payday/watch"
)

type testStream struct {
	grpc.ServerStream
	ctx   context.Context
	ready chan struct{}
	sent  chan *pdpb.WatchEvent
}

func (s *testStream) Context() context.Context      { return s.ctx }
func (s *testStream) SendHeader(metadata.MD) error  { close(s.ready); return nil }
func (s *testStream) Send(v *pdpb.WatchEvent) error { s.sent <- v; return nil }

func identity(_ context.Context, v proto.Message) (proto.Message, error) { return v, nil }

// testServer is a server as an app builds one, in a binary that imported this
// package: the filters it compiles are CEL because of that import and nothing
// else.
func testServer(t testing.TB, project resource.Project) (*resource.Server, *watch.Watch, *pdpb.WatchEvent) {
	t.Helper()
	w := watch.New(watch.Memory())
	s, err := resource.New(&pdpb.UnimplementedBatchServiceServer{}, w, project)
	require.NoError(t, err)
	return s, w, created(t, "public")
}
func created(t testing.TB, method string) *pdpb.WatchEvent {
	t.Helper()
	id := pdid.New(1)
	v, err := resource.Event(id[:], time.Now(), "/app/Add", "/app/Add", pdpb.ChangeKind_CHANGE_KIND_CREATED, nil, pdpb.WatchEvent_builder{Id: id[:], Method: method}.Build())
	require.NoError(t, err)
	return v
}

// compile is a filter as the server compiles it, over the event alone.
func compile(t testing.TB, filter string) program {
	t.Helper()
	f, err := language([]protoreflect.MessageDescriptor{(&pdpb.WatchEvent{}).ProtoReflect().Descriptor()})
	require.NoError(t, err)
	p, err := f.Compile(filter)
	require.NoError(t, err)
	return p.(program)
}
func start(t *testing.T, s *resource.Server, filter string, stream *testStream) <-chan error {
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

// refused is a watch that is expected to end before it is subscribed: a
// refusal that regressed ends it on the deadline, as an error the test reads,
// rather than leaving it open for the run's timeout to find.
func refused(t *testing.T, s *resource.Server, filter string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	t.Cleanup(cancel)
	return s.Watch(pdpb.WatchRequest_builder{Filter: filter}.Build(), &testStream{ctx: ctx, ready: make(chan struct{}), sent: make(chan *pdpb.WatchEvent, 256)})
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
	s, _, _ := testServer(t, identity)
	for _, filter := range []string{"42", "event.value", "event.", strings.Repeat(" ", resource.MaxExpression+1), strings.Repeat("(", MaxDepth+1) + "true" + strings.Repeat(")", MaxDepth+1)} {
		err := refused(t, s, filter)
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
			s, w, event := testServer(t, identity)
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
	s, w, event := testServer(t, func(_ context.Context, v proto.Message) (proto.Message, error) {
		if v.(*pdpb.WatchEvent).GetMethod() == "public" {
			return nil, nil
		}
		return v, nil
	})
	stream := newStream(t)
	done := start(t, s, `event.value.method == "visible" || event.value.missing == 1`, stream)
	publish(w, event)
	visible := created(t, "visible")
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
