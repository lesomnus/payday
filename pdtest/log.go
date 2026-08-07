package pdtest

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/lesomnus/otx/log"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/grpcx"
)

// Logger answers with a logger that writes to the log of the test, so that
// whatever the server says while a test runs is attached to that test and shown
// only if it fails.
//
// It is where a recovered panic and its stack end up, which is the case it
// exists for: without it, a handler that panicked is an Internal error and no
// hint of where.
func Logger(tb testing.TB) *slog.Logger {
	w := &tbWriter{tb: tb}
	tb.Cleanup(w.close)

	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// Logging answers with the option that has everything a server does written to
// the log of the test.
//
// It has to be a stats handler and it has to be **first**. A stats handler is
// the only thing gRPC lets put something into the context of a call before
// anything else runs, and what reads a logger out of that context -- the record
// of a call arriving and being answered -- is a stats handler too. An
// interceptor would be installed behind them, and what an interceptor puts in
// the context is not something they ever see.
func Logging(tb testing.TB) grpc.ServerOption {
	l := Logger(tb)

	return grpcx.Seed(func(ctx context.Context) context.Context {
		return log.Into(ctx, l)
	})
}

// tbWriter writes to the log of a test until that test is over, since logging
// to a test that has finished is a panic -- and a server that outlives the test
// by a moment, which any server with a goroutine in it does, will try.
type tbWriter struct {
	mu   sync.Mutex
	tb   testing.TB
	done bool
}

func (w *tbWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.done {
		w.tb.Log(strings.TrimRight(string(p), "\n"))
	}

	return len(p), nil
}

func (w *tbWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.done = true
}
