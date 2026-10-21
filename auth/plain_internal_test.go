package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/test/bufconn"
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

	// And it believes what Plain believes, of a caller in this process.
	ctx := peer.NewContext(
		metadata.NewIncomingContext(t.Context(), metadata.Pairs(Header, PlainScheme+" @acme/admin")),
		overBufconn(t))
	want, err := Plain().Handle(ctx)
	x.NoError(err)
	got, err := PlainInProcess().Handle(ctx)
	x.NoError(err)
	x.Equal(want, got)
}

// overBufconn is the peer a server sees on a call over `bufconn`, got the way a
// server gets it: from the connection it accepted.
func overBufconn(t *testing.T) *peer.Peer {
	t.Helper()

	l := bufconn.Listen(1 << 10)
	t.Cleanup(func() { _ = l.Close() })

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err == nil {
			accepted <- c
		}
	}()

	dialed, err := l.Dial()
	require.NoError(t, err)
	c := <-accepted
	t.Cleanup(func() { _ = dialed.Close(); _ = c.Close() })

	return &peer.Peer{Addr: c.RemoteAddr(), LocalAddr: c.LocalAddr()}
}

// TestPlainInProcessBelievesOnlyThisProcess: a name is believed of a call over
// `bufconn`, and refused of one that came any other way -- a port, a socket, no
// connection at all -- with an error that stops a [Seq]. A call that says no
// name is not refused, so the next handler still reads what it carried.
func TestPlainInProcessBelievesOnlyThisProcess(t *testing.T) {
	fromOutsideSaid = sync.Once{}
	t.Cleanup(func() { fromOutsideSaid = sync.Once{} })

	var b bytes.Buffer
	logged := log.Into(t.Context(), slog.New(slog.NewTextHandler(&b, nil)))

	named := func(ctx context.Context) context.Context {
		return metadata.NewIncomingContext(ctx, metadata.Pairs(Header, PlainScheme+" @acme/admin"))
	}
	from := func(ctx context.Context, a net.Addr) context.Context {
		return peer.NewContext(ctx, &peer.Peer{Addr: a})
	}
	port := &net.TCPAddr{IP: net.IPv4(203, 0, 113, 7), Port: 41234}

	t.Run("a call over bufconn is believed", func(t *testing.T) {
		x := require.New(t)

		id, err := PlainInProcess().Handle(peer.NewContext(named(logged), overBufconn(t)))
		x.NoError(err)
		x.Equal("@acme/admin", id.Name())
	})

	for what, ctx := range map[string]context.Context{
		"over a port":     from(named(logged), port),
		"over a socket":   from(named(logged), &net.UnixAddr{Name: "/run/app.sock", Net: "unix"}),
		"with no peer":    named(logged),
		"with a nil addr": peer.NewContext(named(logged), &peer.Peer{}),
	} {
		t.Run("a name "+what+" is refused", func(t *testing.T) {
			x := require.New(t)

			_, err := PlainInProcess().Handle(ctx)
			x.ErrorIs(err, ErrFromOutside)
			x.False(errors.Is(err, ErrNoCredential), "a refusal, not nothing said: a Seq would ask the next handler")
		})
	}

	t.Run("saying nothing over a port is saying nothing", func(t *testing.T) {
		x := require.New(t)

		_, err := PlainInProcess().Handle(from(logged, port))
		x.ErrorIs(err, ErrNoCredential)

		other := HandlerFunc(func(context.Context) (Identity, error) { return Identity{Alias: "cookie"}, nil })
		id, err := Seq(PlainInProcess(), other).Handle(from(logged, port))
		x.NoError(err)
		x.Equal("cookie", id.Alias, "the next handler of a Seq was not asked")
	})

	t.Run("and it says so once", func(t *testing.T) {
		x := require.New(t)

		said := b.String()
		x.Equal(1, strings.Count(said, "level=ERROR"), "said it %q", said)
		x.Contains(said, "from outside this process")
		x.Contains(said, "network=")
	})
}
