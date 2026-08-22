package cmd_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
)

// The one command in the tree that does not end on its own.
//
// Everything here runs against the app's real server over a real connection and
// a real broker, for the reason the rest of `pdcmd_test.go` gives -- and for one
// more that is particular to this command: what a watch does is arrive later, so
// a test that called the server directly would be asserting on a channel and not
// on the thing a person reads.

// watched is a watch running, and what it has printed so far.
//
// The output is read while the command is still writing it, which is why it goes
// through a lock: a watch does not return, so waiting for it to finish before
// looking is waiting forever.
type watched struct {
	mu     sync.Mutex
	out    strings.Builder
	err    strings.Builder
	done   chan error
	cancel context.CancelFunc
}

type locked struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (l locked) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.Write(p)
}

func (w *watched) stdout() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.out.String()
}

func (w *watched) stderr() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.err.String()
}

// until waits for something a write should have put on the stream.
//
// Polled rather than signalled because what is being waited for is a whole line
// through a table writer, and a test that woke on every Write would have to know
// how many of them one event is.
func (w *watched) until(t *testing.T, read func() string, want string) string {
	t.Helper()

	for end := time.Now().Add(5 * time.Second); ; {
		if s := read(); strings.Contains(s, want) {
			return s
		}

		// A command that has already returned is not going to print it, and
		// waiting the whole five seconds to say so hides the error that is the
		// actual failure. Put back, because [watched.stop] reads the same one.
		select {
		case err := <-w.done:
			w.done <- err
			t.Fatalf("watch: ended while waiting for %q: %v\n%s", want, err, w.stderr())
		default:
		}

		if time.Now().After(end) {
			t.Fatalf("watch: waited for %q, and what arrived was:\n%s\n%s", want, read(), w.stderr())
		}

		time.Sleep(5 * time.Millisecond)
	}
}

// stop cancels the watch and answers with what it returned.
func (w *watched) stop() error {
	w.cancel()

	select {
	case err := <-w.done:
		return err
	case <-time.After(5 * time.Second):
		return context.DeadlineExceeded
	}
}

// watchCmd starts `robot watch` and answers before it has printed anything.
func (b *built) watchCmd(t *testing.T, conn *grpc.ClientConn, args ...string) *watched {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	w := &watched{done: make(chan error, 1), cancel: cancel}

	root := rooted(t, conn)
	root.Writer = locked{&w.mu, &w.out}
	root.ErrWriter = locked{&w.mu, &w.err}
	root.ReadCloser = io.NopCloser(strings.NewReader(""))

	go func() { w.done <- root.Run(b.travels(ctx), args) }()

	return w
}

// TestWatchIsTheSnapshotAndThenWhatHappens is the command doing the only thing
// it does.
//
// The row is named on the line and not in a trailing protojson, which is the
// form this command exists to have: a watch takes `filters` and **every one of
// them has to name a row** -- a watch with none is the whole table forever, and
// one that named a tenant would be that with extra steps. So the shortest
// request the server accepts is one row, and that is what an argument is for.
func TestWatchIsTheSnapshotAndThenWhatHappens(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	vs := b.sow(ctx, x, b.Tenant, 1, "arm-")

	conn := b.dialed(t, ctx)
	w := b.watchCmd(t, conn, "robot", "watch", "-o", "table", "@acme/arm-a")

	// What is already there, which is what the first message is.
	w.until(t, w.stdout, "arm-a")

	// Written over the connection and not through `Ungated`, because what
	// publishes is the interceptor: it runs once the handler has answered
	// without an error, which is the earliest moment it is known the
	// transaction committed. A write that never went through a request has no
	// such moment and tells nobody.
	_, err := app.NewClient(conn).Robot().Patch(b.travels(ctx), app.RobotPatchRequest_builder{
		Ref:         app.RobotRef_builder{Id: vs[0].GetId()}.Build(),
		Alias:       z.Ptr("arm-z"),
		DateUpdated: vs[0].GetDateUpdated(),
	}.Build())
	x.NoError(err)

	got := w.until(t, w.stdout, "arm-z")

	// The RPC that changed it, by the name gRPC knows it by.
	x.Contains(got, "Patch")

	// One header for the stream and not one between every event, which is what
	// makes a column of this readable at all.
	x.Equal(1, strings.Count(got, "ACTION"))

	// Stopped rather than ended: the person asked, so it is not a gap and not an
	// error.
	x.NoError(w.stop())
}

// TestWatchNamesARowThatIsGone is why the identifier is a column here and only a
// `-o wide` one everywhere else.
//
// A row that was erased -- or that moved out of the filters this stream named,
// which is deliberately the same message -- arrives with no value at all. Every
// column read through it is empty, so a line without the identifier would say
// that something had happened to something.
func TestWatchNamesARowThatIsGone(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	vs := b.sow(ctx, x, b.Tenant, 1, "arm-")

	conn := b.dialed(t, ctx)
	w := b.watchCmd(t, conn, "robot", "watch", "-o", "table", "@acme/arm-a")
	w.until(t, w.stdout, "arm-a")

	_, err := app.NewClient(conn).Robot().Erase(b.travels(ctx),
		app.RobotRef_builder{Id: vs[0].GetId()}.Build())
	x.NoError(err)

	got := w.until(t, w.stdout, "Erase")

	// The identifier is still said, and everything read through the value that
	// is not there reads as not set.
	k, err := pdid.From(vs[0].GetId())
	x.NoError(err)

	// Read as columns rather than as a substring, because a uuid has dashes in
	// it: a test that looked for "-" anywhere on the line would pass on a table
	// that had lost the value columns altogether.
	fs := strings.Fields(lineWith(got, "Erase"))
	x.Len(fs, 4)
	x.Contains(fs[0], "Erase")
	x.Equal("-", fs[1], "the alias, read through a value that is not there")
	x.Equal("-", fs[2], "and its age")
	x.Equal(k.String(), fs[3], "and the row is still named")

	x.NoError(w.stop())
}

// TestWatchFailsWhenTheStreamEnds is the default half of the ending, and the
// reason the command has an opinion about one at all.
//
// A watch has no backlog: a notification reaches whoever is listening and is
// then forgotten. So a stream that stopped and a stream where nothing is
// happening look exactly alike on the screen, and a command that returned
// quietly would leave somebody reading an empty screen and believing it.
func TestWatchFailsWhenTheStreamEnds(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sow(ctx, x, b.Tenant, 1, "arm-")

	// Held, so that this test can be the thing that ends the stream.
	g := b.grpc(t, pdtest.Logging(t))
	w := b.watchCmd(t, pdtest.Serve(t, g), "robot", "watch", "@acme/arm-a")
	w.until(t, w.stdout, "arm-a")

	g.Stop()

	select {
	case err := <-w.done:
		x.Error(err)
	case <-time.After(5 * time.Second):
		x.Fail("the stream ended and the command did not")
	}
}

// TestRetryReconnectsInsteadOfFailing is the other half, and it is the same
// failure: what changes is who decides what it means.
//
// Reconnecting takes the snapshot again, which is the only thing that says what
// was missed. That is why neither half is the default by accident -- exiting is
// what a script needs, and this is what somebody watching one wants.
func TestRetryReconnectsInsteadOfFailing(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sow(ctx, x, b.Tenant, 1, "arm-")

	g := b.grpc(t, pdtest.Logging(t))
	w := b.watchCmd(t, pdtest.Serve(t, g), "robot", "watch", "--retry", "@acme/arm-a")
	w.until(t, w.stdout, "arm-a")

	g.Stop()

	// Said where a `-o json` piped into something will not have it in the
	// middle: the answer is stdout and this is not part of the answer.
	w.until(t, w.stderr, "reconnecting")

	select {
	case err := <-w.done:
		x.Failf("the command ended", "with --retry, and the error was %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	x.NoError(w.stop())
}

// TestRetryAsksForTheSnapshotItSaidItDidNotNeed is the reconnect taking the
// snapshot again, which TestRetryReconnectsInsteadOfFailing cannot see: its
// server never comes back, so there is nothing there to answer one.
//
// The request says `skip_snapshot` -- "I know the current state" -- and after a
// gap that is no longer true: whatever changed while the connection was down
// was sent to nobody. So the reconnect has to ask for the snapshot whatever the
// request said the first time, or the person watching holds a row that is
// wrong until the next write happens to correct it, which may be never.
func TestRetryAsksForTheSnapshotItSaidItDidNotNeed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	vs := b.sow(ctx, x, b.Tenant, 1, "arm-")

	// Served by hand rather than through [pdtest.Serve], because this test
	// needs the one thing that helper does not give: a server that comes back.
	// Stopping a gRPC server closes the listener it was serving on, so the
	// second serving is a second listener, and the connection reaches
	// whichever one the dialer holds now.
	var mu sync.Mutex
	l := bufconn.Listen(1 << 20)

	serve := func() *grpc.Server {
		g := b.grpc(t, pdtest.Logging(t))

		mu.Lock()
		cur := l
		mu.Unlock()

		go func() {
			// A closed listener is how this ends, and is not a failure.
			_ = g.Serve(cur)
		}()
		t.Cleanup(g.Stop)

		return g
	}

	g := serve()

	conn, err := grpc.NewClient(
		"passthrough://bufconn",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			mu.Lock()
			cur := l
			mu.Unlock()

			return cur.DialContext(ctx)
		}),
	)
	x.NoError(err)
	t.Cleanup(func() { conn.Close() })

	// By identifier rather than `@acme/arm-a`, because the writes below rename
	// the row: a filter that named the alias would name nothing on reconnect,
	// and NotFound is a refusal `--retry` rightly does not retry.
	k, err := pdid.From(vs[0].GetId())
	x.NoError(err)

	w := b.watchCmd(t, conn, "robot", "watch", "--retry", k.String(), `{"skip_snapshot":true}`)

	// The stream says nothing until something changes, so something has to --
	// and it has to be a change the subscription was there for, which is a race
	// nothing outside the server can see. So the row is written again under a
	// fresh alias until one shows up: an event published before the handler
	// subscribed was sent to nobody, and no amount of waiting brings it.
	c := app.NewClient(conn).Robot()
	cur, last := vs[0], ""
	for i := 0; last == ""; i++ {
		x.Less(i, 25, "the stream never showed a write")

		alias := fmt.Sprintf("arm-%c", 'b'+i)
		v, err := c.Patch(b.travels(ctx), app.RobotPatchRequest_builder{
			Ref:         app.RobotRef_builder{Id: cur.GetId()}.Build(),
			Alias:       z.Ptr(alias),
			DateUpdated: cur.GetDateUpdated(),
		}.Build())
		x.NoError(err)
		cur = v

		for end := time.Now().Add(250 * time.Millisecond); time.Now().Before(end); {
			if strings.Contains(w.stdout(), alias) {
				last = alias
				break
			}

			time.Sleep(5 * time.Millisecond)
		}
	}

	g.Stop()
	w.until(t, w.stderr, "reconnecting")

	// The server comes back, on a fresh listener the dialer will find.
	mu.Lock()
	l = bufconn.Listen(1 << 20)
	mu.Unlock()
	serve()

	// The row again, and nothing has written since the stop: this is the
	// snapshot, taken by a request whose first words were "skip it".
	w.until(t, func() string {
		_, rest, _ := strings.Cut(w.stdout(), last)
		return rest
	}, last)

	x.NoError(w.stop())
}

// lineWith answers with the one line that holds `want`.
func lineWith(s string, want string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, want) {
			return l
		}
	}

	return ""
}

// TestRetryDoesNotLoopOnARequestTheServerRefused.
//
// `--retry` is about the connection going. A filter that names a row which is
// not there is refused the same way every time, and a loop that kept asking
// would be a command that never stops and never works -- which is worse than
// failing, because it looks like it is doing something.
func TestRetryDoesNotLoopOnARequestTheServerRefused(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	w := b.watchCmd(t, b.dialed(t, ctx), "robot", "watch", "--retry", "@acme/no-such-robot")

	select {
	case err := <-w.done:
		x.Error(err)
		x.NotContains(w.stderr(), "reconnecting", "there was nothing to reconnect to")
	case <-time.After(5 * time.Second):
		x.Fail("the command kept asking for something it had been refused")
	}
}
