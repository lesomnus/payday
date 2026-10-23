package resource

import (
	"context"
	"errors"
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
	"google.golang.org/protobuf/reflect/protoreflect"
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

// refused is a watch that is expected to end before it is subscribed: a
// refusal that regressed ends it on the deadline, as an error the test reads,
// rather than leaving it open for the run's timeout to find.
func refused(t *testing.T, s *Server, filter string) error {
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
func received(t *testing.T, stream *testStream, done <-chan error) *pdpb.WatchEvent {
	t.Helper()
	select {
	case got := <-stream.sent:
		return got
	case err := <-done:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("authorized event did not arrive")
	}
	return nil
}

// A language for the tests of the server, which links none: CEL's own are in
// resourcecel, whose import would put it in this binary as well.
type compiler func(filter string) (Filter, error)

func (c compiler) Compile(filter string) (Filter, error) { return c(filter) }

type matcher func(context.Context, *pdpb.WatchEvent) (bool, error)

func (m matcher) Match(ctx context.Context, event *pdpb.WatchEvent) (bool, error) {
	return m(ctx, event)
}

func filtered(m matcher) Filters {
	return compiler(func(string) (Filter, error) { return m, nil })
}

// TestAServerWithNoLanguageRefusesAFilter, which is what an app's sandbox is:
// nothing in this binary links a language, as nothing in a page does.
func TestAServerWithNoLanguageRefusesAFilter(t *testing.T) {
	s, w, event := testServer(t)
	require.Nil(t, s.filters)

	// Refused before anything is subscribed, rather than served as though the
	// filter were not there.
	err := refused(t, s, "true")
	require.Equal(t, codes.Unimplemented, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "resource/resourcecel")

	// And no filter needs no language.
	stream := newStream(t)
	done := start(t, s, "", stream)
	publish(w, event)
	require.True(t, proto.Equal(event, received(t, stream, done)))
}

// TestATooLargeFilterIsRefusedWhateverItIsWrittenIn: the bound is on what a
// caller may send, and no language is asked to read it.
func TestATooLargeFilterIsRefusedWhateverItIsWrittenIn(t *testing.T) {
	s, _, _ := testServer(t)
	asked := false
	s.filters = compiler(func(string) (Filter, error) { asked = true; return nil, errors.New("asked") })
	err := refused(t, s, strings.Repeat(" ", MaxExpression+1))
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.False(t, asked)
}

// TestAFilterIsAskedOnlyOfWhatMayBeSent: an image the subscriber may not read
// never reaches the filter, and what does is the projected image, not the one
// the write recorded.
func TestAFilterIsAskedOnlyOfWhatMayBeSent(t *testing.T) {
	s, w, event := testServer(t)
	s.project = func(_ context.Context, v proto.Message) (proto.Message, error) {
		if v.(*pdpb.WatchEvent).GetMethod() == "hidden" {
			return nil, nil
		}
		v.(*pdpb.WatchEvent).SetCallMethod("projected")
		return v, nil
	}
	// Appended on the watch's goroutine, and read once the event it sends has
	// arrived, which is after.
	var asked []string
	s.filters = filtered(func(_ context.Context, e *pdpb.WatchEvent) (bool, error) {
		image := &pdpb.WatchEvent{}
		if err := e.GetValue().UnmarshalTo(image); err != nil {
			return false, err
		}
		asked = append(asked, image.GetMethod()+"/"+image.GetCallMethod())
		return image.GetMethod() == "public", nil
	})

	id := pdid.New(1)
	hidden, err := Event(id[:], time.Now(), "/app/Add", "/app/Add", pdpb.ChangeKind_CHANGE_KIND_CREATED, nil, pdpb.WatchEvent_builder{Id: id[:], Method: "hidden"}.Build())
	require.NoError(t, err)
	refused, err := Event(id[:], time.Now(), "/app/Add", "/app/Add", pdpb.ChangeKind_CHANGE_KIND_CREATED, nil, pdpb.WatchEvent_builder{Id: id[:], Method: "refused"}.Build())
	require.NoError(t, err)
	before := proto.Clone(event)

	stream := newStream(t)
	done := start(t, s, "anything", stream)
	publish(w, hidden)
	publish(w, refused)
	publish(w, event)

	got := received(t, stream, done)
	image := &pdpb.WatchEvent{}
	require.NoError(t, got.GetValue().UnmarshalTo(image))
	require.Equal(t, "projected", image.GetCallMethod())
	require.Equal(t, []string{"refused/projected", "public/projected"}, asked)
	require.True(t, proto.Equal(before, event), "projection changed the shared event")
}

// TestWhatAFilterFailsWithIsWhatTheSubscriberIsTold, when it is a status, and
// nothing of it when it is not: the language did not shape it to be read.
func TestWhatAFilterFailsWithIsWhatTheSubscriberIsTold(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code codes.Code
	}{
		{"status", status.Error(codes.ResourceExhausted, "over budget"), codes.ResourceExhausted},
		{"not a status", errors.New("private-error-sentinel"), codes.Internal},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, w, event := testServer(t)
			s.filters = filtered(func(context.Context, *pdpb.WatchEvent) (bool, error) { return false, test.err })
			stream := newStream(t)
			done := start(t, s, "anything", stream)
			publish(w, event)
			err := result(t, done)
			require.Equal(t, test.code, status.Code(err))
			require.NotContains(t, err.Error(), "private-error-sentinel")
		})
	}
}

// TestAFilterCompiledToNothingIsNotNoFilter: nil is how the server says "every
// event", and a language that answered it for a filter would have the stream
// served as though none were sent.
func TestAFilterCompiledToNothingIsNotNoFilter(t *testing.T) {
	s, _, _ := testServer(t)
	s.filters = compiler(func(string) (Filter, error) { return nil, nil })
	err := refused(t, s, "anything")
	require.Equal(t, codes.Internal, status.Code(err))
}

// TestALanguageIsRegisteredOnce, and a server made after it compiles in it.
func TestALanguageIsRegisteredOnce(t *testing.T) {
	require.Nil(t, registered())
	t.Cleanup(func() { language.l = nil })

	var told []protoreflect.FullName
	RegisterLanguage(func(types []protoreflect.MessageDescriptor) (Filters, error) {
		for _, d := range types {
			told = append(told, d.FullName())
		}
		return filtered(func(context.Context, *pdpb.WatchEvent) (bool, error) { return true, nil }), nil
	})
	s, err := New(&pdpb.UnimplementedBatchServiceServer{}, nil, func(_ context.Context, v proto.Message) (proto.Message, error) { return v, nil }, &pdpb.BatchRequest{})
	require.NoError(t, err)
	require.NotNil(t, s.filters)
	require.Equal(t, []protoreflect.FullName{"payday.WatchEvent", "payday.BatchRequest"}, told)

	require.PanicsWithValue(t, "resource: a filter language is registered already", func() {
		RegisterLanguage(func([]protoreflect.MessageDescriptor) (Filters, error) { return nil, nil })
	})
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
