package auth

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

// TestPlainSaysSoOnce is the one thing a deployment must not do quietly.
//
// `Plain` believes whatever it is told, so a process serving it where anybody
// can reach it has no authentication -- and nothing else about that process
// looks unusual. It starts, it answers, its tests pass.
//
// It is an in-package test because the latch is package state, and a test that
// cannot reset it is one that passes for whichever test happened to run first.
// The version of this that lived outside passed alone and failed in the suite,
// which is the wrong way round.
func TestPlainSaysSoOnce(t *testing.T) {
	x := require.New(t)

	plainSaid = sync.Once{}
	t.Cleanup(func() { plainSaid = sync.Once{} })

	var b bytes.Buffer
	was := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(was) })

	Plain()
	Plain()
	Plain()

	said := b.String()
	x.Equal(1, strings.Count(said, "level=WARN"), "said it %q", said)
	x.Contains(said, "every caller is believed")
}

// TestPlainInProcessSaysWhereAndNotAsAWarning: a server only this process
// reaches believes its callers as Plain does, and says so once at info -- the
// warning would read as a hole that is not there, and say nothing about which
// server it meant. A process with both kinds still warns about the one that
// matters.
func TestPlainInProcessSaysWhereAndNotAsAWarning(t *testing.T) {
	x := require.New(t)

	plainSaid, plainInProcessSaid = sync.Once{}, sync.Once{}
	t.Cleanup(func() { plainSaid, plainInProcessSaid = sync.Once{}, sync.Once{} })

	var b bytes.Buffer
	was := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(was) })

	PlainInProcess()
	PlainInProcess()

	said := b.String()
	x.Equal(1, strings.Count(said, "level=INFO"), "said it %q", said)
	x.Zero(strings.Count(said, "level=WARN"), "warned of a server only this process reaches: %q", said)
	x.Contains(said, "only this process reaches")

	Plain()
	x.Equal(1, strings.Count(b.String(), "level=WARN"), "the other kind was not warned about: %q", b.String())

	// And it believes what Plain believes.
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(Header, PlainScheme+" @acme/admin"))
	want, err := Plain().Handle(ctx)
	x.NoError(err)
	got, err := PlainInProcess().Handle(ctx)
	x.NoError(err)
	x.Equal(want, got)
}
