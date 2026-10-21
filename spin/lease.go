package spin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/lesomnus/otx/log"
)

// Lease decides which process of a deployment runs a loop that only one of
// them may (#54).
//
// Without one, every process that calls [Run] runs every loop. That is right
// for one replica and **silently wrong for two**: each runs every sweep, the
// second over a row the first is already changing, and nothing reports it. A
// lease makes a loop the deployment's rather than the process's: [Run] holds
// the loop's name before starting it, and only the process holding it runs it.
//
// # What it promises, and what it cannot
//
// That a loop runs in **at most one process at a time, but for a moment around
// a failover**. Not exactly once: a process that lost its hold goes on until it
// notices, and one cut off from the database goes on writing whatever it had
// already sent. So a loop still has to be safe to run twice -- a lease takes
// that from always to rarely, and a loop that cannot stand rarely wants fencing
// or a queue, which `spin` is not (see `docs/runtime.md`).
//
// # Hold
//
// Hold answers once this process holds `name`, waiting for as long as another
// process holds it. `held` is done when the hold is lost -- a connection that
// went, a lock the database gave up -- and when `ctx` is. `release` gives the
// hold up, and is called once the loop is done with it, lost or not.
//
// Hold answers ctx's error when ctx ends first, which is the ordinary end of a
// process that never held the name. Any other error is a lease that cannot hold
// anything, and ends the process like a loop that gave up.
type Lease interface {
	Hold(ctx context.Context, name string) (held context.Context, release func(), err error)
}

// Everywhere is a loop each process of a deployment runs for itself, holding no
// lease: work that is the process's own -- a cache it keeps, a stream it holds
// open to keep one -- rather than the deployment's. Under a lease every other
// loop is one process's; see [InEveryProcess].
type Everywhere interface {
	SpinEverywhere()
}

// InEveryProcess marks a loop as [Everywhere].
//
// Exclusive is what a loop is when nothing says otherwise, because the two
// mistakes cost differently: a per-process loop left unmarked runs in one
// process and the others go stale, which is found the day they do; a
// deployment-wide one marked by mistake is the duplicate sweep a lease is for.
func InEveryProcess(s Spinner) Spinner { return everywhere{s} }

type everywhere struct{ Spinner }

func (everywhere) SpinEverywhere()   {}
func (e everywhere) Unwrap() Spinner { return e.Spinner }

// Called names a loop. Under a lease the name is what the processes of a
// deployment agree on, so a loop one of them runs has to have one of its own:
// a [Func] is otherwise called by its type, and every [Every] would be one loop.
//
// Names are the deployment's: prefix them with the app's, as `payday.trail` or
// `rove.sweep` are.
func Called(n string, s Spinner) Spinner { return called{n, s} }

type called struct {
	n string
	Spinner
}

func (c called) SpinName() string { return c.n }
func (c called) Unwrap() Spinner  { return c.Spinner }

// Local is a lease for a deployment of one process: a name is held by the first
// loop that asks for it, and by the next once that one gives it up. Loops that
// share one take turns, which is how a test tries a lease in one process.
func Local() Lease { return &local{held: map[string]chan struct{}{}} }

type local struct {
	mu   sync.Mutex
	held map[string]chan struct{}
}

func (l *local) Hold(ctx context.Context, n string) (context.Context, func(), error) {
	for {
		l.mu.Lock()
		gone, busy := l.held[n]
		if !busy {
			gone = make(chan struct{})
			l.held[n] = gone
			l.mu.Unlock()

			held, cancel := context.WithCancel(ctx)
			var once sync.Once
			return held, func() {
				once.Do(func() {
					cancel()
					l.mu.Lock()
					delete(l.held, n)
					l.mu.Unlock()
					close(gone)
				})
			}, nil
		}
		l.mu.Unlock()

		select {
		case <-gone:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

// Nowhere is a lease nothing ever holds: for a process that runs none of the
// loops only one process may -- a replica that answers requests and leaves the
// deployment's work to the others. Its [Everywhere] loops still run.
func Nowhere() Lease { return nowhere{} }

type nowhere struct{}

func (nowhere) Hold(ctx context.Context, _ string) (context.Context, func(), error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

// Option is something [Run] is told besides what to run.
type Option func(*options)

type options struct {
	lease Lease
}

// WithLease runs every loop that is not [Everywhere] in the one process of the
// deployment that holds its lease. See [Lease].
func WithLease(l Lease) Option { return func(o *options) { o.lease = l } }

// isEverywhere says whether a loop, or one it wraps, is [Everywhere].
func isEverywhere(s Spinner) bool {
	for s != nil {
		if _, ok := s.(Everywhere); ok {
			return true
		}
		u, ok := s.(interface{ Unwrap() Spinner })
		if !ok {
			return false
		}
		s = u.Unwrap()
	}
	return false
}

// explicit is the name a loop gave itself, or one it wraps gave itself; false
// for none, and a loop called by its type has none.
func explicit(s Spinner) (string, bool) {
	for s != nil {
		if v, ok := s.(Named); ok && v.SpinName() != "" {
			return v.SpinName(), true
		}
		u, ok := s.(interface{ Unwrap() Spinner })
		if !ok {
			return "", false
		}
		s = u.Unwrap()
	}
	return "", false
}

// agree refuses what the processes of a deployment could not tell apart: a loop
// one of them runs with no name of its own, and two loops with one name between
// them, which one lease would make take turns. Before anything starts, so a
// deployment finds out at its first start rather than at its first replica.
func agree(ss []Spinner) error {
	seen := map[string]bool{}
	for _, s := range ss {
		if isEverywhere(s) {
			continue
		}
		n, ok := explicit(s)
		if !ok {
			return fmt.Errorf("spin: %s is run by one process of a deployment and has no name for them to agree on; "+
				"name it with spin.Called, or say it is each process's own with spin.InEveryProcess", name(s))
		}
		if seen[n] {
			return fmt.Errorf("spin: two loops are called %q, and one lease would have them take turns", n)
		}
		seen[n] = true
	}
	return nil
}

// lead runs a loop for as long as this process holds its lease, and waits to
// hold it again whenever the hold is lost.
func lead(ctx context.Context, l Lease, s Spinner) error {
	n := name(s)
	lg := log.From(ctx).With(slog.String("spin", n))
	ctx = log.Into(ctx, lg)

	for {
		lg.DebugContext(ctx, "spin waits for its lease")
		held, release, err := l.Hold(ctx, n)
		if err != nil {
			if ctx.Err() != nil {
				// Shut down while another process held it: the ordinary end
				// of a loop in a process that never led.
				return nil
			}
			lg.ErrorContext(ctx, "spin cannot hold its lease", slog.Any("err", err))
			return fmt.Errorf("%s: lease: %w", n, err)
		}

		lg.InfoContext(ctx, "spin up", slog.Bool("spin.lease", true))
		err = s.Spin(held)
		// Asked before release, which ends `held` whatever happened.
		lost := held.Err() != nil && ctx.Err() == nil
		release()

		switch {
		case ctx.Err() != nil:
			lg.InfoContext(ctx, "spin down")
			return nil

		case lost:
			// Another process may be running it now, and this one waits its
			// turn. Whatever the loop answered is what being stopped part way
			// looks like -- a query canceled under it says so in its own words
			// -- so it is said, and not counted as the loop giving up.
			args := []any{}
			if err != nil && !errors.Is(err, context.Canceled) {
				args = append(args, slog.Any("err", err))
			}
			lg.WarnContext(ctx, "spin lost its lease, and waits to hold it again", args...)
			continue

		case err == nil:
			lg.InfoContext(ctx, "spin down")
			return nil

		default:
			lg.ErrorContext(ctx, "spin failed", slog.Any("err", err))
			return fmt.Errorf("%s: %w", n, err)
		}
	}
}
