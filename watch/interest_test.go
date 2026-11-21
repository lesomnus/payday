package watch

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/stretchr/testify/require"
)

func TestInterestIsConservativeAndDoesNotUseTheActorsTenant(t *testing.T) {
	key := pdid.New(3)
	route := RouteKey("app", "n:topic.*.>")
	scope := Interest{Service: "/app.JobService/", Keys: []pdid.Id{key}, Routes: []Route{route}}
	c := Change{By: "/app.JobService/Patch", Key: key, Routes: []Route{route}}
	require.True(t, scope.Matches(Event{Tenant: pdid.New(1), Changes: []Change{c}}))
	c.By = "/app.RobotService/Patch"
	require.False(t, scope.Match(c))
	c.By = ""
	c.Key = pdid.New(3)
	require.False(t, scope.Match(c))
	c.Key = pdid.Nil
	c.Routes = []Route{}
	require.False(t, scope.Match(c))
	c.Routes = nil
	require.True(t, scope.Match(c), "unknown metadata must overdeliver")
	require.NotEqual(t, RouteKey("a", "b", "c"), RouteKey("a", "b.c"))
	require.NotEqual(t, RouteKey("a", "r:ID"), RouteKey("a", "n:ID"))
	require.NotEqual(t, RouteKey("a", "n:ID"), RouteKey("b", "n:ID"))
}

func TestFanoutSharesInterpretationNotDistinctMutations(t *testing.T) {
	var hub Fanout
	keys := []pdid.Id{pdid.New(3)}
	a, err := hub.Open(t.Context(), Interest{Keys: keys})
	require.NoError(t, err)
	defer a.Close()
	b, err := hub.Open(t.Context(), Interest{Keys: keys})
	require.NoError(t, err)
	defer b.Close()
	key := keys[0]
	keys[0] = pdid.New(3) // Caller ownership cannot change a live scope.
	var calls atomic.Int32
	for range 2 {
		hub.Publish(Event{Changes: []Change{{Key: key}}})
		first := <-a.Events
		second := <-b.Events
		x := first.Shared("resolver", func() any { return calls.Add(1) })
		y := second.Shared("resolver", func() any { return calls.Add(1) })
		require.Equal(t, x, y)
	}
	require.EqualValues(t, 2, calls.Load())
	// One stalled stream does not take a responsive neighbour with it.
	for range Backlog + 1 {
		hub.Publish(Event{Changes: []Change{{Key: key}}})
		<-b.Events
	}
	for range a.Events {
	}
	hub.Publish(Event{Changes: []Change{{Key: key}}})
	_, ok := <-b.Events
	require.True(t, ok)
}

type countedBroadcast struct {
	hub    Fanout
	opened atomic.Int32
	closed atomic.Int32
}

func (b *countedBroadcast) Publish(_ context.Context, v Event) { b.hub.Publish(v) }
func (b *countedBroadcast) Subscribe() (<-chan Event, func()) {
	s, _ := b.hub.Open(context.Background(), Interest{})
	b.opened.Add(1)
	return s.Events, func() { b.closed.Add(1); s.Close() }
}
func TestLegacyTransportIsSharedAndReleased(t *testing.T) {
	broker := &countedBroadcast{}
	w := New(broker)
	a, err := w.Open(t.Context(), Interest{})
	require.NoError(t, err)
	b, err := w.Open(t.Context(), Interest{})
	require.NoError(t, err)
	require.EqualValues(t, 1, broker.opened.Load())
	a.Close()
	a.Close()
	require.Zero(t, broker.closed.Load())
	b.Close()
	require.Eventually(t, func() bool { return broker.closed.Load() == 1 }, time.Second, time.Millisecond)
	c, err := w.Open(t.Context(), Interest{})
	require.NoError(t, err)
	defer c.Close()
	require.EqualValues(t, 2, broker.opened.Load())
	broker.hub.Close()
	select {
	case _, ok := <-c.Events:
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("source gap did not cut listeners")
	}
}
