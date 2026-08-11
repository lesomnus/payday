package authsession

import (
	"context"
	"sync"
	"time"
)

// MemStore keeps sessions in this process.
//
// It is right for one replica and **silently wrong** for two. A cookie minted
// on one is unknown to the other, so a browser is signed in or out depending on
// which one the load balancer picked -- intermittently, per request, with
// nothing in any log saying why. It is the same trap `watch`'s memory broker
// carries, and it is worth being as loud about: what makes it dangerous is that
// a single-replica deployment works perfectly, so the failure arrives on the
// day somebody scales up and looks like anything but this.
//
// It is also lost on restart, which is the honest half: everybody is signed out
// by a deploy. A real store is a table with an index on the key, or a cache
// with one.
type MemStore struct {
	mu sync.RWMutex
	vs map[string]Session
}

func NewMemStore() *MemStore { return &MemStore{vs: map[string]Session{}} }

func (s *MemStore) Put(ctx context.Context, v Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Whatever has expired, dropped while somebody is signing in. There is no
	// timer here on purpose: a goroutine per store is a thing to shut down, and
	// the map is only ever as large as the sessions made since the last one.
	for k, w := range s.vs {
		if !w.Expires.IsZero() && !time.Now().Before(w.Expires) {
			delete(s.vs, k)
		}
	}

	s.vs[v.Key] = v

	return nil
}

func (s *MemStore) Get(ctx context.Context, key string) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	v, ok := s.vs[key]
	if !ok {
		return Session{}, ErrNoSession
	}

	return v, nil
}

func (s *MemStore) Del(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.vs, key)

	return nil
}

// Len is how many are held, for a test and for a metric.
func (s *MemStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.vs)
}

var _ Store = (*MemStore)(nil)

// DelBy ends every session a predicate matches.
//
// The operation a store needs when somebody **leaves**. A session is keyed by
// its own value, because that is what every request has and what a lookup on
// every request must be fast for -- so "whose sessions are these" is a
// different index, and one that is asked once per departure rather than once
// per call.
//
// It is on the store rather than on [Sessions] because it is the store's shape
// that decides how it is answered: a map is walked, a table is a `WHERE`, and a
// cache with no such index cannot answer at all. A deployment whose store
// cannot do this has sessions that outlive the person by however long they had
// left, which is worth knowing rather than discovering.
func (s *MemStore) DelBy(ctx context.Context, is func(Session) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for k, v := range s.vs {
		if is(v) {
			delete(s.vs, k)
		}
	}

	return nil
}
