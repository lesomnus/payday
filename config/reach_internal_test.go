package config

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/lesomnus/otx/log"
	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stretchr/testify/require"
)

// The clock is driven here rather than waited on, as grpcx's limiter's is:
// what ten seconds of patience does would otherwise be ten seconds of test,
// and a test that sleeps for what it measures fails on a loaded machine. The
// errors are real ones, from real dials, because which errors are waited on is
// the half of this a mistake hides in.

// TestTheFirstContactIsTriedForTenSeconds pins the schedule a deployment gets:
// 200ms, doubled up to 2s, the last wait cut so that the last attempt is made
// at ten seconds -- nine attempts -- and each refusal said with its attempt and
// the wait before the next.
func TestTheFirstContactIsTriedForTenSeconds(t *testing.T) {
	x := require.New(t)

	refused := refusal(t)
	c := &ticking{now: time.Unix(0, 0)}
	s := &said{}
	n := 0
	err := patience{within: reachWithin, first: reachFirst, most: reachMost, clock: c}.
		reach(s.into(t.Context()), "db", func(context.Context) error {
			n++
			return refused
		})

	x.ErrorIs(err, refused)
	x.ErrorContains(err, "not reached in 10s")
	x.Equal(9, n)
	x.Equal([]time.Duration{
		200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond,
		2 * time.Second, 2 * time.Second, 2 * time.Second, 1 * time.Second,
	}, c.slept)
	x.Equal([]string{
		"WARN db: cannot be reached; trying again attempt=1 wait=200ms",
		"WARN db: cannot be reached; trying again attempt=2 wait=400ms",
		"WARN db: cannot be reached; trying again attempt=3 wait=800ms",
		"WARN db: cannot be reached; trying again attempt=4 wait=1.6s",
		"WARN db: cannot be reached; trying again attempt=5 wait=2s",
		"WARN db: cannot be reached; trying again attempt=6 wait=2s",
		"WARN db: cannot be reached; trying again attempt=7 wait=2s",
		"WARN db: cannot be reached; trying again attempt=8 wait=1s",
		"ERROR db: cannot be reached; giving up attempts=9 after=10s",
	}, s.lines())
	x.True(s.saidErr(), "a refusal was logged without what it was")
}

// TestWhatComesUpIsReached, and the log says so: an operator who reads two
// warnings should not have to wonder whether it gave up.
func TestWhatComesUpIsReached(t *testing.T) {
	x := require.New(t)

	refused := refusal(t)
	c := &ticking{now: time.Unix(0, 0)}
	s := &said{}
	n := 0
	err := patience{within: reachWithin, first: reachFirst, most: reachMost, clock: c}.
		reach(s.into(t.Context()), "db", func(context.Context) error {
			n++
			if n < 3 {
				return refused
			}
			return nil
		})

	x.NoError(err)
	x.Equal(3, n)
	x.Equal([]string{
		"WARN db: cannot be reached; trying again attempt=1 wait=200ms",
		"WARN db: cannot be reached; trying again attempt=2 wait=400ms",
		"INFO db: reached attempts=3 after=600ms",
	}, s.lines())

	// And a first contact that is made at once says nothing at all.
	s = &said{}
	x.NoError(patience{within: reachWithin, first: reachFirst, most: reachMost, clock: c}.
		reach(s.into(t.Context()), "db", func(context.Context) error { return nil }))
	x.Empty(s.lines())
}

// TestAnAnswerIsNotWaitedOn: returned as it is, after one attempt, and nothing
// logged -- the caller says it, as it said it before any of this.
func TestAnAnswerIsNotWaitedOn(t *testing.T) {
	x := require.New(t)

	no := connect(t, "postgres://app@"+answering(t, "28P01")+"/app?sslmode=disable")
	c := &ticking{now: time.Unix(0, 0)}
	s := &said{}
	n := 0
	err := patience{within: reachWithin, first: reachFirst, most: reachMost, clock: c}.
		reach(s.into(t.Context()), "db", func(context.Context) error {
			n++
			return no
		})

	x.Same(no, err)
	x.Equal(1, n)
	x.Empty(c.slept)
	x.Empty(s.lines())
}

// TestTheBoundIsTimeAndNotAttempts: no attempt begins more than `within` after
// the first, however few there were, so an attempt that is slow to fail -- a
// dial whose SYNs were dropped -- spends the bound rather than adding to it.
func TestTheBoundIsTimeAndNotAttempts(t *testing.T) {
	x := require.New(t)

	refused := refusal(t)
	c := &ticking{now: time.Unix(0, 0)}
	s := &said{}
	n := 0
	err := patience{within: reachWithin, first: reachFirst, most: reachMost, clock: c}.
		reach(s.into(t.Context()), "db", func(context.Context) error {
			n++
			c.now = c.now.Add(6 * time.Second)
			return refused
		})

	x.ErrorIs(err, refused)
	x.Equal(2, n)
	x.Equal([]string{
		"WARN db: cannot be reached; trying again attempt=1 wait=200ms",
		"ERROR db: cannot be reached; giving up attempts=2 after=12.2s",
	}, s.lines())
}

// TestStoppingIsNotWaitedOut: a process asked to stop while it waits stops
// waiting, and one stopped by the time its attempt fails does not wait at all.
func TestStoppingIsNotWaitedOut(t *testing.T) {
	x := require.New(t)

	// The wall's wait, which is the one a process has: an hour of it, asked
	// to stop, is no wait at all; and a wait nobody stops ends.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	x.ErrorIs(wall{}.Sleep(ctx, time.Hour), context.Canceled)
	x.NoError(wall{}.Sleep(t.Context(), time.Millisecond))

	refused := refusal(t)

	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	n := 0
	err := patience{within: time.Hour, first: time.Hour, most: time.Hour, clock: stopping{cancel}}.
		reach(ctx, "db", func(context.Context) error {
			n++
			return refused
		})
	x.ErrorIs(err, context.Canceled)
	x.Equal(1, n)

	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	c := &ticking{now: time.Unix(0, 0)}
	n = 0
	err = patience{within: time.Hour, first: time.Hour, most: time.Hour, clock: c}.
		reach(ctx, "db", func(context.Context) error {
			n++
			cancel()
			return refused
		})
	x.ErrorIs(err, refused)
	x.Equal(1, n)
	x.Empty(c.slept)
}

// TestWhatReachedNothing is the line [unreachable] draws, on what the drivers
// payday ships actually answer -- pgx at addresses that refuse, reset, hang or
// answer as PostgreSQL does, and SQLite at files it cannot open.
func TestWhatReachedNothing(t *testing.T) {
	closed := closedAddr(t)
	reset := resetting(t)
	cut := closing(t)

	for _, v := range []struct {
		what string
		err  error
		is   bool
	}{
		{"a dial nothing took", refusal(t), true},
		{"a Unix socket that is not there yet",
			dial("unix", filepath.Join(t.TempDir(), ".s.PGSQL.5432")), true},
		{"pgx at a port nothing listens on",
			connect(t, "postgres://app@"+closed+"/app?sslmode=disable"), true},
		{"pgx at it twice, with TLS and without (prefer)",
			connect(t, "postgres://app@"+closed+"/app"), true},
		{"pgx at a connection taken and reset",
			connect(t, "postgres://app@"+reset+"/app?sslmode=disable"), true},
		{"pgx at a connection taken and reset, twice (prefer)",
			connect(t, "postgres://app@"+reset+"/app"), true},
		{"pgx at a connection closed before a word was said",
			connect(t, "postgres://app@"+cut+"/app?sslmode=disable"), true},
		{"pgx at a connection closed before a word was said, twice (prefer)",
			connect(t, "postgres://app@"+cut+"/app"), true},
		{"pgx at a connection that never answers, under connect_timeout",
			connect(t, "postgres://app@"+hanging(t)+"/app?sslmode=disable&connect_timeout=1"), true},
		{"pgx at a resolver that could not be asked",
			lookup(t, &net.DNSError{Err: "read: connection refused", Name: "db", IsTemporary: true}), true},
		{"pgx at a resolver that timed out",
			lookup(t, &net.DNSError{Err: "i/o timeout", Name: "db", IsTimeout: true, IsTemporary: true}), true},
		{"PostgreSQL starting up (57P03)",
			connect(t, "postgres://app@"+answering(t, notYet)+"/app?sslmode=disable"), true},

		{"a name no resolver knows",
			lookup(t, &net.DNSError{Err: "no such host", Name: "db", IsNotFound: true}), false},
		{"a wrong password (28P01)",
			connect(t, "postgres://app@"+answering(t, "28P01")+"/app?sslmode=disable"), false},
		{"a database that does not exist (3D000)",
			connect(t, "postgres://app@"+answering(t, "3D000")+"/app?sslmode=disable"), false},
		{"a wrong password at one host and nothing at the other",
			connect(t, fmt.Sprintf("host=%s,%s port=%s,%s user=app dbname=app sslmode=disable",
				host(closed), host(closed), port(closed), port(answering(t, "28P01")))), false},
		{"a wrong password after TLS was refused (prefer)",
			connect(t, "postgres://app@"+answering(t, "28P01")+"/app"), false},
		{"57P03 after TLS was refused (prefer), knowingly",
			connect(t, "postgres://app@"+answering(t, notYet)+"/app"), false},
		{"a server that will not speak TLS (require)",
			connect(t, "postgres://app@"+answering(t, "28P01")+"/app?sslmode=require"), false},
		{"a certificate nobody vouches for (verify-full)",
			connect(t, "postgres://app@"+tlsAnswering(t)+"/app?sslmode=verify-full"), false},

		{"SQLite in a directory that is not there", ping(t, "file:/no/such/directory/test.db"), false},
		{"SQLite at a directory", ping(t, "file:"+t.TempDir()), false},
		{"SQLite read-only at a file that is not there",
			ping(t, "file:"+filepath.Join(t.TempDir(), "missing.db")+"?mode=ro"), false},

		{"the caller's cancellation", context.Canceled, false},
		{"anything else", errors.New("no"), false},
		{"nothing", nil, false},
	} {
		t.Run(v.what, func(t *testing.T) {
			require.Equal(t, v.is, unreachable(v.err), "%v", v.err)
		})
	}
}

// refusal is a real refused dial.
func refusal(t *testing.T) error {
	t.Helper()
	return dial("tcp", closedAddr(t))
}

func dial(network, addr string) error {
	c, err := net.DialTimeout(network, addr, 5*time.Second)
	if err == nil {
		c.Close()
		return errors.New("dialed " + addr + ", which was to refuse")
	}
	return err
}

// closedAddr is an address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	return addr
}

func host(addr string) string { h, _, _ := net.SplitHostPort(addr); return h }
func port(addr string) string { _, p, _ := net.SplitHostPort(addr); return p }

// connect is how pgx refused `dsn`.
func connect(t *testing.T, dsn string) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	c, err := pgconn.Connect(ctx, dsn)
	if err == nil {
		c.Close(ctx)
		t.Fatalf("connected to %s, which was to refuse", dsn)
	}
	return err
}

// lookup is how pgx refused a host its resolver answered `with`.
func lookup(t *testing.T, with *net.DNSError) error {
	t.Helper()

	cfg, err := pgconn.ParseConfig("postgres://app@db:5432/app?sslmode=disable")
	require.NoError(t, err)
	cfg.LookupFunc = func(context.Context, string) ([]string, error) { return nil, with }

	_, err = pgconn.ConnectConfig(t.Context(), cfg)
	require.Error(t, err)
	return err
}

// ping is how the SQLite driver refused `dsn`.
func ping(t *testing.T, dsn string) error {
	t.Helper()

	db, err := sql.Open("sqlite3", dsn)
	require.NoError(t, err)
	defer db.Close()

	err = db.PingContext(t.Context())
	require.Error(t, err)
	return err
}

// serve answers every connection to an address of its own with `with`.
func serve(t *testing.T, with func(net.Conn)) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var wg sync.WaitGroup
	t.Cleanup(func() {
		l.Close()
		wg.Wait()
	})
	wg.Go(func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer c.Close()
				with(c)
			})
		}
	})

	return l.Addr().String()
}

// resetting takes a connection and closes it with what was sent unread, which
// is a reset.
func resetting(t *testing.T) string {
	return serve(t, func(c net.Conn) {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		_, _ = c.Read(make([]byte, 1))
	})
}

// closing takes a connection, reads what the client opened it with, and closes
// it without a word: what a proxy in front of nothing does.
func closing(t *testing.T) string {
	return serve(t, func(c net.Conn) {
		_, _ = pgproto3.NewBackend(c, c).ReceiveStartupMessage()
	})
}

// hanging takes a connection and says nothing until the client gives up.
func hanging(t *testing.T) string {
	return serve(t, func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
}

// answering answers the start of every connection as PostgreSQL would refuse
// it, FATAL with `code`, after saying no to TLS.
func answering(t *testing.T, code string) string {
	return serve(t, func(c net.Conn) {
		b := pgproto3.NewBackend(c, c)
		m, err := b.ReceiveStartupMessage()
		if _, ok := m.(*pgproto3.SSLRequest); ok && err == nil {
			if _, err := c.Write([]byte("N")); err != nil {
				return
			}
			m, err = b.ReceiveStartupMessage()
		}
		if _, ok := m.(*pgproto3.StartupMessage); !ok || err != nil {
			return
		}

		b.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: code, Message: "said by a test"})
		_ = b.Flush()
	})
}

// tlsAnswering says yes to TLS, with a certificate nobody vouches for.
func tlsAnswering(t *testing.T) string {
	t.Helper()

	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pdtest"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	require.NoError(t, err)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}

	return serve(t, func(c net.Conn) {
		b := pgproto3.NewBackend(c, c)
		if m, err := b.ReceiveStartupMessage(); err != nil {
			return
		} else if _, ok := m.(*pgproto3.SSLRequest); !ok {
			return
		}
		if _, err := c.Write([]byte("S")); err != nil {
			return
		}
		_ = tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}}).Handshake()
	})
}

// ticking is a clock that moves when it is waited on, or when a test moves it.
type ticking struct {
	now   time.Time
	slept []time.Duration
}

func (c *ticking) Now() time.Time { return c.now }

func (c *ticking) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	return nil
}

// stopping is a clock whose every wait the process is asked to stop during.
type stopping struct{ stop context.CancelFunc }

func (stopping) Now() time.Time { return time.Unix(0, 0) }

func (c stopping) Sleep(ctx context.Context, _ time.Duration) error {
	c.stop()
	<-ctx.Done()
	return ctx.Err()
}

// said is what a test's log was told.
type said struct {
	mu sync.Mutex
	rs []slog.Record
}

func (s *said) into(ctx context.Context) context.Context { return log.Into(ctx, slog.New(s)) }

func (s *said) Enabled(context.Context, slog.Level) bool { return true }
func (s *said) WithAttrs([]slog.Attr) slog.Handler       { return s }
func (s *said) WithGroup(string) slog.Handler            { return s }

func (s *said) Handle(_ context.Context, r slog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rs = append(s.rs, r.Clone())
	return nil
}

// lines is each record as its level, its message and its attributes but the
// error, which is a driver's sentence.
func (s *said) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var vs []string
	for _, r := range s.rs {
		v := []string{r.Level.String(), r.Message}
		r.Attrs(func(a slog.Attr) bool {
			if a.Key != "err" {
				v = append(v, a.String())
			}
			return true
		})
		vs = append(vs, strings.Join(v, " "))
	}
	return vs
}

// saidErr is whether every record but one that was reached said its error.
func (s *said) saidErr() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, r := range s.rs {
		if r.Level == slog.LevelInfo {
			continue
		}
		found := false
		r.Attrs(func(a slog.Attr) bool {
			found = found || (a.Key == "err" && a.Value.Any() != nil)
			return true
		})
		if !found {
			return false
		}
	}
	return true
}
