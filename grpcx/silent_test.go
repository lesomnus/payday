package grpcx

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/otx"
	"github.com/lesomnus/otx/otxtest"
)

// TestASilentServerSaysSo, which is the only way it can be found.
//
// A server built on a context with no telemetry answers every call and writes
// nothing anywhere. There is no error to return and no counter to read: the log
// is what is missing, so the absence has no reporter but this one.
func TestASilentServerSaysSo(t *testing.T) {
	t.Run("with nothing on the context", func(t *testing.T) {
		x := require.New(t)

		got := captured(t, func() { Serving(context.Background()) })
		x.Contains(got, "logs, traces and measures nothing")
		x.Contains(got, "Otel.Build", "it has to say what to call")
	})

	t.Run("and not when there is one", func(t *testing.T) {
		x := require.New(t)

		ctx := otx.Into(context.Background(), otxtest.New(t).Otx)

		got := captured(t, func() { Serving(ctx) })
		x.Empty(got, "a server that is observed has nothing to be told")
	})

	t.Run("once, because it is about the process", func(t *testing.T) {
		x := require.New(t)

		got := captured(t, func() {
			Serving(context.Background())
			Serving(context.Background())
		})
		x.Equal(1, strings.Count(got, "grpcx:"),
			"a server per test would otherwise print this per test")
	})
}

// captured runs f with stderr replaced, and answers with what it wrote.
//
// The warning is reported once per process, so the [sync.Once] is reset around
// each case -- otherwise the first subtest is the only one that can observe
// anything and the rest pass by saying nothing about anything.
func captured(t *testing.T, f func()) string {
	t.Helper()

	said = sync.Once{}
	t.Cleanup(func() { said = sync.Once{} })

	r, w, err := os.Pipe()
	require.NoError(t, err)

	was := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = was }()

	f()
	w.Close()

	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Close()

	return sb.String()
}
