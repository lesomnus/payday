package auth

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
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
