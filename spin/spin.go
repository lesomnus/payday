// Package spin runs what the layers of a server have to do besides answering
// requests.
//
// A request is not the only reason a server does something. Rows get swept,
// leases renewed, an outbox drained -- work that belongs to a layer, needs what
// that layer holds, and happens on a clock rather than because somebody asked.
// A layer with such work answers to [Spinner], and [Run] finds the ones that do.
//
// # Why it is not a method on the server
//
// The other design is `Spin(ctx)` on the app's `Server` interface, walked
// recursively. That interface is generated, so a method on it is a method the
// generator has to emit and every `Overlay`, `StaticServer` and
// `UnimplementedServer` has to carry -- and every layer then inherits an empty
// one, which is exactly the shape that hides the answer to "does this layer
// have background work?" Every layer would say yes and almost all of them would
// mean no.
//
// payday takes the road the generated package already took for everything else
// a layer can do: a capability is found, not declared. Nothing is added to
// `Server`, [Run] walks a stack that was built and asks each layer, and a layer
// with nothing to run writes not one line.
//
// # What a failure means
//
// A [Spinner] that answers with an error takes the process down. It has said it
// is giving up, and a sweep that gave up quietly is found days later -- by which
// time the table it was keeping small is why something else fell over. So [Run]
// stops the rest, waits for them, and answers with the failure; whatever called
// it ends the process.
//
// An earlier system decided the other way -- log it and start it again after a
// wait -- and that is the conservative half of the same trade: a database that
// blinked is not a reason to stop serving requests. payday keeps the loud half
// and leaves the quiet one to the loop, which is the only thing that knows
// whether what just failed was a blink or the schema having moved underneath
// it. A pass that should be tolerated logs and answers nil; an error means
// giving up.
//
// # It is not readiness
//
// Nothing here touches a health check, and that is deliberate rather than
// unfinished. Tying a spinner to readiness means one GC loop failing pulls a
// whole replica out of the load balancer while every request it was answering
// was fine. A deployment that wants that says so where it reads [Run]'s error,
// which is a decision about one deployment rather than one this package makes
// for all of them.
package spin

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"time"

	"github.com/lesomnus/otx/log"
	"golang.org/x/sync/errgroup"
)

// Spinner is a layer with something to run for as long as the app is running.
//
// It is answered by the layer itself, so what it runs has whatever the layer
// has: the servers behind it, the database it can reach, the rules it applies.
// Work that goes *through* a layer's own rules is the point -- a sweep that
// wrote around them would be a second implementation of the app.
//
// The context is done when the app is shutting down and Spin must return when
// it is. Answering nil is a loop saying it is finished, which is not a failure.
// Answering an error is it giving up, which ends the process; see the package
// comment for why that direction was chosen.
//
// Nothing here waits on a Spin's behalf, so whatever it holds it releases
// before it returns.
type Spinner interface {
	Spin(ctx context.Context) error
}

// Named is a [Spinner] that says what to call it, in the log and in the error
// [Run] answers with. A layer that is not one is called by its type, which
// reads well for a layer and says nothing at all for a [Func].
type Named interface {
	SpinName() string
}

// Run starts every [Spinner] in `layers` and returns once they have all
// stopped.
//
// The sequence is the stack: the `Iter` the generator emits for an app's
// `Server` is exactly it. It is a parameter rather than the stack itself
// because payday cannot name a type that is generated per app, and a sequence
// costs the caller one word at the one place this is wired. Anything else that
// spins is passed the same way, so a deployment is not obliged to make a layer
// out of a loop that is not one.
//
// A layer that is not a [Spinner] is skipped, which is most of them, and a
// stack where none of them is answers nil at once.
//
// It answers nil when everything stopped because `ctx` was done, and the first
// failure otherwise -- having canceled the rest and waited for them, so a
// caller returning from main is not racing a loop that still holds a connection.
//
// Each layer given is started once. A deployment that built two stacks over one
// set of layers hands over the one that owns the background work rather than
// both: there is no deduplication here, since a layer is a struct an app wrote
// and is not required to be comparable.
func Run[T any](ctx context.Context, layers iter.Seq[T]) error {
	// The group's context is what the loops are handed, so the first failure
	// stops the others instead of leaving them running under a process that is
	// on its way out.
	g, ctx := errgroup.WithContext(ctx)

	n := 0
	for v := range layers {
		s, ok := any(v).(Spinner)
		if !ok {
			continue
		}

		n++
		g.Go(func() error { return run(ctx, s) })
	}

	// How many there were is worth saying once. A deployment that expected its
	// sweep to be running and finds zero here is one whose layer was left out
	// of the build, and nothing else would ever mention it.
	log.From(ctx).DebugContext(ctx, "spinning", slog.Int("spin.count", n))

	return g.Wait()
}

// run starts one loop and reports what it stopped for.
func run(ctx context.Context, s Spinner) error {
	l := log.From(ctx).With(slog.String("spin", name(s)))
	ctx = log.Into(ctx, l)

	l.InfoContext(ctx, "spin up")

	switch err := s.Spin(ctx); {
	case err == nil:
		// Finished, which is not the same as having fallen over: a loop that
		// means to keep going does not return.
		l.InfoContext(ctx, "spin down")
		return nil

	case errors.Is(err, ctx.Err()):
		// What a loop that was in the middle of something when the app was told
		// to go answers with, and it is the shutdown that was asked for rather
		// than a failure -- otherwise every clean stop would end in one.
		//
		// It is compared against the context rather than against
		// [context.Canceled] on its own, because `ctx.Err()` is nil while
		// nothing has been canceled and nothing is `errors.Is` a nil target. So
		// a Spin that answers Canceled out of nowhere -- a loop that carried the
		// wrong context, which is a bug and not a shutdown -- still falls
		// through to the loud case.
		l.InfoContext(ctx, "spin down")
		return nil

	default:
		l.ErrorContext(ctx, "spin failed", slog.String("error", err.Error()))

		// Named, because an errgroup answers with the error and nothing else,
		// and "connection refused" alone does not say which loop to go and look
		// at.
		return fmt.Errorf("%s: %w", name(s), err)
	}
}

// name is what a loop is called in the log and in the error.
func name(s Spinner) string {
	if v, ok := s.(Named); ok {
		return v.SpinName()
	}

	return fmt.Sprintf("%T", s)
}

// Every answers with a [Spinner] that runs `f` every `d` until the app is
// shutting down.
//
// The tick is the wait *between* passes rather than a schedule: one that takes
// longer than `d` does not double up on itself, and one that was missed is not
// made up afterwards. That is what a sweep wants, and whatever wants a wall
// clock wants a schedule and not this.
//
// It runs `f` once as it starts. A loop whose first pass is a whole interval
// away does nothing at all in a deployment that restarts more often than that.
//
// A pass that fails ends the loop, and by [Run] the process. That is the
// contract rather than an oversight: `f` is the only thing that knows whether
// what just failed was a database blinking or the schema having moved, so a
// pass that should be tolerated logs it and answers nil. Swallowing it here
// would decide for every caller, and in the direction that hides.
func Every(d time.Duration, f func(ctx context.Context) error) Func {
	return func(ctx context.Context) error {
		for {
			if err := f(ctx); err != nil {
				return err
			}

			select {
			case <-ctx.Done():
				// Not a failure. The app is shutting down, and a sweep caught
				// between passes has nothing left to put away.
				return nil
			case <-time.After(d):
			}
		}
	}
}

// Func is a [Spinner] made of a function, for a layer that would rather compose
// one than write a method -- and for a test.
type Func func(ctx context.Context) error

func (f Func) Spin(ctx context.Context) error { return f(ctx) }
