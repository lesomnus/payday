package resource

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestEventKindDoesNotDependOnOptionalPrevious(t *testing.T) {
	id := pdid.New(1)
	value := pdpb.WatchEvent_builder{Id: id[:], Method: "after"}.Build()
	old := proto.Clone(value).(*pdpb.WatchEvent)
	old.SetMethod("before")
	for _, tc := range []struct {
		name            string
		kind            pdpb.ChangeKind
		previous, value proto.Message
		valid           bool
	}{
		{"create", pdpb.ChangeKind_CHANGE_KIND_CREATED, nil, value, true},
		{"update without previous", pdpb.ChangeKind_CHANGE_KIND_UPDATED, nil, value, true},
		{"update with previous", pdpb.ChangeKind_CHANGE_KIND_UPDATED, old, value, true},
		{"delete", pdpb.ChangeKind_CHANGE_KIND_DELETED, old, nil, true},
		{"unknown", pdpb.ChangeKind_CHANGE_KIND_UNSPECIFIED, nil, value, false},
		{"create with previous", pdpb.ChangeKind_CHANGE_KIND_CREATED, old, value, false},
		{"update without value", pdpb.ChangeKind_CHANGE_KIND_UPDATED, old, nil, false},
		{"delete without previous", pdpb.ChangeKind_CHANGE_KIND_DELETED, nil, nil, false},
		{"delete with value", pdpb.ChangeKind_CHANGE_KIND_DELETED, old, value, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event, err := Event(id[:], time.Now(), "/app/operation", "/app/operation", tc.kind, tc.previous, tc.value)
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.kind, event.GetKind())
			require.Equal(t, tc.previous != nil, event.HasPrevious())
		})
	}
}

func TestUpdateAuthorizesOnlyIncludedImages(t *testing.T) {
	s, _, created := testServer(t)
	visible := &pdpb.WatchEvent{}
	require.NoError(t, created.GetValue().UnmarshalTo(visible))
	hidden := proto.Clone(visible).(*pdpb.WatchEvent)
	hidden.SetMethod("hidden")
	s.project = func(_ context.Context, v proto.Message) (proto.Message, error) {
		if v.(*pdpb.WatchEvent).GetMethod() == "hidden" {
			return nil, nil
		}
		return v, nil
	}
	for _, tc := range []struct {
		previous, value proto.Message
		allowed         bool
	}{
		{nil, visible, true}, {hidden, visible, false}, {visible, hidden, false}, {nil, hidden, false},
	} {
		event, err := Event(created.GetId(), time.Now(), "/app/Move", "/app/Move", pdpb.ChangeKind_CHANGE_KIND_UPDATED, tc.previous, tc.value)
		require.NoError(t, err)
		got, err := s.public(t.Context(), event)
		require.NoError(t, err)
		require.Equal(t, tc.allowed, got != nil)
	}
}
