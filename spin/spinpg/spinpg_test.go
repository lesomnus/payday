package spinpg

import (
	"context"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/spin"
)

// Two leases on one database are two processes of one deployment, for every
// purpose this package has: each holds on a session of its own, and *does the
// other see the hold* is the whole question.

func dsn(t *testing.T) string {
	t.Helper()

	v := os.Getenv(pdtest.Postgres)
	if v == "" {
		t.Skipf("%s is not set; these are PostgreSQL's locks", pdtest.Postgres)
	}

	return v
}

func lease(t *testing.T) *Lease {
	t.Helper()

	l, err := New(t.Context(), dsn(t), Retry(50*time.Millisecond), Check(50*time.Millisecond))
	require.NoError(t, err)

	return l
}

// named gives a test names of its own: the locks are the database's, and a test
// beside another on the same one would hold its names too.
func named(t *testing.T, n string) string { return t.Name() + "/" + n }

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

func notWithin[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("too soon: %s", what)
	case <-time.After(300 * time.Millisecond):
	}
}

// holding asks for `name` in the background, and hands over the release once
// it is held.
func holding(t *testing.T, l *Lease, name string) <-chan func() {
	t.Helper()

	got := make(chan func(), 1)
	go func() {
		_, release, err := l.Hold(t.Context(), name)
		if err == nil {
			got <- release
		}
	}()

	return got
}

// TestANameIsHeldByOneProcessAtATime, and by the next once it is given up.
func TestANameIsHeldByOneProcessAtATime(t *testing.T) {
	x := require.New(t)
	a, b := lease(t), lease(t)
	n := named(t, "sweep")

	held, release, err := a.Hold(t.Context(), n)
	x.NoError(err)
	x.NoError(held.Err())

	got := holding(t, b, n)
	notWithin(t, got, "a name held by two processes")

	release()
	within(t, got, "the name, once the other process gave it up")()
}

// TestAHoldGoesWithItsSession: the database ends the session that held a name
// -- the process gone, its connection cut -- and the name is free for another
// process, and the one that held it hears so within a ping.
func TestAHoldGoesWithItsSession(t *testing.T) {
	x := require.New(t)
	a, b := lease(t), lease(t)
	n := named(t, "drain")

	held, release, err := a.Hold(t.Context(), n)
	x.NoError(err)
	defer release()
	got := holding(t, b, n)
	notWithin(t, got, "a name held by two processes")

	admin, err := pgx.Connect(t.Context(), dsn(t))
	x.NoError(err)
	defer admin.Close(context.Background())
	var pid int32
	x.NoError(admin.QueryRow(t.Context(), `
		SELECT pid FROM pg_locks
		WHERE locktype = 'advisory' AND classid = $1::int4::oid AND objid = $2::int4::oid AND objsubid = 2`,
		Namespace, keyOf(n)).Scan(&pid), "the lock, where the database says it is")
	_, err = admin.Exec(t.Context(), "SELECT pg_terminate_backend($1)", pid)
	x.NoError(err)

	within(t, held.Done(), "the process hearing its hold was lost")
	within(t, got, "the name, held by the other process")()
}

// TestOneProcessDoesNotHoldANameTwice: a session lock is the session's, and
// asked for again on it would be granted again. Two loops of one name in one
// process take turns as two processes would.
func TestOneProcessDoesNotHoldANameTwice(t *testing.T) {
	x := require.New(t)
	a := lease(t)
	n := named(t, "sweep")

	_, release, err := a.Hold(t.Context(), n)
	x.NoError(err)
	got := holding(t, a, n)
	notWithin(t, got, "a name held twice in one process")

	release()
	within(t, got, "the name, once the other loop gave it up")()
}

// TestTheSessionAsksForKeepalivesOfItsOwn: otherwise a host that vanished keeps
// every name it held until the server's own keepalives give up on it.
func TestTheSessionAsksForKeepalivesOfItsOwn(t *testing.T) {
	x := require.New(t)
	a := lease(t)

	_, release, err := a.Hold(t.Context(), named(t, "k"))
	x.NoError(err)
	defer release()

	settings := map[string]string{}
	a.mu.Lock()
	rows, err := a.conn.Query(t.Context(), `
		SELECT name, setting FROM pg_settings
		WHERE name IN ('tcp_keepalives_idle', 'tcp_keepalives_interval', 'tcp_keepalives_count', 'application_name')`)
	if err == nil {
		for rows.Next() {
			var k, v string
			if err = rows.Scan(&k, &v); err != nil {
				break
			}
			settings[k] = v
		}
		rows.Close()
	}
	a.mu.Unlock()
	x.NoError(err)

	x.Equal("payday.spin", settings["application_name"], "a session the database can say is a lease's")
	if settings["tcp_keepalives_idle"] == "0" {
		t.Skip("connected over a Unix socket, where TCP keepalives read as zero")
	}
	x.Equal("30", settings["tcp_keepalives_idle"])
	x.Equal("10", settings["tcp_keepalives_interval"])
	x.Equal("3", settings["tcp_keepalives_count"])
}

// TestALeaseKeepsNoConnectionForNothing: with nothing held or waited for there
// is no session to keep.
func TestALeaseKeepsNoConnectionForNothing(t *testing.T) {
	x := require.New(t)
	a := lease(t)

	_, release, err := a.Hold(t.Context(), named(t, "x"))
	x.NoError(err)
	a.mu.Lock()
	x.NotNil(a.conn)
	a.mu.Unlock()

	release()
	a.mu.Lock()
	x.Nil(a.conn)
	a.mu.Unlock()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = a.Hold(ctx, named(t, "y"))
	x.ErrorIs(err, context.Canceled)
	a.mu.Lock()
	x.Zero(a.users, "a wait that ended still counted")
	a.mu.Unlock()
}

// TestTwoNamesOfOneLockAreRefused rather than made to take turns.
func TestTwoNamesOfOneLockAreRefused(t *testing.T) {
	x := require.New(t)
	a := lease(t)

	// As if `app.b` hashed as `app.a` does.
	a.names[keyOf("app.a")] = "app.b"
	_, _, err := a.Hold(t.Context(), "app.a")
	x.ErrorContains(err, `"app.b"`)
	x.ErrorContains(err, `"app.a"`)
}

// TestSpinRunsALoopInOneProcessOfTwo: [spin.Run] over this lease, in two
// processes of one deployment. The loop runs in one of them, and in the other
// once the first is gone.
func TestSpinRunsALoopInOneProcessOfTwo(t *testing.T) {
	x := require.New(t)
	n := named(t, "sweep")

	var active atomic.Int32
	started := make(chan int, 4)
	loop := func(process int) spin.Spinner {
		return spin.Called(n, spin.Func(func(ctx context.Context) error {
			if active.Add(1) > 1 {
				t.Error("the loop ran in two processes at once")
			}
			started <- process
			<-ctx.Done()
			active.Add(-1)
			return nil
		}))
	}

	ctxs := []context.CancelFunc{}
	done := []chan error{}
	for p := range 2 {
		ctx, cancel := context.WithCancel(t.Context())
		ctxs = append(ctxs, cancel)
		ch := make(chan error, 1)
		done = append(done, ch)
		l := lease(t)
		go func() { ch <- spin.Run(ctx, slices.Values([]any{loop(p)}), spin.WithLease(l)) }()
	}
	defer func() {
		for _, c := range ctxs {
			c()
		}
	}()

	first := within(t, started, "the loop starting in one process")
	notWithin(t, started, "the loop starting in the other too")

	ctxs[first]()
	x.NoError(within(t, done[first], "the first process stopping"))
	second := within(t, started, "the loop starting in the other process")
	x.NotEqual(first, second)
}
