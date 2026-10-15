package rbac

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/lesomnus/payday/grpcx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Store replaces complete, validated policies. A request can pin a snapshot
// so its gate, scope and escalation checks all answer from the same revision.
// Changed streams reconnect: keeping the old context would keep old grants.
type Store struct {
	mu      sync.RWMutex
	cur     *Snapshot
	changed chan struct{}
	encoded string
	checks  []func(Rules) error
}

func NewStore(r Rules, checks ...func(Rules) error) (*Store, error) {
	s := &Store{changed: make(chan struct{}), checks: checks}
	if err := s.Replace(r); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Replace(r Rules) error {
	p, err := New(r)
	if err != nil {
		return err
	}
	for _, check := range s.checks {
		if err := check(r); err != nil {
			return err
		}
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil && s.encoded == string(b) {
		return nil
	}
	s.cur, s.encoded = p, string(b)
	close(s.changed)
	s.changed = make(chan struct{})
	return nil
}

func (s *Store) Current() (*Snapshot, <-chan struct{}) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur, s.changed
}

func (s *Store) Pin(ctx context.Context) context.Context {
	if _, ok := ctx.Value(s).(*Snapshot); ok {
		return ctx
	}
	p, _ := s.Current()
	return context.WithValue(ctx, s, p)
}

func (s *Store) Grants(ctx context.Context, name string) ([]Grant, error) {
	p, ok := ctx.Value(s).(*Snapshot)
	if !ok {
		p, _ = s.Current()
	}
	return p.Grants(ctx, name)
}

// Interceptor pins unary requests and ends streams on an accepted change.
// All streams using this source reconnect, including ones unaffected by an
// edit; this avoids guessing whether a grant change is a restriction.
func (s *Store) Interceptor() grpcx.Chain {
	return grpcx.Chain{
		Unary: []grpc.UnaryServerInterceptor{func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			return next(s.Pin(ctx), req)
		}},
		Stream: []grpc.StreamServerInterceptor{func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
			p, changed := s.Current()
			ctx, cancel := context.WithCancelCause(context.WithValue(ss.Context(), s, p))
			defer cancel(nil)
			done := make(chan struct{})
			defer close(done)
			errChanged := status.Error(codes.Unauthenticated, "authorization policy changed; reconnect")
			go func() {
				select {
				case <-changed:
					cancel(errChanged)
				case <-ctx.Done():
				case <-done:
				}
			}()
			err := next(srv, grpcx.StreamWithContext(ss, ctx))
			if context.Cause(ctx) == errChanged {
				return errChanged
			}
			return err
		}},
	}
}
