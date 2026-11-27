// Package watchpg is a [watch.Broker] over PostgreSQL's `LISTEN`/`NOTIFY`.
//
// # Why this one first
//
// Because it is the one that needs nothing. An app that keeps its rows in
// Postgres already has a Postgres, and `NOTIFY` is a statement it can already
// issue -- so the difference between one replica and several stops being a
// piece of infrastructure to stand up and becomes a line of configuration.
// Every other broker worth writing is a message bus somebody has to run.
//
// It is **the rows' own database** and not a second one. There is nothing to
// store: a notification is delivered to whoever is listening at the time and
// then forgotten, which is exactly the guarantee `watch` already gives.
//
// # What travels
//
// Legacy key/method notifications and optional public mutation envelopes travel
// between replicas. Resource envelopes have secrets removed before publication;
// each subscriber's read authorization and CEL filter are applied after receipt.
// Large envelopes are fragmented and reassembled with bounded memory and time.
// Request and Response do not travel.
//
// # What it does when it loses the connection
//
// **Cuts every subscriber**, then reconnects. A listener that was away may have
// missed notifications, and there is no backlog to catch up from -- so quietly
// resuming would leave a stream open, healthy-looking and permanently behind,
// which is the one failure this whole seam exists to prevent. Cut, the stream
// ends with `ErrBehind`, the client asks again, and a fresh stream begins with
// everything that matches now.
//
// # How it stops
//
// By being closed, and by nothing else. A broker holds two connections of its
// own -- the one it listens on and the one it publishes on, neither from the
// app's pool -- and a goroutine for each, and the listener dials again whenever
// its connection ends: killing the connection from outside brings it back a
// second later. So a broker lives from [New] until its `Close`, which
// `(*watch.Watch).Close` calls, and an app's server closes its Watch. In a
// deployment that builds one server that is the process's lifetime and nothing
// changes; in anything that builds a server more than once in a process -- a
// test suite, an app that embeds another, a reload -- a broker nobody closed is
// two connections and two goroutines that stay until the process exits, and a
// suite of a few dozen servers takes every connection a default PostgreSQL has.
package watchpg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/watch"
)

// Channel is what this listens on.
//
// One name, and it needs no more: `LISTEN` is scoped to a **database**, so two
// deployments are two databases and hear nothing of each other -- which is
// already true of their rows. An app with two planes has two databases for the
// same reason and gets the same separation for free.
const Channel = "payday_watch"

// Payload is the most PostgreSQL will carry in one notification.
//
// 8000 bytes is the server's limit and it is not configurable. A call that
// changed more rows than fit is sent as several notifications, which a
// subscriber cannot tell from several calls -- `watch.Next` accumulates keys
// either way.
const Payload = 8000

// Queue is how many publications may be waiting to reach the database.
// The aggregate encoded queue is additionally bounded by MaxWire bytes.
//
// Its own number and not [watch.Backlog], which is a **subscriber's** patience.
// This one is the deployment's write rate against one round trip per event on
// an open connection, and that round trip is cheaper than the transaction that
// produced the event -- so a full queue does not mean "busy", it means the
// database is not answering. Generous enough that an ordinary spike never
// reaches it, and bounded because the alternative to dropping is a server that
// stops answering.
const Queue = 1 << 12

// Backoff is how long a listener waits before dialing again.
//
// Short, because what is lost while it is away is every event: a subscriber
// that reconnects gets a snapshot, and a deployment where that happens often is
// one doing full reads it did not ask for.
const Backoff = time.Second

// CloseWithin is how long `Close` waits for the listener and the publisher to
// be gone, and how long either is given to say goodbye to the server on its
// way out.
//
// Both end as soon as they are told, since everything they wait on waits on
// the broker's context; what can take longer is a connection to a server that
// stopped answering, and a shutdown should not hang on that. Past it, `Close`
// logs a warning on the context [New] was given ("watchpg: closed, and has not
// stopped") and returns: the goroutines have been told and end when their
// connection does.
const CloseWithin = 5 * time.Second

// ErrClosed is what a publication through a closed broker is answered.
//
// [watch.Broker]'s `Publish` answers nothing, so there it is logged, at ERROR,
// as a change that was not published: a server that still writes after its
// broker was closed is a shutdown in the wrong order, and an event nobody
// will hear. `PublishConfirmed`, which the outbox drain uses, returns it, and
// the row stays for whoever drains next.
var ErrClosed = errors.New("watchpg: the broker is closed")

// New answers with a broker publishing on `dsn`.
//
// It dials once here so that a deployment naming a database it cannot reach
// finds out at startup, in the process that could not start, rather than from
// a `Watch` that opens and never speaks.
func New(ctx context.Context, dsn string) (watch.Broker, error) {
	if dsn == "" {
		return nil, errors.New("watchpg: no dsn: this broker listens on the database the rows are in, so it needs to be told which")
	}

	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("watchpg: %w", err)
	}
	if _, err := c.Exec(ctx, "listen "+Channel); err != nil {
		_ = c.Close(ctx)
		return nil, fmt.Errorf("watchpg: %w", err)
	}

	// Detached from the caller's cancellation on purpose: a broker outlives the
	// call that built it, and the one that built it is a server starting up.
	// What is kept of that context is its values, and they are the point: the
	// log a listener that stopped or a change that was not published is said
	// in is the process's, carried there (see config.RegisterBroker). Its end
	// is [broker.Close] now and not the process's.
	ctx, stop := context.WithCancel(context.WithoutCancel(ctx))

	b := &broker{dsn: dsn, ctx: ctx, stop: stop, to: map[chan watch.Event]struct{}{}, out: make(chan []string, Queue), ready: true}

	b.running.Add(2)
	go func() {
		defer b.running.Done()
		b.listen(ctx, c)
	}()
	go func() {
		defer b.running.Done()
		b.publish(ctx)
	}()

	return b, nil
}

type broker struct {
	dsn string

	// ctx is what both goroutines run on, and stop ends it. Kept for `Close`
	// to log on.
	ctx     context.Context
	stop    context.CancelFunc
	running sync.WaitGroup

	mu     sync.Mutex
	to     map[chan watch.Event]struct{}
	ready  bool
	closed bool

	// out is what has been published and not yet notified.
	//
	// Buffered and drained by a goroutine, because `Publish` must not block the
	// call that produced it and a `NOTIFY` is a round trip to another process.
	//
	// Full, or failing, means the database is not answering -- and an event
	// that never reached it is one nobody hears. So it is said out loud **and**
	// every subscriber on this replica is cut, which is the only recovery
	// available from here: they ask again and a fresh stream begins with
	// everything that matches now. Subscribers on other replicas are the
	// listener half's problem, and it cuts its own for the same reason.
	//
	// A deployment that cannot lose an event turns on `watch.outbox`, which
	// writes it as a row inside the transaction that made it. With this broker
	// that composes properly: the drain publishes here, so it crosses replicas
	// -- which it does not with `memory`.
	out    chan []string
	queued int
}

func (b *broker) Subscribe() (<-chan watch.Event, func()) {
	c := make(chan watch.Event, watch.Backlog)

	b.mu.Lock()
	if b.ready && !b.closed {
		b.to[c] = struct{}{}
	} else {
		close(c)
	}
	b.mu.Unlock()

	var once sync.Once

	return c, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()

			if _, ok := b.to[c]; ok {
				delete(b.to, c)
				close(c)
			}
		})
	}
}

// Publish sends the event to every replica, this one included.
//
// Including this one, which is worth saying: there is no local shortcut. One
// path means a subscriber here and a subscriber elsewhere see the same thing in
// the same order, and it means the round trip is exercised by every deployment
// rather than only by the ones with two replicas.
func (b *broker) Publish(ctx context.Context, v watch.Event) {
	packets, err := packets(v)
	if err != nil {
		log.From(ctx).ErrorContext(ctx, "watchpg: cannot encode event", "err", err)
		b.cut()
		return
	}
	size := 0
	for _, p := range packets {
		size += len(p)
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		log.From(ctx).ErrorContext(ctx, "watchpg: a change was not published", "err", ErrClosed)

		return
	}
	accepted := false
	if b.queued+size <= MaxWire {
		select {
		case b.out <- packets:
			b.queued += size
			accepted = true
		default:
		}
	}
	b.mu.Unlock()
	if !accepted {
		log.From(ctx).ErrorContext(ctx, "watchpg: publication queue is full")
		b.cut()
	}
}

// PublishConfirmed waits for PostgreSQL to commit all notification fragments. It
// acknowledges broker acceptance, not subscriber receipt. Outbox drain uses this
// so a failed send leaves the row available for retry.
func (b *broker) PublishConfirmed(ctx context.Context, v watch.Event) error {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return ErrClosed
	}

	packets, err := packets(v)
	if err != nil {
		return err
	}
	c, err := pgx.Connect(ctx, b.dsn)
	if err != nil {
		return err
	}
	defer c.Close(context.WithoutCancel(ctx))
	return send(ctx, c, packets)
}

func send(ctx context.Context, c *pgx.Conn, packets []string) error {
	tx, err := c.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	for _, p := range packets {
		if _, err := tx.Exec(ctx, "select pg_notify($1, $2)", Channel, p); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Close ends the broker: it refuses what is published and subscribed from
// here on, tells both goroutines to stop, cuts every subscriber, and waits --
// for [CloseWithin] at most -- for the listener and the publisher to be gone,
// each closing its connection on the way out. Twice is nothing.
//
// A subscriber is cut exactly as it is when the listening connection is lost:
// its channel is closed, and a Watch on it ends with `watch.ErrBehind`. A
// subscription asked for afterwards is closed as it is made, which is what a
// listener that never came back gives too. An app's Watch RPCs have ended by
// then, with the server that served them, which stops before it closes its
// Watch.
//
// What was published and not yet sent when it closes is not sent, and is
// logged as such: a deployment that cannot lose an event has `watch.outbox`.
func (b *broker) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()

		return nil
	}
	b.closed = true
	b.ready = false
	b.mu.Unlock()

	b.stop()
	b.cut()

	done := make(chan struct{})
	go func() {
		b.running.Wait()
		close(done)
	}()

	t := time.NewTimer(CloseWithin)
	defer t.Stop()

	select {
	case <-done:
	case <-t.C:
		log.From(b.ctx).WarnContext(b.ctx, "watchpg: closed, and has not stopped", "within", CloseWithin)
	}

	return nil
}

// hangUp closes `c` on the way out, whether or not `ctx` has ended, and for
// [CloseWithin] at most: saying goodbye to a server that stopped answering is
// not worth a shutdown that hangs.
func hangUp(ctx context.Context, c *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CloseWithin)
	defer cancel()

	_ = c.Close(ctx)
}

// publish drains the queue.
func (b *broker) publish(ctx context.Context) {
	var c *pgx.Conn
	// lost is the publication in hand when the broker was closed.
	lost := 0
	defer func() {
		if c != nil {
			hangUp(ctx, c)
		}
		if n := lost + len(b.out); n > 0 {
			log.From(ctx).WarnContext(ctx, "watchpg: closed with changes not yet published", "count", n)
		}
	}()

	for {
		var packets []string
		select {
		case <-ctx.Done():
			return
		case packets = <-b.out:
		}

		size := 0
		for _, p := range packets {
			size += len(p)
		}
		b.mu.Lock()
		b.queued -= size
		b.mu.Unlock()

		if c == nil {
			v, err := pgx.Connect(ctx, b.dsn)
			if err != nil {
				if ctx.Err() != nil {
					lost = 1
					return
				}
				log.From(ctx).ErrorContext(ctx, "watchpg: cannot reach the database to publish", "err", err)
				b.cut()
				b.wait(ctx)

				continue
			}

			c = v
		}

		if err := send(ctx, c, packets); err != nil {
			if ctx.Err() != nil {
				lost = 1
				return
			}
			log.From(ctx).ErrorContext(ctx, "watchpg: a change was not published", "err", err)

			// Dropped rather than retried, and the reason is the alternative:
			// holding it means the next write queues behind an unreachable
			// database, and a `Publish` that blocks is a server that stops
			// answering. What is done instead is the same thing the listener
			// does when it notices the outage from its side -- cut, so that
			// whoever is watching here starts again from a snapshot rather than
			// from a hole they cannot see.
			b.cut()

			hangUp(ctx, c)
			c = nil
		}
	}
}

// listen holds the connection everything arrives on.
func (b *broker) listen(ctx context.Context, first *pgx.Conn) {
	for ctx.Err() == nil {
		if err := b.once(ctx, first); err != nil && ctx.Err() == nil {
			log.From(ctx).WarnContext(ctx, "watchpg: listening stopped", "err", err)
		}

		first = nil
		b.mu.Lock()
		b.ready = false
		b.mu.Unlock()
		// Whatever ended it, every subscriber has a hole in what it was told
		// and no way to find out how big. See the note at the top of this file.
		b.cut()
		b.wait(ctx)
	}
}

func (b *broker) once(ctx context.Context, c *pgx.Conn) error {
	if c == nil {
		var err error
		c, err = pgx.Connect(ctx, b.dsn)
		if err != nil {
			return err
		}
		if _, err := c.Exec(ctx, "listen "+Channel); err != nil {
			hangUp(ctx, c)
			return err
		}
	}
	defer hangUp(ctx, c)
	b.mu.Lock()
	b.ready = true
	b.mu.Unlock()

	var incoming receiver
	for {
		waiting := ctx
		cancel := func() {}
		if deadline, ok := incoming.deadline(); ok {
			waiting, cancel = context.WithDeadline(ctx, deadline)
		}
		n, err := c.WaitForNotification(waiting)
		cancel()
		if err != nil {
			return err
		}

		v, err := incoming.accept(n.Payload)
		if err != nil {
			return err
		}
		if v != nil {
			b.fan(*v)
		}
	}
}

// fan hands one event to every subscriber, and cuts the ones that are behind.
//
// The same three answers `watch.Memory` weighs, resolved the same way: waiting
// turns a slow consumer into a slow listener and stalls every other subscriber
// with it, and skipping in silence leaves one believing it has seen everything.
func (b *broker) fan(v watch.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for c := range b.to {
		select {
		case c <- v:
		default:
			delete(b.to, c)
			close(c)
		}
	}
}

// cut ends every subscription, so that each one starts again from a snapshot.
func (b *broker) cut() {
	b.mu.Lock()
	defer b.mu.Unlock()

	for c := range b.to {
		delete(b.to, c)
		close(c)
	}
}

func (b *broker) wait(ctx context.Context) {
	t := time.NewTimer(Backoff)
	defer t.Stop()

	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Dialect refuses a database this cannot listen on, by the name a driver was
// registered under.
func Dialect(v string) error {
	if v == "postgres" {
		return nil
	}

	return fmt.Errorf("watchpg: this listens on PostgreSQL and the database is %s;"+
		" a deployment on %s has no broker in this binary and needs one that is not the database",
		v, strings.ToLower(v))
}
