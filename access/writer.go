package access

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/lesomnus/otx/log"
)

// Writer writes the access record: a read's row a second after it, with the
// others of that second, and a write's and a download's at once.
//
// # Why a read waits
//
// A read has no transaction of its own to write its row in, and a page is a
// dozen reads. A row each, at once, is a statement in front of every answer;
// queued, it is one statement a second for all of them. What that costs is the
// queue: a process killed outright loses the last second's reads. One that is
// stopped writes the queue before it goes ([Writer.Spin]).
//
// # Each process's own
//
// What is queued is in this process's memory, so every process runs its own
// loop rather than one of them holding a lease for the deployment.
type Writer struct {
	// Sink writes rows: the generated `pd.AccessSink`.
	Sink func(ctx context.Context, rs []Record) error

	// Every is how long a read waits to be written with the others; a
	// second when zero.
	Every time.Duration

	// Max is how many reads may wait, while the database cannot be written;
	// a hundred thousand when zero. Past it the oldest go, said in the log as
	// an error: what is lost is lost loudly.
	Max int

	mu    sync.Mutex
	queue []Record

	// spun is whether [Writer.Spin] has begun; told, whether the log has
	// heard that it had not, with this much waiting.
	spun, told bool

	// log is the process's log as [Writer.Spin] was handed it, which is
	// where a loss is said: the one a context with nothing in it reaches is
	// the global, and nothing installs that.
	log *slog.Logger
}

// logger is where the writer says what it lost.
func (w *Writer) logger() *slog.Logger {
	if w.log != nil {
		return w.log
	}

	return log.From(context.Background())
}

// unspun is how many reads may wait with nothing writing them before the log
// is told: a writer nobody handed to `spin.Run` records nothing, and says so
// once rather than not at all.
const unspun = 1000

// Queue keeps rows to be written with the next batch.
func (w *Writer) Queue(rs ...Record) {
	if len(rs) == 0 {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	w.queue = append(w.queue, rs...)
	if !w.spun && !w.told && len(w.queue) >= unspun {
		w.told = true
		w.logger().Error("access: reads are waiting and nothing writes them; hand the writer to spin.Run", "rows", len(w.queue))
	}
	if over := len(w.queue) - w.max(); over > 0 {
		w.queue = w.queue[over:]
		w.logger().Error("access: the record could not be written, and the oldest reads waiting for it were dropped", "dropped", over)
	}
}

// Write writes rows now: a write's, and a download's, which leaves only once
// its row is written.
func (w *Writer) Write(ctx context.Context, rs ...Record) error {
	if len(rs) == 0 {
		return nil
	}

	return w.Sink(ctx, rs)
}

// Flush writes what is queued. What it could not write waits for the next.
func (w *Writer) Flush(ctx context.Context) error {
	w.mu.Lock()
	rs := w.queue
	w.queue = nil
	w.mu.Unlock()

	if len(rs) == 0 {
		return nil
	}
	if err := w.Sink(ctx, rs); err != nil {
		w.mu.Lock()
		w.queue = append(rs, w.queue...)
		w.mu.Unlock()

		return err
	}

	return nil
}

// Pending is how many rows wait to be written.
func (w *Writer) Pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return len(w.queue)
}

func (w *Writer) max() int {
	if w.Max > 0 {
		return w.Max
	}

	return 100_000
}

func (w *Writer) every() time.Duration {
	if w.Every > 0 {
		return w.Every
	}

	return time.Second
}

// SpinName is what the loop is called.
func (w *Writer) SpinName() string { return "payday.access" }

// SpinEverywhere says each process runs its own; see [spin.InEveryProcess].
func (w *Writer) SpinEverywhere() {}

// Spin writes the queue every [Writer.Every], and once more when it is
// stopped: a process being stopped is the moment a queue is most worth
// writing, and the context it was given is gone by then, so the last write
// has a few seconds of its own.
//
// What a server answers while it drains is queued after that, since the two
// stop together: whatever stops the server writes the queue once more when it
// has ([Writer.Flush]), as the template's `serve` does.
func (w *Writer) Spin(ctx context.Context) error {
	w.mu.Lock()
	w.spun = true
	w.log = log.From(ctx)
	w.mu.Unlock()

	t := time.NewTicker(w.every())
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			last, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := w.Flush(last); err != nil {
				log.From(ctx).ErrorContext(ctx, "access: the record waiting to be written was lost as the process stopped", "rows", w.Pending(), "err", err)
			}

			return nil

		case <-t.C:
			if err := w.Flush(ctx); err != nil {
				log.From(ctx).WarnContext(ctx, "access: the record could not be written; it is tried again", "rows", w.Pending(), "err", err)
			}
		}
	}
}
