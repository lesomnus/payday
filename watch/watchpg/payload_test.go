package watchpg

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/resource"
	"github.com/lesomnus/payday/watch"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestResourcePayloadAcrossNotifications(t *testing.T) {
	x := require.New(t)
	id := pdid.New(3)
	image := pdpb.WatchEvent_builder{Id: id[:], Method: strings.Repeat("large-resource", 10000)}.Build()
	event, err := resource.Event(id[:], time.Now(), "/app.RobotService/Add", "/payday.ResourceService/Batch", pdpb.ChangeKind_CHANGE_KIND_CREATED, nil, image)
	x.NoError(err)
	input := watch.Event{Method: event.GetCallMethod(), Changes: []watch.Change{{By: "/app.RobotService/Add", Method: event.GetMethod(), Key: id, Resource: event}}}
	chunks, err := packets(input)
	x.NoError(err)
	x.Greater(len(chunks), 1)
	var r receiver
	var output *watch.Event
	// Reordering is tolerated, but the whole change is emitted only once complete.
	for i := len(chunks) - 1; i >= 0; i-- {
		x.Less(len(chunks[i]), Payload)
		v, err := r.accept(chunks[i])
		x.NoError(err)
		if i > 0 {
			x.Nil(v)
		} else {
			output = v
		}
	}
	x.NotNil(output)
	x.Equal(input.Method, output.Method)
	x.Equal(input.Changes[0].Method, output.Changes[0].Method)
	x.True(proto.Equal(event, output.Changes[0].Resource))
	x.Zero(r.size)
	x.Empty(r.pending)
}

func TestMalformedFragmentIsObservable(t *testing.T) {
	for _, data := range []string{`{"f":"x","i":-1,"n":2,"d":"YQ=="}`, `{"f":"x","n":999999999,"d":"YQ=="}`} {
		var r receiver
		_, err := r.accept(data)
		require.Error(t, err)
	}
}

func TestInvalidationSurvivesTheWireWithoutPublishingMutationPatches(t *testing.T) {
	x := require.New(t)
	signal := []byte(`{"topic":"n:maintenance"}`)
	input := watch.Event{Changes: []watch.Change{{
		By: "/payday.notify/internal/Invalidate", Key: pdid.New(1),
		Signal: signal, Patch: []byte("private mutation content"),
	}}}
	chunks, err := packets(input)
	x.NoError(err)
	x.Len(chunks, 1)
	x.NotContains(chunks[0], "private mutation content")
	output, err := decode(chunks[0])
	x.NoError(err)
	x.Equal(signal, output.Changes[0].Signal)
	x.Empty(output.Changes[0].Patch)
	x.Nil(output.Changes[0].Resource)
}

func TestInvalidationCountsTowardsThePublicationLimit(t *testing.T) {
	_, err := packets(watch.Event{Changes: []watch.Change{{
		Key: pdid.New(1), Signal: make([]byte, MaxWire),
	}}})
	require.ErrorContains(t, err, "payload limit")
}

func TestAppWideSignalsAllowAnAbsentResourceKey(t *testing.T) {
	x := require.New(t)
	signal := []byte(`{"topic":"n:maintenance"}`)
	chunks, err := packets(watch.Event{Changes: []watch.Change{{
		By: "/app/internal/Invalidate", Signal: signal,
	}}})
	x.NoError(err)
	x.Len(chunks, 1)
	output, err := decode(chunks[0])
	x.NoError(err)
	x.True(output.Changes[0].Key.IsZero())
	x.Equal(signal, output.Changes[0].Signal)

	for _, item := range []change{
		{Key: pdid.Nil.String()},
		{Key: pdid.Nil.String(), Signal: signal, Resource: []byte{8, 1}},
		{Key: "not-an-id", Signal: signal},
		{Key: "", Signal: signal},
	} {
		data, err := json.Marshal(wire{Changes: []change{item}})
		x.NoError(err)
		_, err = decode(string(data))
		x.ErrorContains(err, "key")
	}
}
