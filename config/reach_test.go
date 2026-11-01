package config_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/config/brokerpg"
	"github.com/lesomnus/payday/config/leasepg"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/payday/watch"

	// Standing in for an app on PostgreSQL, as `dbsqlite3` stands in for one
	// on SQLite in db_test.go.
	_ "github.com/lesomnus/payday/config/dbpgx"
)

// These are the first contact as a deployment makes it, through `Open` and the
// two builds that dial, at an address that refuses until it is opened -- which
// is what a network policy not yet in place looks like from inside the pod
// (#192). Where there is something to reach it is the database PDTEST_POSTGRES
// names, through that address, and those skip without one; which errors are
// waited on, and the schedule, are reach_internal_test.go's.
//
// The address opens as the first refusal is said, and not after a time, so
// that the attempt after it is the one that finds it open on any machine,
// however loaded.

// TestADatabaseThatComesUpLateIsOpened: refused, and then there, it is opened,
// and the log says the refusal and that it was reached.
func TestADatabaseThatComesUpLateIsOpened(t *testing.T) {
	x := require.New(t)

	u := postgres(t)
	at := late(t, u.Host)

	var said logged
	said.when("db: cannot be reached", at.open)
	db, dialect, err := config.DbConfig{Driver: "pgx", Dsn: via(u, at.addr)}.Open(said.into(t.Context()))
	x.NoError(err)
	defer db.Close()

	x.Equal(config.DialectPostgres, dialect)
	x.NoError(db.PingContext(t.Context()))
	x.Contains(said.String(), `msg="db: cannot be reached; trying again" attempt=1 wait=200ms`)
	x.Contains(said.String(), "connection refused", "a refusal said without what it was")
	x.Contains(said.String(), `msg="db: reached" attempts=2`)
}

// TestADatabaseThatAnswersIsNotWaitedFor: what the database said is returned
// at once, with nothing said in the log -- a wrong password is a person's to
// fix, and ten seconds of retrying it would only hide it.
func TestADatabaseThatAnswersIsNotWaitedFor(t *testing.T) {
	t.Run("SQLite, which has no network to wait for", func(t *testing.T) {
		x := require.New(t)

		var said logged
		_, _, err := config.DbConfig{Driver: "sqlite3", Dsn: "file:/no/such/directory/test.db"}.Open(said.into(t.Context()))
		x.ErrorContains(err, "ping")
		x.Empty(said.String())
	})
	t.Run("a wrong password", func(t *testing.T) {
		x := require.New(t)

		u := postgres(t)
		wrong := *u
		wrong.User = url.UserPassword(u.User.Username(), "not-"+password(u))

		var said logged
		_, _, err := config.DbConfig{Driver: "pgx", Dsn: wrong.String()}.Open(said.into(t.Context()))
		if err == nil {
			t.Skip("this server takes any password")
		}
		x.ErrorContains(err, "28P01")
		x.Empty(said.String())
	})
	t.Run("a database that does not exist", func(t *testing.T) {
		x := require.New(t)

		u := postgres(t)
		nowhere := *u
		nowhere.Path = "/pdtest_nonesuch"

		var said logged
		_, _, err := config.DbConfig{Driver: "pgx", Dsn: nowhere.String()}.Open(said.into(t.Context()))
		x.ErrorContains(err, "3D000")
		x.Empty(said.String())
	})
}

// TestADatabaseLostLaterIsNotWaitedFor: the first contact is the only one
// waited for. Once `Open` has answered, a database that goes away is the
// driver's to say, at the statement that finds out, as it always was.
func TestADatabaseLostLaterIsNotWaitedFor(t *testing.T) {
	x := require.New(t)

	u := postgres(t)
	at := late(t, u.Host)
	at.open()

	var said logged
	db, _, err := config.DbConfig{Driver: "pgx", Dsn: via(u, at.addr)}.Open(said.into(t.Context()))
	x.NoError(err)
	defer db.Close()
	x.NoError(db.PingContext(t.Context()))

	at.stop()
	x.Error(db.PingContext(t.Context()), "a database that went away still answered")
	x.Error(db.PingContext(t.Context()))
	x.Empty(said.String(), "a running server's lost database was waited for")
}

// TestABrokerAndALeaseAreWaitedForAsTheDatabaseIs: both dial their own first
// contact as a server is built, after the database's and perhaps at an address
// of their own (`watch.dsn`, `spin.dsn`), so they are given the same moment.
func TestABrokerAndALeaseAreWaitedForAsTheDatabaseIs(t *testing.T) {
	t.Run("registered", func(t *testing.T) {
		x := require.New(t)

		broker := late(t, "")
		config.RegisterBroker("test-late", func(context.Context, config.WatchConfig, config.DbConfig) (watch.Broker, error) {
			return watch.Memory(), dialed(broker.addr)
		})
		lease := late(t, "")
		config.RegisterLease("test-late", func(context.Context, config.SpinConfig, config.DbConfig) (spin.Lease, error) {
			return spin.Local(), dialed(lease.addr)
		})

		var said logged
		said.when("watch.broker: test-late: cannot be reached", broker.open)
		said.when("spin.lease: test-late: cannot be reached", lease.open)
		_, err := config.WatchConfig{Broker: "test-late"}.Build(said.into(t.Context()), config.DbConfig{})
		x.NoError(err)
		_, err = config.SpinConfig{Lease: "test-late"}.Build(said.into(t.Context()), config.DbConfig{})
		x.NoError(err)

		x.Contains(said.String(), `msg="watch.broker: test-late: cannot be reached; trying again" attempt=1`)
		x.Contains(said.String(), `msg="watch.broker: test-late: reached" attempts=2`)
		x.Contains(said.String(), `msg="spin.lease: test-late: cannot be reached; trying again" attempt=1`)
		x.Contains(said.String(), `msg="spin.lease: test-late: reached" attempts=2`)
	})
	t.Run("but not one that answered", func(t *testing.T) {
		x := require.New(t)

		no := errors.New("no")
		n := 0
		config.RegisterBroker("test-answered", func(context.Context, config.WatchConfig, config.DbConfig) (watch.Broker, error) {
			n++
			return nil, no
		})

		var said logged
		_, err := config.WatchConfig{Broker: "test-answered"}.Build(said.into(t.Context()), config.DbConfig{})
		x.ErrorIs(err, no)
		x.Equal(1, n)
		x.Empty(said.String())
	})
	t.Run("postgres", func(t *testing.T) {
		x := require.New(t)

		u := postgres(t)
		db := config.DbConfig{Dialect: config.DialectPostgres, Dsn: u.String()}

		broker := late(t, u.Host)
		lease := late(t, u.Host)
		var said logged
		said.when("watch.broker: postgres: cannot be reached", broker.open)
		said.when("spin.lease: postgres: cannot be reached", lease.open)

		_, err := config.WatchConfig{Broker: brokerpg.Name, Dsn: via(u, broker.addr)}.Build(said.into(t.Context()), db)
		x.NoError(err)
		_, err = config.SpinConfig{Lease: leasepg.Name, Dsn: via(u, lease.addr)}.Build(said.into(t.Context()), db)
		x.NoError(err)

		x.Contains(said.String(), `msg="watch.broker: postgres: cannot be reached; trying again" attempt=1`)
		x.Contains(said.String(), `msg="watch.broker: postgres: reached" attempts=2`)
		x.Contains(said.String(), `msg="spin.lease: postgres: cannot be reached; trying again" attempt=1`)
		x.Contains(said.String(), `msg="spin.lease: postgres: reached" attempts=2`)
	})
}

// postgres is the server PDTEST_POSTGRES names, or a skip.
func postgres(t *testing.T) *url.URL {
	t.Helper()

	v := os.Getenv(pdtest.Postgres)
	if v == "" {
		t.Skipf("%s is not set", pdtest.Postgres)
	}
	u, err := url.Parse(v)
	require.NoError(t, err)
	if u.Host == "" {
		t.Skipf("%s is not an address over TCP, which is what is put in front of it here", pdtest.Postgres)
	}

	return u
}

func password(u *url.URL) string {
	v, _ := u.User.Password()
	return v
}

// via is `u` at `addr` instead.
func via(u *url.URL, addr string) string {
	v := *u
	v.Host = addr
	return v.String()
}

// dialed is how a dial to `addr` went: nil, or a real refusal.
func dialed(addr string) error {
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return err
	}
	return c.Close()
}

// lateAddr is an address that refuses every connection until it is opened,
// and from then on takes them: passed through to `upstream` if there is one,
// and held otherwise.
type lateAddr struct {
	t        *testing.T
	addr     string
	upstream string

	mu      sync.Mutex
	l       net.Listener
	conns   []net.Conn
	stopped bool
	wg      sync.WaitGroup
}

func late(t *testing.T, upstream string) *lateAddr {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	a := &lateAddr{t: t, addr: l.Addr().String(), upstream: upstream}
	require.NoError(t, l.Close())
	t.Cleanup(a.stop)

	return a
}

// open listens where it refused.
func (a *lateAddr) open() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return
	}

	l, err := net.Listen("tcp", a.addr)
	if err != nil {
		// Taken in the moment it was closed, which is rare and is not what
		// the test is about.
		a.t.Errorf("listen again at %s: %v", a.addr, err)
		return
	}
	a.l = l
	a.wg.Go(func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			a.hold(c)
			if a.upstream != "" {
				a.wg.Go(func() { a.pass(c) })
			}
		}
	})
}

// pass carries `c` to the upstream and back until either end closes.
func (a *lateAddr) pass(c net.Conn) {
	u, err := net.DialTimeout("tcp", a.upstream, 5*time.Second)
	if err != nil {
		c.Close()
		return
	}
	a.hold(u)

	go func() {
		_, _ = io.Copy(u, c)
		u.Close()
		c.Close()
	}()
	_, _ = io.Copy(c, u)
	c.Close()
	u.Close()
}

func (a *lateAddr) hold(c net.Conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		c.Close()
		return
	}
	a.conns = append(a.conns, c)
}

// stop closes what it listens on and every connection through it: the
// database gone, as far as anything on this side of it can tell.
func (a *lateAddr) stop() {
	a.mu.Lock()
	a.stopped = true
	if a.l != nil {
		a.l.Close()
	}
	for _, c := range a.conns {
		c.Close()
	}
	a.mu.Unlock()

	a.wg.Wait()
}

// logged is what a test's log was told, as text, and what to do the first time
// it is told something.
type logged struct {
	mu    sync.Mutex
	b     bytes.Buffer
	hooks []*hook
}

type hook struct {
	said  string
	then  func()
	fired bool
}

func (l *logged) into(ctx context.Context) context.Context {
	return log.Into(ctx, slog.New(slog.NewTextHandler(l, nil)))
}

// when calls `then` as a record with `said` in it is first written, before the
// call that logged it goes on.
func (l *logged) when(said string, then func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hooks = append(l.hooks, &hook{said: said, then: then})
}

// Write is told a record at a time: a text handler writes each in one call.
func (l *logged) Write(p []byte) (int, error) {
	l.mu.Lock()
	n, err := l.b.Write(p)
	var due []func()
	for _, h := range l.hooks {
		if !h.fired && bytes.Contains(p, []byte(h.said)) {
			h.fired = true
			due = append(due, h.then)
		}
	}
	l.mu.Unlock()

	for _, f := range due {
		f()
	}
	return n, err
}

func (l *logged) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
