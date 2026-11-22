// Package watchnats routes live payday changes through Core NATS ordinary
// publish/subscribe. Every interested replica receives a copy; no queue group
// competes with another replica for a client's notification.
//
// New requires a deployment namespace shared by its replicas. It owns its NATS
// connection; Close releases it. Authentication and TLS can be supplied as NATS
// options. Lifecycle handlers and reconnect buffering are owned by this adapter.
// The configured server must allow the namespace's subjects, and max_payload
// must be at least 1048 bytes. Large changes are fragmented with bounded memory
// and a thirty-second incomplete-publication deadline.
//
// Open installs reference-counted native interests and flushes the subscription
// before returning. This is a barrier on the connected server, not a global
// cluster convergence or delivery acknowledgement. Disconnect, slow consumer,
// malformed payload or missing fragment closes the affected local epoch.
// Reconnecting clients reconcile state; they never resume an old live stream.
//
// Publish enqueues without waiting for the server, bounded by 256 calls and
// 32 MiB of encoded traffic. PublishConfirmed waits for a server flush, suitable
// for outbox retry. Core NATS stores no replay and neither operation proves
// client consumption. Requests, responses and mutation patches never travel.
package watchnats

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/watch"
	"github.com/lesomnus/payday/watch/internal/wire"
	"github.com/nats-io/nats.go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const timeout = 5 * time.Second
const queueSize = 256

type binding struct {
	sub     *nats.Subscription
	readers int
}
type epoch struct {
	hub      watch.Fanout
	bindings map[string]*binding
	in       chan *nats.Msg
	done     chan struct{}
	readers  int
}
type batch struct {
	publications []publication
	size         int
}

// Broker owns the connection and local subscriptions. Use New; its zero value
// is not connected. Configuration registers it through config/brokernats.
type Broker struct {
	conn       *nats.Conn
	prefix     string
	ctx        context.Context
	mu         sync.Mutex
	current    *epoch
	closed     bool
	queued     int
	out        chan batch
	done       chan struct{}
	publishing chan struct{}
}

var _ watch.Broker = (*Broker)(nil)
var _ watch.ScopedBroker = (*Broker)(nil)
var _ watch.ConfirmedBroker = (*Broker)(nil)

func New(ctx context.Context, dsn, namespace string, options ...nats.Option) (*Broker, error) {
	if dsn == "" || namespace == "" {
		return nil, errors.New("watchnats: dsn and deployment namespace are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := &Broker{prefix: "payday.w1." + token(namespace), ctx: context.WithoutCancel(ctx), out: make(chan batch, queueSize), done: make(chan struct{}), publishing: make(chan struct{}, 1)}
	// Do not let an opaque reconnect buffer acknowledge an outbox publication.
	// Options are last for the handlers this adapter must own; TLS/auth and other
	// connection settings remain the application's choice.
	dialTimeout := timeout
	if deadline, ok := ctx.Deadline(); ok {
		dialTimeout = min(dialTimeout, time.Until(deadline))
	}
	options = append(options, nats.Name("payday-watch"), nats.Timeout(dialTimeout), nats.MaxReconnects(-1), nats.ReconnectWait(250*time.Millisecond), nats.ReconnectBufSize(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { b.fail(err) }),
		nats.ClosedHandler(func(_ *nats.Conn) { b.fail(nats.ErrConnectionClosed) }),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { b.fail(err) }))
	conn, err := nats.Connect(dsn, options...)
	if err != nil {
		return nil, fmt.Errorf("watchnats: %w", err)
	}
	b.conn = conn
	if conn.MaxPayload() < 1024+frameHeader {
		conn.Close()
		return nil, errors.New("watchnats: max_payload must be at least 1048 bytes")
	}
	if err := b.flush(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	go b.sender()
	return b, nil
}
func (b *Broker) flush(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := b.conn.FlushWithContext(ctx); err != nil {
		return err
	}
	// Permission errors are parsed before the subsequent PONG, while the async
	// callback can run later. Do not emit READY or acknowledge an outbox row then.
	if err := b.conn.LastError(); err != nil && !errors.Is(err, nats.ErrSlowConsumer) {
		return err
	}
	if !b.conn.IsConnected() {
		return nats.ErrDisconnected
	}
	return nil
}
func (b *Broker) Open(ctx context.Context, interest watch.Interest) (subscription *watch.Subscription, err error) {
	// Broker configuration and connection errors are operational failures, not a
	// revocation of the app reader's access. Keep them retryable on the RPC wire.
	defer func() {
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			err = status.FromContextError(ctx.Err()).Err()
			return
		}
		if status.Code(err) == codes.Unknown {
			err = status.Errorf(codes.Unavailable, "watchnats: cannot install subscription: %v", err)
		}
	}()

	patterns, err := b.patterns(interest)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	e, local, err := b.install(ctx, interest, patterns)
	if err != nil {
		return nil, err
	}
	// Never hold the dispatch/queue mutex while waiting on network I/O. Publish
	// is called after a business commit and must remain independent of readiness.
	err = b.flush(ctx)
	b.mu.Lock()
	if err == nil && (b.current != e || b.closed || !b.conn.IsConnected()) {
		err = watch.ErrBehind
	}
	if err != nil {
		local.Close()
		e.readers--
		b.release(e, patterns)
		b.mu.Unlock()
		return nil, err
	}
	b.mu.Unlock()
	return watch.NewSubscription(local.Events, func() {
		local.Close()
		b.mu.Lock()
		defer b.mu.Unlock()
		e.readers--
		b.release(e, patterns)
	}), nil
}

func (b *Broker) install(ctx context.Context, interest watch.Interest, patterns []string) (*epoch, *watch.Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || !b.conn.IsConnected() {
		return nil, nil, nats.ErrDisconnected
	}
	e := b.current
	if e == nil {
		e = &epoch{bindings: make(map[string]*binding), in: make(chan *nats.Msg, queueSize), done: make(chan struct{})}
		b.current = e
		go b.receive(e)
	}
	needed := 0
	for _, p := range patterns {
		if e.bindings[p] == nil {
			needed++
		}
	}
	if len(e.bindings)+needed > maxBindings {
		if e.readers == 0 {
			b.cut(e)
		}
		return nil, nil, status.Error(codes.ResourceExhausted, "watchnats: native subscription budget exhausted")
	}
	local, err := e.hub.Open(ctx, interest)
	if err != nil {
		if e.readers == 0 {
			b.cut(e)
		}
		return nil, nil, err
	}
	installed := make([]string, 0, len(patterns))
	for _, p := range patterns {
		v := e.bindings[p]
		if v == nil {
			sub, err := b.conn.ChanSubscribe(p, e.in)
			if err != nil {
				local.Close()
				b.release(e, installed)
				return nil, nil, err
			}
			v = &binding{sub: sub}
			e.bindings[p] = v
		}
		v.readers++
		installed = append(installed, p)
	}
	// A pending Open owns a reference as well. Closing the previous last reader
	// cannot tear down this registration while its flush is in progress.
	e.readers++
	return e, local, nil
}

// release and cut hold mu. An old epoch's Close must not change a newer one.
func (b *Broker) release(e *epoch, patterns []string) {
	for _, p := range patterns {
		if v := e.bindings[p]; v != nil {
			v.readers--
			if v.readers == 0 {
				_ = v.sub.Unsubscribe()
				delete(e.bindings, p)
			}
		}
	}
	if e.readers == 0 && b.current == e {
		b.cut(e)
	}
}
func (b *Broker) cut(e *epoch) {
	if b.current != e {
		return
	}
	b.current = nil
	for _, v := range e.bindings {
		_ = v.sub.Unsubscribe()
	}
	clear(e.bindings)
	e.hub.Close()
	close(e.done)
}
func (b *Broker) fail(err error) {
	b.mu.Lock()
	if b.current != nil {
		b.cut(b.current)
	}
	b.mu.Unlock()
	if err != nil {
		log.From(b.ctx).WarnContext(b.ctx, "watchnats: live subscriptions interrupted", "err", err)
	}
}
func (b *Broker) receive(e *epoch) {
	var decoder receiver
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-e.done:
			return
		case now := <-timer.C:
			if !decoder.expired(now) {
				continue
			}
			b.mu.Lock()
			b.cut(e)
			b.mu.Unlock()
			return
		case m := <-e.in:
			event, err := decoder.accept(m.Data)
			if err != nil {
				b.mu.Lock()
				b.cut(e)
				b.mu.Unlock()
				log.From(b.ctx).ErrorContext(b.ctx, "watchnats: invalid publication", "err", err)
				return
			}
			if event != nil {
				e.hub.Publish(*event)
			}
		}
	}
}
func (b *Broker) Subscribe() (<-chan watch.Event, func()) {
	s, err := b.Open(context.Background(), watch.Interest{})
	if err != nil {
		c := make(chan watch.Event)
		close(c)
		return c, func() {}
	}
	return s.Events, s.Close
}
func (b *Broker) Publish(ctx context.Context, v watch.Event) {
	packets, size, err := b.encode(v)
	if err != nil {
		b.fail(err)
		return
	}
	if len(packets) == 0 {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	if size > wire.MaxWire-b.queued {
		b.mu.Unlock()
		b.fail(errors.New("publication queue byte budget exhausted"))
		return
	}
	select {
	case b.out <- batch{publications: packets, size: size}:
		b.queued += size
		b.mu.Unlock()
	default:
		b.mu.Unlock()
		b.fail(errors.New("publication queue is full"))
	}
}
func (b *Broker) PublishConfirmed(ctx context.Context, v watch.Event) error {
	packets, _, err := b.encode(v)
	if err != nil {
		return err
	}
	return b.send(ctx, packets)
}
func (b *Broker) send(ctx context.Context, packets []publication) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.done:
		return nats.ErrConnectionClosed
	case b.publishing <- struct{}{}:
	}
	defer func() { <-b.publishing }()
	if !b.conn.IsConnected() {
		return nats.ErrDisconnected
	}
	for _, p := range packets {
		for _, subject := range p.subjects {
			for _, data := range p.frames {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := b.conn.Publish(subject, data); err != nil {
					return err
				}
			}
		}
	}
	return b.flush(ctx)
}
func (b *Broker) sender() {
	for {
		select {
		case <-b.done:
			return
		case v := <-b.out:
			err := b.send(context.Background(), v.publications)
			b.mu.Lock()
			b.queued -= v.size
			b.mu.Unlock()
			if err != nil {
				b.fail(err)
			}
		}
	}
}
func (b *Broker) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	if b.current != nil {
		b.cut(b.current)
	}
	close(b.done)
	b.mu.Unlock()
	b.conn.Close()
	return nil
}
