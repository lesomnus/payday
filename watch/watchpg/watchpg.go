// Package watchpg is a [watch.Broker] over PostgreSql's `LISTEN`/`NOTIFY`.
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
// # What travels, and what deliberately does not
//
// The identity of what changed: the RPC, and for each write the entity's method
// and the row's key. Not the row.
//
// That is not a size compromise, though it helps. What a subscriber may see is
// decided **per subscriber**, by re-reading each row through their own
// narrowing -- see `watch.Stream` and the generated `watchRead`. Putting the
// row's content on a channel every replica reads would be answering that
// question once, in the wrong place, for everybody.
//
// `Event.Request` and `Event.Response` do not travel either, and could not:
// they are the app's own messages and mean something only in the process that
// handled the call. Nothing reads them from a subscription.
//
// # What it does when it loses the connection
//
// **Cuts every subscriber**, then reconnects. A listener that was away may have
// missed notifications, and there is no backlog to catch up from -- so quietly
// resuming would leave a stream open, healthy-looking and permanently behind,
// which is the one failure this whole seam exists to prevent. Cut, the stream
// ends with `ErrBehind`, the client asks again, and a fresh stream begins with
// everything that matches now.
package watchpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/watch"
)

// Channel is what this listens on.
//
// One name, and it needs no more: `LISTEN` is scoped to a **database**, so two
// deployments are two databases and hear nothing of each other -- which is
// already true of their rows. An app with two planes has two databases for the
// same reason and gets the same separation for free.
const Channel = "payday_watch"

// Payload is the most PostgreSql will carry in one notification.
//
// 8000 bytes is the server's limit and it is not configurable. A call that
// changed more rows than fit is sent as several notifications, which a
// subscriber cannot tell from several calls -- `watch.Next` accumulates keys
// either way.
const Payload = 8000

// Queue is how many notifications may be waiting to reach the database.
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
	if err := c.Close(ctx); err != nil {
		return nil, fmt.Errorf("watchpg: %w", err)
	}

	b := &broker{dsn: dsn, to: map[chan watch.Event]struct{}{}, out: make(chan string, Queue)}

	// Detached from the caller's context on purpose: a broker outlives the call
	// that built it, and the one that built it is a server starting up.
	go b.listen(context.WithoutCancel(ctx))
	go b.publish(context.WithoutCancel(ctx))

	return b, nil
}

type broker struct {
	dsn string

	mu sync.Mutex
	to map[chan watch.Event]struct{}

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
	out chan string
}

func (b *broker) Subscribe() (<-chan watch.Event, func()) {
	c := make(chan watch.Event, watch.Backlog)

	b.mu.Lock()
	b.to[c] = struct{}{}
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
	for _, s := range encode(v) {
		select {
		case b.out <- s:
		default:
			log.From(ctx).ErrorContext(ctx, "watchpg: a change was not published",
				"why", "the queue to the database is full",
				"method", v.Method)

			b.cut()
		}
	}
}

// publish drains the queue.
func (b *broker) publish(ctx context.Context) {
	var c *pgx.Conn

	for {
		var s string
		select {
		case <-ctx.Done():
			return
		case s = <-b.out:
		}

		if c == nil {
			v, err := pgx.Connect(ctx, b.dsn)
			if err != nil {
				log.From(ctx).ErrorContext(ctx, "watchpg: cannot reach the database to publish", "err", err)
				b.wait(ctx)

				continue
			}

			c = v
		}

		if _, err := c.Exec(ctx, "select pg_notify($1, $2)", Channel, s); err != nil {
			log.From(ctx).ErrorContext(ctx, "watchpg: a change was not published", "err", err)

			// Dropped rather than retried, and the reason is the alternative:
			// holding it means the next write queues behind an unreachable
			// database, and a `Publish` that blocks is a server that stops
			// answering. What is done instead is the same thing the listener
			// does when it notices the outage from its side -- cut, so that
			// whoever is watching here starts again from a snapshot rather than
			// from a hole they cannot see.
			b.cut()

			_ = c.Close(ctx)
			c = nil
		}
	}
}

// listen holds the connection everything arrives on.
func (b *broker) listen(ctx context.Context) {
	for ctx.Err() == nil {
		if err := b.once(ctx); err != nil && ctx.Err() == nil {
			log.From(ctx).WarnContext(ctx, "watchpg: listening stopped", "err", err)
		}

		// Whatever ended it, every subscriber has a hole in what it was told
		// and no way to find out how big. See the note at the top of this file.
		b.cut()
		b.wait(ctx)
	}
}

func (b *broker) once(ctx context.Context) error {
	c, err := pgx.Connect(ctx, b.dsn)
	if err != nil {
		return err
	}
	defer c.Close(context.WithoutCancel(ctx))

	if _, err := c.Exec(ctx, "listen "+Channel); err != nil {
		return err
	}

	for {
		n, err := c.WaitForNotification(ctx)
		if err != nil {
			return err
		}

		v, err := decode(n.Payload)
		if err != nil {
			// A payload this cannot read is a deployment running two versions
			// of payday against one database. Said and skipped: the alternative
			// is cutting every subscriber on every notification, which turns a
			// rolling deploy into an outage.
			log.From(ctx).WarnContext(ctx, "watchpg: a notification could not be read", "err", err)

			continue
		}

		b.fan(v)
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

// wire is one notification.
//
// JSON because a notification is a **string** and this one is read by a person
// as often as by a program: `LISTEN payday_watch` in psql is how somebody finds
// out whether the thing they just did published anything.
type wire struct {
	Actor   string   `json:"a,omitempty"`
	Tenant  string   `json:"t,omitempty"`
	Method  string   `json:"m"`
	Changes []change `json:"c"`
}

type change struct {
	By  string `json:"b"`
	Key string `json:"k"`
}

// encode is the event as notifications, split so each one fits.
//
// A call that changed more rows than [Payload] holds becomes several, which a
// subscriber cannot tell from several calls: `watch.Next` gathers keys until
// there is nothing queued and answers with the set.
func encode(v watch.Event) []string {
	cs := make([]change, 0, len(v.Changes))
	for _, c := range v.Changes {
		cs = append(cs, change{By: c.By, Key: c.Key.String()})
	}
	if len(cs) == 0 {
		// A read is most of what a server does and is not news.
		return nil
	}

	head := wire{Method: v.Method}
	if v.Actor != pdid.Nil {
		head.Actor = v.Actor.String()
	}
	if v.Tenant != pdid.Nil {
		head.Tenant = v.Tenant.String()
	}

	var out []string
	for len(cs) > 0 {
		n := len(cs)
		for {
			w := head
			w.Changes = cs[:n]

			b, err := json.Marshal(w)
			if err != nil {
				return out
			}
			if len(b) <= Payload {
				out = append(out, string(b))
				break
			}
			if n == 1 {
				// One change that does not fit, which takes a method name of
				// several kilobytes. Nothing to split, and dropping it silently
				// is the one thing this package will not do.
				return out
			}

			n /= 2
		}

		cs = cs[n:]
	}

	return out
}

func decode(s string) (watch.Event, error) {
	var w wire
	if err := json.Unmarshal([]byte(s), &w); err != nil {
		return watch.Event{}, err
	}

	v := watch.Event{Method: w.Method, Changes: make([]watch.Change, 0, len(w.Changes))}
	for _, c := range w.Changes {
		k, err := pdid.Parse(c.Key)
		if err != nil {
			return watch.Event{}, fmt.Errorf("key %q: %w", c.Key, err)
		}

		v.Changes = append(v.Changes, watch.Change{By: c.By, Method: w.Method, Key: k})
	}

	for _, p := range []struct {
		s string
		v *pdid.Id
	}{{w.Actor, &v.Actor}, {w.Tenant, &v.Tenant}} {
		if p.s == "" {
			continue
		}

		k, err := pdid.Parse(p.s)
		if err != nil {
			return watch.Event{}, fmt.Errorf("%w", err)
		}

		*p.v = k
	}

	return v, nil
}

// Dialect refuses a database this cannot listen on, by the name a driver was
// registered under.
func Dialect(v string) error {
	if v == "postgres" {
		return nil
	}

	return fmt.Errorf("watchpg: this listens on PostgreSql and the database is %s;"+
		" a deployment on %s has no broker in this binary and needs one that is not the database",
		v, strings.ToLower(v))
}
