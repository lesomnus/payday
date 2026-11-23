package watch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdid"
)

// openedBroker is the memory broker that also remembers the interest each
// subscription was opened with, which is what a native subscription is made of
// and the only way to see the routing a stream asked for.
type openedBroker struct {
	hub Fanout

	mu     sync.Mutex
	opened []Interest
	ready  chan struct{}
}

func newOpenedBroker() *openedBroker { return &openedBroker{ready: make(chan struct{})} }

func (b *openedBroker) Publish(_ context.Context, v Event) { b.hub.Publish(v) }
func (b *openedBroker) Subscribe() (<-chan Event, func()) {
	s, _ := b.hub.Open(context.Background(), Interest{})
	return s.Events, s.Close
}
func (b *openedBroker) Open(ctx context.Context, interest Interest) (*Subscription, error) {
	s, err := b.hub.Open(ctx, interest)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.opened = append(b.opened, interest.clone())
	b.mu.Unlock()
	close(b.ready)
	return s, nil
}

// streamOf opens a Stream on `prefix` and answers with what each call of its
// `send` was given. It returns once the subscription is installed, so what the
// test publishes next is what the stream is told of.
func streamOf(t *testing.T, prefix string) (*openedBroker, <-chan map[pdid.Id]string) {
	t.Helper()
	// Only so that a stream that hears nothing fails this test rather than
	// the whole run; nothing below is timed against it.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	b := newOpenedBroker()
	sends := make(chan map[pdid.Id]string)
	done := make(chan error, 1)
	go func() {
		done <- Stream(ctx, New(b), prefix, nil, func(ks map[pdid.Id]string, _ Seen) error {
			select {
			case sends <- ks:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-b.ready:
	case err := <-done:
		t.Fatalf("the stream ended before it subscribed: %v", err)
	}

	out := make(chan map[pdid.Id]string)
	go func() {
		defer close(out)
		for {
			select {
			case ks := <-sends:
				out <- ks
			case <-done:
				return
			}
		}
	}()
	return b, out
}

func wrote(by string, key pdid.Id) Event {
	return Event{Method: by, Changes: []Change{{Method: by, By: by, Key: key}}}
}

func heard(t *testing.T, sends <-chan map[pdid.Id]string, want map[pdid.Id]string) {
	t.Helper()
	ks, ok := <-sends
	require.True(t, ok, "the stream ended without being sent %v", want)
	require.Equal(t, want, ks)
}

func TestStreamOnAPrefixServicesShareHearsEachOfThem(t *testing.T) {
	b, sends := streamOf(t, "/app.")

	job, robot := pdid.New(3), pdid.New(3)
	b.Publish(t.Context(), wrote("/app.JobService/Patch", job))
	heard(t, sends, map[pdid.Id]string{job: "/app.JobService/Patch"})
	b.Publish(t.Context(), wrote("/app.RobotService/Add", robot))
	heard(t, sends, map[pdid.Id]string{robot: "/app.RobotService/Add"})

	require.Equal(t, []Interest{{}}, b.opened, "no one service is this stream's")
}

func TestStreamOnOneServiceIsRoutedToItAndHearsOnlyIt(t *testing.T) {
	b, sends := streamOf(t, "/app.JobService/")
	require.Equal(t, []Interest{{Service: "/app.JobService/"}}, b.opened)

	job, robot := pdid.New(3), pdid.New(3)
	// Another service's first, so that being sent only the job's says the
	// robot's was not let through.
	b.Publish(t.Context(), wrote("/app.RobotService/Patch", robot))
	b.Publish(t.Context(), wrote("/app.JobService/Patch", job))
	heard(t, sends, map[pdid.Id]string{job: "/app.JobService/Patch"})
}

func TestStreamOnAnyOtherPrefixHearsWhatItCovers(t *testing.T) {
	t.Run("a service's name without its closing slash", func(t *testing.T) {
		b, sends := streamOf(t, "/app.JobService")

		job, other := pdid.New(3), pdid.New(3)
		b.Publish(t.Context(), wrote("/app.RobotService/Patch", pdid.New(3)))
		b.Publish(t.Context(), wrote("/app.JobService/Patch", job))
		heard(t, sends, map[pdid.Id]string{job: "/app.JobService/Patch"})
		// A prefix and not a service, as it always was.
		b.Publish(t.Context(), wrote("/app.JobServiceTwo/Patch", other))
		heard(t, sends, map[pdid.Id]string{other: "/app.JobServiceTwo/Patch"})

		require.Equal(t, []Interest{{}}, b.opened)
	})
	t.Run("empty", func(t *testing.T) {
		b, sends := streamOf(t, "")

		job, robot := pdid.New(3), pdid.New(3)
		b.Publish(t.Context(), wrote("/app.JobService/Patch", job))
		heard(t, sends, map[pdid.Id]string{job: "/app.JobService/Patch"})
		b.Publish(t.Context(), wrote("/other.RobotService/Add", robot))
		heard(t, sends, map[pdid.Id]string{robot: "/other.RobotService/Add"})

		require.Equal(t, []Interest{{}}, b.opened)
	})
}
