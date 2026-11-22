package watch

import (
	"context"
	"sync"
)

// Open installs a scoped listener. Existing brokers are shared through one
// broadcast subscription while any local listener remains. This saves local
// interpretation work even when the transport cannot save network traffic.
func (w *Watch) Open(ctx context.Context, interest Interest) (*Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if w == nil || w.to == nil {
		return nil, ErrNoBroker
	}
	if b, ok := w.to.(ScopedBroker); ok {
		return b.Open(ctx, interest)
	}
	w.mu.Lock()
	b := w.local
	if b == nil {
		events, stop := w.to.Subscribe()
		b = &broadcast{events: events, done: make(chan struct{}), stop: sync.OnceFunc(stop)}
		w.local = b
		go w.relay(b)
	}
	s, err := b.hub.Open(ctx, interest)
	if err == nil {
		b.readers++
	}
	unused := err != nil && b.readers == 0 && w.local == b
	if unused {
		w.local = nil
		close(b.done)
		b.hub.Close()
	}
	w.mu.Unlock()
	if err != nil {
		if unused {
			b.stop()
		}
		return nil, err
	}
	return NewSubscription(s.Events, func() {
		s.Close()
		w.mu.Lock()
		b.readers--
		last := b.readers == 0 && w.local == b
		if last {
			w.local = nil
			close(b.done)
			b.hub.Close()
		}
		w.mu.Unlock()
		if last {
			b.stop()
		}
	}), nil
}

type broadcast struct {
	events  <-chan Event
	done    chan struct{}
	stop    func()
	hub     Fanout
	readers int
}

func (w *Watch) relay(b *broadcast) {
	defer b.stop()
	for {
		select {
		case <-b.done:
			return
		case event, ok := <-b.events:
			if !ok {
				w.mu.Lock()
				if w.local == b {
					w.local = nil
				}
				b.hub.Close()
				w.mu.Unlock()
				return
			}
			b.hub.Publish(event)
		}
	}
}

// Close releases local fallback subscriptions and a broker that owns external
// resources. The built server calls it at shutdown and on failed construction.
func (w *Watch) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	b := w.local
	if b != nil {
		w.local = nil
		close(b.done)
		b.hub.Close()
	}
	w.mu.Unlock()
	if b != nil {
		b.stop()
	}
	if closer, ok := w.to.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}
