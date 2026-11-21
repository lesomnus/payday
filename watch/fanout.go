package watch

import (
	"context"
	"sync"

	"google.golang.org/grpc/status"
)

// Fanout dispatches one transport event to interested local listeners. Its zero
// value is ready. It shares interpretation, never merges distinct mutations,
// and cuts only the local listener whose buffer overflows. Close cuts all
// listeners permanently; a reconnected transport must use a fresh Fanout.
type Fanout struct {
	mu     sync.Mutex
	closed bool
	to     map[chan Event]Interest
}

func (f *Fanout) Open(ctx context.Context, interest Interest) (*Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, ErrBehind
	}
	if f.to == nil {
		f.to = make(map[chan Event]Interest)
	}
	c := make(chan Event, Backlog)
	f.to[c] = interest.clone()
	return NewSubscription(c, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.to[c]; ok {
			delete(f.to, c)
			close(c)
		}
	}), nil
}

func (f *Fanout) Publish(v Event) {
	if v.shared == nil {
		v.shared = &sync.Map{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for c, interest := range f.to {
		if !interest.Matches(v) {
			continue
		}
		select {
		case c <- v:
		default:
			delete(f.to, c)
			close(c)
		}
	}
}

func (f *Fanout) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for c := range f.to {
		delete(f.to, c)
		close(c)
	}
}
