// Package spinpg is a [spin.Lease] over PostgreSQL's advisory locks (#54).
//
// # Why this one first
//
// For the reason `watch/watchpg` came first: an app that keeps its rows in
// Postgres already has a Postgres, and a session advisory lock is a statement
// it can already issue. Nothing is stored -- no table, no migration -- and
// nothing hangs on a clock: the database holds a lock for as long as the session
// that took it, and lets it go when the session ends, which is what a process
// ending is.
//
// # One connection, and what losing it costs
//
// A process takes every name it holds on one connection of its own, and not on
// one of the app's pool: a session lock is the session's, and a pooled
// connection is lent to whoever asks next. Losing that connection loses every
// hold on it at once, which is the truth -- the database has let them all go --
// so every loop that held one is stopped, and waits to hold it again.
//
// It notices by asking. A process holding anything pings the connection every
// [Check], and a ping that fails is a connection that is gone. Until then it
// goes on as if it held what it held, which is the window no lease closes (see
// [spin.Lease]).
//
// A connection is made outside the lock every hold shares, one dial at a time
// and under the context of the loop that asked, so a database that does not
// answer keeps waiting only the loops that are waiting anyway -- and a process
// shutting down stops dialing when it is told to, rather than once every loop
// has had its own dial time out in turn.
//
// # The other half of noticing
//
// The database notices a process that is gone when its connection closes. A
// host that vanished closes nothing, and the server hears of it only when TCP
// keepalives give up on the connection: two hours, on Linux's defaults, with
// every name it held held all that while. So the session asks for keepalives of
// its own -- idle 30s, then every 10s, three times -- and another process holds
// the names within about a minute of the host going. An address that says
// otherwise (`tcp_keepalives_idle` in the DSN) is taken at its word.
//
// # Not through a transaction pooler
//
// A pooler in transaction mode hands the session to another client between
// transactions, so a lock taken on it is held by nobody in particular. It is why
// `LISTEN` does not survive one either; `spin.dsn` is for an address that
// reaches the database directly.
//
// # The lock space
//
// Two keys, `pg_try_advisory_lock(int, int)`: [Namespace] for payday's leases,
// and a hash of the name. The two-key form is a space of its own -- PostgreSQL
// never confuses it with `pg_advisory_lock(bigint)` -- so a lock an app takes for
// a reason of its own cannot collide with one of these. Two names that hash
// alike in one process are refused rather than made to take turns. Who holds
// what is the database's to say:
//
//	SELECT a.pid, a.client_addr, a.backend_start, l.objid
//	FROM pg_locks l JOIN pg_stat_activity a USING (pid)
//	WHERE l.locktype = 'advisory' AND l.classid = 1885631344 AND l.objsubid = 2;
package spinpg

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lesomnus/otx/log"

	"github.com/lesomnus/payday/spin"
)

// Namespace is the first key of every lock a lease takes: "pdsp".
const Namespace int32 = 0x70647370

// The defaults [New] is given unless told otherwise.
const (
	// DefaultRetry is how long a process that does not hold a name waits
	// before it asks again: how long a loop can go unrun after the process that
	// held it let it go.
	DefaultRetry = 5 * time.Second

	// DefaultCheck is how often a process that holds a name asks whether its
	// connection, and so its locks, are still there: how long it can go on
	// after losing them.
	DefaultCheck = 5 * time.Second
)

// dialWithin is how long a connection may take to be made before the database
// is taken for one that cannot be asked just now.
const dialWithin = 10 * time.Second

// Lease is a [spin.Lease] whose holds are advisory locks on one connection.
type Lease struct {
	cfg   *pgx.ConnConfig
	log   *slog.Logger
	retry time.Duration
	check time.Duration
	dial  time.Duration

	mu       sync.Mutex
	conn     *pgx.Conn
	holds    map[int32]*hold
	names    map[int32]string
	users    int
	checking bool

	// dialing is closed when the connection being made is made, or is not;
	// nil while nobody is making one. dialed is how the database refused the
	// last one, for whoever waited on it.
	dialing chan struct{}
	dialed  error
}

type hold struct {
	stop context.CancelFunc
	conn *pgx.Conn
}

var _ spin.Lease = (*Lease)(nil)

// Option is something [New] is told besides where to connect.
type Option func(*Lease)

// Retry is how long a process that does not hold a name waits before it asks
// again; [DefaultRetry] otherwise.
func Retry(d time.Duration) Option { return func(l *Lease) { l.retry = d } }

// Check is how often a process that holds a name pings its connection;
// [DefaultCheck] otherwise.
func Check(d time.Duration) Option { return func(l *Lease) { l.check = d } }

// New answers with a lease on the database at `dsn`. It connects once to check,
// as a broker does, so that a lease that cannot reach its database says so when
// a server is built rather than at its first hold.
//
// What it says later -- which names a lost connection took with it -- it says
// in the log `ctx` carries, which is the process's when the serve command
// builds it; the lease outlives `ctx` being done.
func New(ctx context.Context, dsn string, opts ...Option) (*Lease, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("spinpg: %w", err)
	}
	for k, v := range map[string]string{
		"tcp_keepalives_idle":     "30",
		"tcp_keepalives_interval": "10",
		"tcp_keepalives_count":    "3",
		"application_name":        "payday.spin",
	} {
		if _, ok := cfg.RuntimeParams[k]; !ok {
			cfg.RuntimeParams[k] = v
		}
	}

	l := &Lease{
		cfg:   cfg,
		log:   log.From(context.WithoutCancel(ctx)),
		retry: DefaultRetry,
		check: DefaultCheck,
		dial:  dialWithin,
		holds: map[int32]*hold{},
		names: map[int32]string{},
	}
	for _, opt := range opts {
		opt(l)
	}

	d, cancel := context.WithTimeout(ctx, l.dial)
	defer cancel()
	c, err := pgx.ConnectConfig(d, cfg.Copy())
	if err != nil {
		return nil, fmt.Errorf("spinpg: %w", err)
	}
	_ = c.Close(d)

	return l, nil
}

// Dialect refuses a database a lease cannot ride: these are PostgreSQL's
// locks.
func Dialect(v string) error {
	if v == "postgres" {
		return nil
	}

	return fmt.Errorf("spinpg: this holds PostgreSQL's advisory locks and the database is %s;"+
		" a deployment on %s of more than one process needs a lease that is not the database", v, strings.ToLower(v))
}

func keyOf(name string) int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return int32(h.Sum32())
}

// Hold answers once this process holds `name`: asking every [Retry] while
// another process holds it, and while the database cannot be asked at all.
func (l *Lease) Hold(ctx context.Context, name string) (context.Context, func(), error) {
	key := keyOf(name)
	if err := l.claim(key, name); err != nil {
		return nil, nil, err
	}

	l.mu.Lock()
	l.users++
	l.mu.Unlock()

	unreachable := false
	for {
		held, release, err := l.try(ctx, key)
		if held != nil {
			return held, release, nil
		}
		if err != nil && ctx.Err() == nil && !unreachable {
			// Said once, and not every retry: a database that is down is
			// a thing to say and then wait out. Not said of a dial the
			// process stopped by shutting down, which is no news about the
			// database.
			unreachable = true
			log.From(ctx).WarnContext(ctx, "spinpg: the lease's database cannot be asked; asking again",
				slog.String("spin", name), slog.Any("err", err))
		}

		select {
		case <-ctx.Done():
			l.mu.Lock()
			l.leave()
			l.mu.Unlock()
			return nil, nil, ctx.Err()
		case <-time.After(l.retry):
		}
	}
}

// claim refuses two names that would be one lock.
func (l *Lease) claim(key int32, name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if v, ok := l.names[key]; ok && v != name {
		return fmt.Errorf("spinpg: %q and %q are one lock between them; rename one", v, name)
	}
	l.names[key] = name
	return nil
}

// try asks for `key` once, and answers nothing while somebody else holds it.
func (l *Lease) try(ctx context.Context, key int32) (context.Context, func(), error) {
	if err := l.connected(ctx); err != nil {
		return nil, nil, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, mine := l.holds[key]; mine {
		// This process holds it already, for another loop of the name: a
		// session lock is the session's and would be taken twice, so this one
		// waits as another process would.
		return nil, nil, nil
	}
	if l.conn == nil {
		// Lost between being made and here. Asked again after a retry, on
		// whatever connection there is then.
		return nil, nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	// Not the caller's context: a query canceled part way is a connection
	// closed, and every other hold on it with it.
	q, cancel := context.WithTimeout(context.Background(), l.check)
	defer cancel()
	var ok bool
	if err := l.conn.QueryRow(q, "SELECT pg_try_advisory_lock($1, $2)", Namespace, key).Scan(&ok); err != nil {
		l.lose(err)
		return nil, nil, err
	}
	if !ok {
		return nil, nil, nil
	}

	held, stop := context.WithCancel(ctx)
	h := &hold{stop: stop, conn: l.conn}
	l.holds[key] = h
	l.watch()

	var once sync.Once
	return held, func() { once.Do(func() { l.release(key, h) }) }, nil
}

// connected makes sure there is a connection to ask on.
//
// The dial is outside the lock and under the context of the loop that asked,
// because a dial is the one thing here that can take seconds: a database that
// does not answer takes ten of them to give up on, and made under the lock that
// was every waiting loop in turn, each waiting on the one before it, and a
// shutdown that took as long as all of them together. Not a query: a dial
// canceled part way is a connection that was never made, which nothing holds
// anything on.
//
// One at a time. The others wait for it -- or for their own context -- and take
// what it made, or how the database refused it, as their own try. A dial given
// up because the loop making it stopped says nothing about the database, so
// whoever was waiting makes one of its own.
func (l *Lease) connected(ctx context.Context) error {
	l.mu.Lock()
	for {
		if l.conn != nil && l.conn.IsClosed() {
			// Closed under it, which a failed query or ping says rather than
			// leaves to be found; said here so that nothing held on it is
			// believed a moment longer.
			l.lose(errClosed)
		}
		if l.conn != nil {
			l.mu.Unlock()
			return nil
		}

		wait := l.dialing
		if wait == nil {
			break
		}
		l.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}

		l.mu.Lock()
		if l.conn == nil && l.dialed != nil {
			err := l.dialed
			l.mu.Unlock()
			return err
		}
	}

	wait := make(chan struct{})
	l.dialing = wait
	l.mu.Unlock()

	d, cancel := context.WithTimeout(ctx, l.dial)
	c, err := pgx.ConnectConfig(d, l.cfg.Copy())
	cancel()

	l.mu.Lock()
	defer l.mu.Unlock()
	l.dialing = nil
	close(wait)
	switch {
	case err == nil:
		l.conn, l.dialed = c, nil
		return nil

	case ctx.Err() != nil:
		l.dialed = nil
		return ctx.Err()

	default:
		l.dialed = err
		return err
	}
}

// errClosed is a connection found closed before anything said so.
var errClosed = errors.New("spinpg: the connection closed")

func (l *Lease) release(key int32, h *hold) {
	l.mu.Lock()
	defer l.mu.Unlock()

	h.stop()
	if l.holds[key] == h {
		delete(l.holds, key)
		// On the connection that took it, and only while it is the one this
		// process has: a lock on a connection that went went with it.
		if l.conn != nil && l.conn == h.conn {
			q, cancel := context.WithTimeout(context.Background(), l.check)
			_, err := l.conn.Exec(q, "SELECT pg_advisory_unlock($1, $2)", Namespace, key)
			cancel()
			if err != nil {
				l.lose(err)
			}
		}
	}
	l.leave()
}

// leave is a hold given up, or a wait that ended without one. With nothing held
// or waited for there is nothing to keep a connection for.
func (l *Lease) leave() {
	l.users--
	if l.users > 0 || l.conn == nil {
		return
	}
	q, cancel := context.WithTimeout(context.Background(), l.check)
	_ = l.conn.Close(q)
	cancel()
	l.conn = nil
}

// lose is the connection gone, and every hold on it: each loop that held one
// is stopped, and waits to hold it again.
func (l *Lease) lose(err error) {
	if l.conn != nil {
		q, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = l.conn.Close(q)
		cancel()
		l.conn = nil
	}
	if len(l.holds) == 0 {
		return
	}

	names := []string{}
	for k, h := range l.holds {
		h.stop()
		delete(l.holds, k)
		names = append(names, l.names[k])
	}
	sort.Strings(names)
	l.log.Warn("spinpg: the lease's connection was lost, and every name held on it with it",
		slog.String("spin.names", strings.Join(names, ",")), slog.Any("err", err))
}

// watch pings the connection for as long as anything is held on it.
func (l *Lease) watch() {
	if l.checking {
		return
	}
	l.checking = true

	go func() {
		t := time.NewTicker(l.check)
		defer t.Stop()

		for range t.C {
			l.mu.Lock()
			if len(l.holds) == 0 || l.conn == nil {
				l.checking = false
				l.mu.Unlock()
				return
			}
			q, cancel := context.WithTimeout(context.Background(), l.check)
			err := l.conn.Ping(q)
			cancel()
			if err != nil {
				l.lose(err)
				l.checking = false
				l.mu.Unlock()
				return
			}
			l.mu.Unlock()
		}
	}()
}
