package spin_test

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/spin"
)

// seq is a stack, as [spin.Run] takes one: the sequence a generated `Iter`
// answers with.
func seq(vs ...any) func(func(any) bool) {
	return slices.Values(vs)
}

// TestALayerWithNothingToRunWritesNothing is why this is not a method on the
// server interface.
//
// Most layers have no background work. Asked as an interface, they say so by
// not answering it -- rather than by every one of them carrying an empty method
// that means nothing, which is the shape that hides the ones that mean
// something.
func TestALayerWithNothingToRunWritesNothing(t *testing.T) {
	x := require.New(t)

	var ran atomic.Bool
	err := spin.Run(t.Context(), seq(
		struct{}{},
		"not a spinner either",
		spin.Func(func(ctx context.Context) error {
			ran.Store(true)
			return nil
		}),
	))
	x.NoError(err)
	x.True(ran.Load())
}

// TestNothingToSpinIsNotAFailure, and it does not block either.
func TestNothingToSpinIsNotAFailure(t *testing.T) {
	x := require.New(t)

	done := make(chan error, 1)
	go func() { done <- spin.Run(t.Context(), seq(struct{}{}, 42)) }()

	select {
	case err := <-done:
		x.NoError(err)
	case <-time.After(3 * time.Second):
		t.Fatal("a stack with no loops in it did not return")
	}
}

// TestTheyRunTogether: a stack of loops is a stack of loops, not a queue.
func TestTheyRunTogether(t *testing.T) {
	x := require.New(t)

	var n atomic.Int32
	both := make(chan struct{})

	f := spin.Func(func(ctx context.Context) error {
		if n.Add(1) == 2 {
			close(both)
		}
		<-ctx.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- spin.Run(ctx, seq(f, f)) }()

	select {
	case <-both:
	case <-time.After(3 * time.Second):
		t.Fatal("the second loop did not start until the first had finished")
	}

	cancel()
	x.NoError(<-done)
}

// TestGivingUpTakesTheProcessDown is the decision this package is built around.
//
// A sweep that stopped quietly is found days later, by which time the table it
// was keeping small is why something else fell over. So the failure comes back
// out of Run, and whatever called it ends the process.
func TestGivingUpTakesTheProcessDown(t *testing.T) {
	x := require.New(t)

	err := spin.Run(t.Context(), seq(spin.Func(func(ctx context.Context) error {
		return errors.New("the schema moved")
	})))
	x.ErrorContains(err, "the schema moved")
}

// TestTheOthersAreStoppedAndWaitedFor is the half of that which is not about
// the error.
//
// A caller returning from main must not be racing a loop that still holds a
// connection, so Run cancels the rest and does not answer until they have all
// come back.
func TestTheOthersAreStoppedAndWaitedFor(t *testing.T) {
	x := require.New(t)

	var stopped atomic.Bool
	other := spin.Func(func(ctx context.Context) error {
		<-ctx.Done()
		// Long enough that an implementation which did not wait would answer
		// first, and short enough not to be a delay anybody notices.
		time.Sleep(50 * time.Millisecond)
		stopped.Store(true)
		return nil
	})

	err := spin.Run(t.Context(), seq(
		other,
		spin.Func(func(ctx context.Context) error { return errors.New("gave up") }),
	))
	x.Error(err)
	x.True(stopped.Load(), "Run answered while a loop was still running")
}

// TestTheFailureSaysWhichLoop, because an errgroup answers with the error and
// nothing else, and "connection refused" alone does not say where to look.
func TestTheFailureSaysWhichLoop(t *testing.T) {
	x := require.New(t)

	err := spin.Run(t.Context(), seq(named{"outbox", errors.New("connection refused")}))
	x.ErrorContains(err, "outbox")
	x.ErrorContains(err, "connection refused")
}

type named struct {
	name string
	err  error
}

func (v named) Spin(ctx context.Context) error { return v.err }
func (v named) SpinName() string               { return v.name }

// TestShuttingDownIsNotAFailure is the case that would otherwise make every
// clean stop end in an error.
//
// A loop caught mid-pass answers with the context's error, and that is the
// shutdown that was asked for.
func TestShuttingDownIsNotAFailure(t *testing.T) {
	x := require.New(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := spin.Run(ctx, seq(spin.Func(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})))
	x.NoError(err)
}

// TestCanceledOutOfNowhereIsStillAFailure is why that check is against the
// context rather than against [context.Canceled] alone.
//
// A loop that answers Canceled while nothing has been canceled is one carrying
// the wrong context, which is a bug and not a shutdown. `ctx.Err()` is nil
// until something cancels, and nothing is `errors.Is` a nil target, so this
// falls through to the loud case on its own.
func TestCanceledOutOfNowhereIsStillAFailure(t *testing.T) {
	x := require.New(t)

	err := spin.Run(t.Context(), seq(spin.Func(func(ctx context.Context) error {
		return context.Canceled
	})))
	x.ErrorIs(err, context.Canceled)
}

// TestEveryRunsAtOnceAndThenWaits.
//
// A loop whose first pass is a whole interval away does nothing at all in a
// deployment that restarts more often than the interval.
func TestEveryRunsAtOnceAndThenWaits(t *testing.T) {
	x := require.New(t)

	var n atomic.Int32
	first := make(chan struct{})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- spin.Run(ctx, seq(spin.Every(time.Hour, func(ctx context.Context) error {
			if n.Add(1) == 1 {
				close(first)
			}
			return nil
		})))
	}()

	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("the first pass waited for the interval")
	}

	cancel()
	x.NoError(<-done)
	x.Equal(int32(1), n.Load(), "it ran again inside an hour")
}

// TestAPassThatFailsEndsTheLoop, and by Run the process.
//
// A pass that should be tolerated logs it and answers nil: `f` is the only
// thing that knows whether what just failed was a database blinking or the
// schema having moved, and swallowing it here would decide for every caller in
// the direction that hides.
func TestAPassThatFailsEndsTheLoop(t *testing.T) {
	x := require.New(t)

	var n atomic.Int32
	err := spin.Run(t.Context(), seq(spin.Every(time.Millisecond, func(ctx context.Context) error {
		if n.Add(1) < 3 {
			return nil
		}

		return errors.New("the table is gone")
	})))
	x.ErrorContains(err, "the table is gone")
	x.Equal(int32(3), n.Load())
}

// TestEveryStopsWhenTheAppDoes, from between passes, which is where a sweep
// spends nearly all of its time.
func TestEveryStopsWhenTheAppDoes(t *testing.T) {
	x := require.New(t)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	err := spin.Run(ctx, seq(spin.Every(time.Hour, func(ctx context.Context) error { return nil })))
	x.NoError(err)
}
