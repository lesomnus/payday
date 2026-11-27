package watch_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/watch"
)

// ended is whether `c` is closed with nothing left on it, waiting a moment for
// it to be.
func ended(t *testing.T, c <-chan watch.Event) bool {
	t.Helper()

	select {
	case v, ok := <-c:
		if ok {
			t.Fatalf("an event arrived on a subscription that should have ended: %s", v.Method)
		}

		return true

	case <-time.After(time.Second):
		return false
	}
}

func changed() watch.Event {
	return watch.Event{
		Method:  "/app.RobotService/Add",
		Changes: []watch.Change{{By: "/app.RobotService/Add", Key: pdid.New(200)}},
	}
}

// TestClosingAWatchEndsItsMemoryBrokersSubscriptions: the memory broker is a
// scoped one, so a subscription opened through a Watch is the broker's own and
// not one the Watch shares out; closing the Watch has to reach the broker for
// it to end. A server built twice in one process -- a test suite -- otherwise
// keeps every stream of the first open on a broker nothing publishes to.
func TestClosingAWatchEndsItsMemoryBrokersSubscriptions(t *testing.T) {
	x := require.New(t)

	w := watch.New(watch.Memory())

	scoped, err := w.Open(t.Context(), watch.Interest{})
	x.NoError(err)
	broadcast, stop := w.Subscribe()
	defer stop()
	defer scoped.Close()

	x.NoError(w.Close())

	x.True(ended(t, scoped.Events), "a scoped subscription outlived its Watch")
	x.True(ended(t, broadcast), "a subscription outlived its Watch")

	// Twice is nothing.
	x.NoError(w.Close())
}

// TestAClosedMemoryBrokerRefusesWhatComesAfter: a subscriber after the close is
// told what a subscriber that fell behind is told, and a publication reaches
// nobody -- not a subscription that was opened before.
func TestAClosedMemoryBrokerRefusesWhatComesAfter(t *testing.T) {
	x := require.New(t)

	w := watch.New(watch.Memory())
	x.NoError(w.Close())

	_, err := w.Open(t.Context(), watch.Interest{})
	x.ErrorIs(err, watch.ErrBehind)

	late, stop := w.Subscribe()
	defer stop()
	x.True(ended(t, late), "a subscription made after the close was opened")

	w.Broker().Publish(t.Context(), changed())
	x.True(ended(t, late))
}
