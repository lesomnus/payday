package resource

import (
	"context"
	"strings"
	"testing"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"github.com/lesomnus/payday/pdpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestWireFilterMatchesNativeCEL(t *testing.T) {
	s, _, created := testServer(t)
	before := &pdpb.WatchEvent{}
	require.NoError(t, created.GetValue().UnmarshalTo(before))
	after := proto.Clone(before).(*pdpb.WatchEvent)
	after.SetMethod("after")
	nested, err := anypb.New(wrapperspb.String("nested"))
	require.NoError(t, err)
	after.SetValue(nested)
	event, err := Event(created.GetId(), time.Now(), "/app/Patch", "/batch", pdpb.ChangeKind_CHANGE_KIND_UPDATED, before, after)
	require.NoError(t, err)
	native, err := cel.NewEnv(cel.Types(&pdpb.WatchEvent{}), cel.Variable("event", cel.ObjectType("payday.WatchEvent")))
	require.NoError(t, err)
	for _, expr := range []string{
		`has(event.value) && type(event.value) == payday.WatchEvent && event.value.method == "after"`,
		`has(event.previous) && event.previous.method == "public" && event.previous.id == event.value.id`,
		`event.previous != event.value && event.value == event.value && event == event`,
		`event.value.value == "nested" && event.date_created.getFullYear() > 2020`,
		`event.previous.method == "after"`,
		`event.value.missing == 1`,
	} {
		t.Run(expr, func(t *testing.T) {
			ast, issues := native.Compile(expr)
			require.NoError(t, issues.Err())
			old, err := native.Program(ast)
			require.NoError(t, err)
			wire, err := s.compile(expr)
			require.NoError(t, err)
			vars := map[string]any{"event": event}
			want, _, nativeErr := old.Eval(vars)
			got, _, wireErr := wire.Eval(vars)
			require.Equal(t, nativeErr != nil, wireErr != nil)
			if nativeErr == nil {
				require.Equal(t, types.True, got.Equal(want))
			}
		})
	}
}

func TestWireFilterLeavesUnrequestedPayloadUnread(t *testing.T) {
	s, _, event := testServer(t)
	event.GetValue().Value = []byte{0x80}
	p, err := s.compile(`type(event.value) == payday.WatchEvent`)
	require.NoError(t, err)
	v, _, err := p.Eval(map[string]any{"event": event})
	require.NoError(t, err)
	require.Equal(t, types.True, v)
	p, err = s.compile(`has(event.value.method)`)
	require.NoError(t, err)
	_, _, err = p.Eval(map[string]any{"event": event})
	require.Error(t, err)
	// The serving path still decodes and authorizes first. A metadata-only filter
	// must not turn a corrupt stored image into a delivered event.
	_, err = s.public(t.Context(), event)
	require.Equal(t, codes.Internal, status.Code(err))
}

func TestWireFilterUsesProjectedImage(t *testing.T) {
	s, w, event := testServer(t)
	before := proto.Clone(event)
	s.project = func(_ context.Context, v proto.Message) (proto.Message, error) {
		v.(*pdpb.WatchEvent).SetMethod("redacted")
		return v, nil
	}
	stream := newStream(t)
	done := start(t, s, `event.value.method == "redacted"`, stream)
	publish(w, event)
	select {
	case got := <-stream.sent:
		image := &pdpb.WatchEvent{}
		require.NoError(t, got.GetValue().UnmarshalTo(image))
		require.Equal(t, "redacted", image.GetMethod())
		require.True(t, proto.Equal(before, event), "projection changed the shared event")
	case err := <-done:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("filter did not observe the projected image")
	}
}

func TestWireFilterDeepAnyErrorIsSanitized(t *testing.T) {
	s, w, event := testServer(t)
	var nested proto.Message = wrapperspb.String("private-error-sentinel")
	for range 103 {
		var err error
		nested, err = anypb.New(nested)
		require.NoError(t, err)
	}
	image := &pdpb.WatchEvent{}
	require.NoError(t, event.GetValue().UnmarshalTo(image))
	image.SetValue(nested.(*anypb.Any))
	packed, err := anypb.New(image)
	require.NoError(t, err)
	event.SetValue(packed)
	stream := newStream(t)
	done := start(t, s, `event.value.value == "private-error-sentinel"`, stream)
	publish(w, event)
	err = result(t, done)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.False(t, strings.Contains(err.Error(), "private-error-sentinel"))
}
