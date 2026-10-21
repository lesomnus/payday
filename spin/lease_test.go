package spin_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/spin"
)

// within fails the test unless `ch` delivers before long.
func within[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("not in time: %s", what)
		var zero T
		return zero
	}
}

// TestALeasedLoopRunsInTheOneProcessHoldingIt: two processes of a deployment,
// one lease between them. The loop runs in one of them at a time, and in the
// other once the first is gone.
func TestALeasedLoopRunsInTheOneProcessHoldingIt(t *testing.T) {
	x := require.New(t)
	lease := spin.Local()

	var active, most atomic.Int32
	started := make(chan int, 2)
	loop := func(process int) spin.Spinner {
		return spin.Called("app.sweep", spin.Func(func(ctx context.Context) error {
			n := active.Add(1)
			for {
				m := most.Load()
				if n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			started <- process
			<-ctx.Done()
			active.Add(-1)
			return nil
		}))
	}

	ctx1, stop1 := context.WithCancel(t.Context())
	ctx2, stop2 := context.WithCancel(t.Context())
	defer stop2()
	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() { done1 <- spin.Run(ctx1, seq(loop(1)), spin.WithLease(lease)) }()
	go func() { done2 <- spin.Run(ctx2, seq(loop(2)), spin.WithLease(lease)) }()

	first := within(t, started, "the loop starting somewhere")
	select {
	case p := <-started:
		t.Fatalf("both processes ran it: %d and %d", first, p)
	case <-time.After(200 * time.Millisecond):
	}

	// The one that runs it goes away, and the other takes it up.
	if first == 1 {
		stop1()
		x.NoError(within(t, done1, "the first process stopping"))
	} else {
		stop2()
		x.NoError(within(t, done2, "the first process stopping"))
	}
	second := within(t, started, "the loop starting in the other process")
	x.NotEqual(first, second)
	x.EqualValues(1, most.Load(), "two at once")

	stop1()
	stop2()
}

// leaseOf is a lease the test takes holds away from.
type leaseOf struct {
	holds atomic.Int32
	taken chan context.CancelFunc
}

func (l *leaseOf) Hold(ctx context.Context, _ string) (context.Context, func(), error) {
	held, cancel := context.WithCancel(ctx)
	l.holds.Add(1)
	select {
	case l.taken <- cancel:
	case <-ctx.Done():
		cancel()
		return nil, nil, ctx.Err()
	}
	return held, cancel, nil
}

// TestALoopThatLostItsHoldWaitsToHoldItAgain: a hold lost under a loop stops
// it, and the process waits its turn -- it does not end, and the loop starts
// again once it holds the lease again. What the loop answered on its way out,
// a query canceled under it say, is not it giving up.
func TestALoopThatLostItsHoldWaitsToHoldItAgain(t *testing.T) {
	x := require.New(t)
	lease := &leaseOf{taken: make(chan context.CancelFunc)}

	starts := make(chan struct{}, 4)
	loop := spin.Called("app.drain", spin.Func(func(ctx context.Context) error {
		starts <- struct{}{}
		<-ctx.Done()
		return errors.New("canceling statement due to user request")
	}))

	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- spin.Run(ctx, seq(loop), spin.WithLease(lease)) }()

	lose := within(t, lease.taken, "the first hold")
	within(t, starts, "the loop starting")
	lose()

	within(t, lease.taken, "a second hold, after the first was lost")
	within(t, starts, "the loop starting again")
	select {
	case err := <-done:
		t.Fatalf("a lost hold ended the process: %v", err)
	default:
	}

	stop()
	x.NoError(within(t, done, "shutting down"))
	x.EqualValues(2, lease.holds.Load())
}

// TestAProcessThatHoldsNothingRunsItsOwnLoopsAlone: under [spin.Nowhere] a
// loop one process runs never starts, one each process runs for itself does,
// and shutting down while waiting is not a failure.
func TestAProcessThatHoldsNothingRunsItsOwnLoopsAlone(t *testing.T) {
	x := require.New(t)

	var shared atomic.Bool
	own := make(chan struct{}, 1)
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- spin.Run(ctx, seq(
			spin.Called("app.sweep", spin.Func(func(ctx context.Context) error {
				shared.Store(true)
				<-ctx.Done()
				return nil
			})),
			spin.InEveryProcess(spin.Func(func(ctx context.Context) error {
				own <- struct{}{}
				<-ctx.Done()
				return nil
			})),
		), spin.WithLease(spin.Nowhere()))
	}()

	within(t, own, "a process's own loop starting")
	time.Sleep(100 * time.Millisecond)
	x.False(shared.Load(), "a loop one process runs, run where nothing is held")

	stop()
	x.NoError(within(t, done, "shutting down while waiting"))
}

// TestALoopUnderALeaseHasANameOfItsOwn: the processes of a deployment agree on
// a loop by its name, so under a lease one with none of its own -- a [spin.Func]
// is called by its type, and every one would be one loop -- and two with one
// name are refused before anything starts.
func TestALoopUnderALeaseHasANameOfItsOwn(t *testing.T) {
	x := require.New(t)

	var ran atomic.Bool
	f := spin.Func(func(ctx context.Context) error {
		ran.Store(true)
		return nil
	})

	err := spin.Run(t.Context(), seq(f), spin.WithLease(spin.Local()))
	x.ErrorContains(err, "spin.Called")
	err = spin.Run(t.Context(), seq(spin.Called("app.a", f), spin.Called("app.a", f)), spin.WithLease(spin.Local()))
	x.ErrorContains(err, `"app.a"`)
	x.False(ran.Load(), "a loop started before the others were refused")

	// A loop each process runs needs no name to agree on, wrapped either way
	// round; and nothing changes without a lease.
	x.NoError(spin.Run(t.Context(), seq(spin.InEveryProcess(f), spin.Called("app.b", spin.InEveryProcess(f))), spin.WithLease(spin.Nowhere())))
	x.NoError(spin.Run(t.Context(), seq(f, f)))
	x.True(ran.Load())
}

// TestALoopThatGivesUpUnderItsLeaseEndsTheProcess, as one without a lease does.
func TestALoopThatGivesUpUnderItsLeaseEndsTheProcess(t *testing.T) {
	x := require.New(t)

	err := spin.Run(t.Context(), seq(spin.Called("app.sweep", spin.Func(func(ctx context.Context) error {
		return errors.New("the schema moved")
	}))), spin.WithLease(spin.Local()))
	x.ErrorContains(err, "app.sweep")
	x.ErrorContains(err, "the schema moved")
}

// TestALeaseThatCannotHoldAnythingEndsTheProcess.
func TestALeaseThatCannotHoldAnythingEndsTheProcess(t *testing.T) {
	x := require.New(t)

	err := spin.Run(t.Context(), seq(spin.Called("app.sweep", spin.Func(func(ctx context.Context) error {
		t.Fatal("ran without a hold")
		return nil
	}))), spin.WithLease(broken{}))
	x.ErrorContains(err, "app.sweep")
	x.ErrorContains(err, "cannot hold")
}

type broken struct{}

func (broken) Hold(context.Context, string) (context.Context, func(), error) {
	return nil, nil, errors.New("cannot hold")
}

// TestLocalTakesTurns: one [spin.Local] between two loops of one name holds it
// for one of them, and for the other once it is given up.
func TestLocalTakesTurns(t *testing.T) {
	x := require.New(t)
	l := spin.Local()

	_, release, err := l.Hold(t.Context(), "app.a")
	x.NoError(err)

	var wg sync.WaitGroup
	got := make(chan struct{})
	wg.Go(func() {
		_, r, err := l.Hold(t.Context(), "app.a")
		if err == nil {
			close(got)
			r()
		}
	})
	select {
	case <-got:
		t.Fatal("held twice")
	case <-time.After(100 * time.Millisecond):
	}

	_, other, err := l.Hold(t.Context(), "app.b")
	x.NoError(err, "another name is another hold")
	other()

	release()
	within(t, got, "the second hold, once the first was given up")
	wg.Wait()

	ctx, cancel := context.WithCancel(t.Context())
	_, again, err := l.Hold(t.Context(), "app.a")
	x.NoError(err)
	cancel()
	_, _, err = l.Hold(ctx, "app.a")
	x.ErrorIs(err, context.Canceled, "waiting, and shut down")
	again()
}

// TestARenamedLoopTakesTurnsWithTheBuildBefore: a rolling upgrade runs the build
// that called a loop `app.sweep` beside the one that calls it `app.tidy`. Said
// so with [spin.Formerly], the new build holds the old name as well as its own,
// so the loop runs in one process of either at a time -- and in the new one
// once the old has gone.
func TestARenamedLoopTakesTurnsWithTheBuildBefore(t *testing.T) {
	x := require.New(t)
	lease := spin.Local()

	var active, most atomic.Int32
	started := make(chan string, 2)
	loop := func(build string) spin.Spinner {
		return spin.Func(func(ctx context.Context) error {
			n := active.Add(1)
			for {
				m := most.Load()
				if n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			started <- build
			<-ctx.Done()
			active.Add(-1)
			return nil
		})
	}

	before, stopBefore := context.WithCancel(t.Context())
	after, stopAfter := context.WithCancel(t.Context())
	defer stopAfter()
	doneBefore := make(chan error, 1)
	doneAfter := make(chan error, 1)
	go func() {
		doneBefore <- spin.Run(before, seq(spin.Called("app.sweep", loop("before"))), spin.WithLease(lease))
	}()
	x.Equal("before", within(t, started, "the build before running its loop"))

	go func() {
		doneAfter <- spin.Run(after, seq(spin.Formerly("app.sweep", spin.Called("app.tidy", loop("after")))), spin.WithLease(lease))
	}()
	select {
	case b := <-started:
		t.Fatalf("the loop ran in both builds at once: before and %s", b)
	case <-time.After(200 * time.Millisecond):
	}

	stopBefore()
	x.NoError(within(t, doneBefore, "the build before stopping"))
	x.Equal("after", within(t, started, "the new build taking the loop up"))
	x.EqualValues(1, most.Load(), "two at once")

	stopAfter()
	x.NoError(within(t, doneAfter, "the new build stopping"))
}

// TestANameALoopWasCalledIsItsOwn: a name a loop was called is refused beside
// a loop that holds it, as a name is, and so is a former name that is nothing
// or the loop's own.
func TestANameALoopWasCalledIsItsOwn(t *testing.T) {
	x := require.New(t)

	f := spin.Func(func(ctx context.Context) error { return nil })
	refused := func(ss ...any) error {
		// Refused before anything starts, or waiting on a lease that holds
		// nothing until the second is up.
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		return spin.Run(ctx, seq(ss...), spin.WithLease(spin.Nowhere()))
	}

	err := refused(spin.Called("app.a", f), spin.Formerly("app.a", spin.Called("app.b", f)))
	x.ErrorContains(err, `"app.a"`)
	x.ErrorContains(err, "app.b")

	err = refused(spin.Formerly("app.x", spin.Called("app.a", f)), spin.Formerly("app.x", spin.Called("app.b", f)))
	x.ErrorContains(err, `"app.x"`)

	x.ErrorContains(refused(spin.Formerly("app.a", spin.Called("app.a", f))), "its own name")
	x.ErrorContains(refused(spin.Formerly("", spin.Called("app.a", f))), "called nothing")

	// Either way round, and beside a loop each process runs, which holds
	// nothing.
	x.NoError(spin.Run(t.Context(), seq(
		spin.Called("app.a", spin.Formerly("app.old", f)),
		spin.InEveryProcess(spin.Formerly("app.a", f)),
	), spin.WithLease(spin.Local())))
}

// namesOf is a lease the test hands each name out of, one at a time.
type namesOf struct {
	asked chan string
	given chan context.CancelFunc
}

func (l *namesOf) Hold(ctx context.Context, n string) (context.Context, func(), error) {
	select {
	case l.asked <- n:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}

	held, cancel := context.WithCancel(ctx)
	select {
	case l.given <- cancel:
	case <-ctx.Done():
		cancel()
		return nil, nil, ctx.Err()
	}

	return held, cancel, nil
}

// TestALoopHoldsEveryNameItHas: a renamed loop holds each of its names, in one
// order, starts once it holds all of them, and stops when it loses any. One lost
// while the next is waited for is a lost hold too -- the loop waits to hold them
// all again -- and not a lease that cannot hold anything.
func TestALoopHoldsEveryNameItHas(t *testing.T) {
	x := require.New(t)
	lease := &namesOf{asked: make(chan string), given: make(chan context.CancelFunc)}

	starts := make(chan struct{}, 4)
	loop := spin.Formerly("app.b", spin.Called("app.a", spin.Func(func(ctx context.Context) error {
		starts <- struct{}{}
		<-ctx.Done()
		return nil
	})))

	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- spin.Run(ctx, seq(loop), spin.WithLease(lease)) }()

	// Both, in their order, and the loop once it holds them.
	x.Equal("app.a", within(t, lease.asked, "the first name asked for"))
	loseA := within(t, lease.given, "the first name given")
	x.Equal("app.b", within(t, lease.asked, "the second name asked for"))
	loseB := within(t, lease.given, "the second name given")
	within(t, starts, "the loop starting")

	// The second lost: the loop stops and asks for both again.
	loseB()
	x.Equal("app.a", within(t, lease.asked, "the first name, asked for again"))
	loseA()
	loseA = within(t, lease.given, "the first name, given again")

	// The first lost while the second is waited for: asked for again from the
	// start, and the process does not end.
	x.Equal("app.b", within(t, lease.asked, "the second name, asked for again"))
	loseA()
	x.Equal("app.a", within(t, lease.asked, "the first name, asked for once more"))
	select {
	case err := <-done:
		t.Fatalf("a name lost while waiting ended the process: %v", err)
	default:
	}

	stop()
	x.NoError(within(t, done, "shutting down"))
}

// paced is one process's view of a shared lease, asking for every name after
// its first a moment late -- long enough for another process to have asked for
// its own first.
type paced struct {
	spin.Lease
	asked atomic.Int32
}

func (l *paced) Hold(ctx context.Context, n string) (context.Context, func(), error) {
	if l.asked.Add(1) > 1 {
		time.Sleep(100 * time.Millisecond)
	}

	return l.Lease.Hold(ctx, n)
}

// TestTwoNamesAreAskedForInOneOrder: a loop renamed from `app.b` to `app.a`
// and one renamed back, side by side through a rollback, hold the same two
// names. Each asks for them in the one order every process asks in, so one of
// them holds both and runs -- where asking for its own name first would have
// each hold one and wait for the other's, and neither run.
func TestTwoNamesAreAskedForInOneOrder(t *testing.T) {
	shared := spin.Local()

	started := make(chan struct{}, 2)
	loop := spin.Func(func(ctx context.Context) error {
		started <- struct{}{}
		<-ctx.Done()
		return nil
	})

	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	go func() {
		_ = spin.Run(ctx, seq(spin.Formerly("app.b", spin.Called("app.a", loop))), spin.WithLease(&paced{Lease: shared}))
	}()
	go func() {
		_ = spin.Run(ctx, seq(spin.Formerly("app.a", spin.Called("app.b", loop))), spin.WithLease(&paced{Lease: shared}))
	}()

	within(t, started, "one of the two running")
}
