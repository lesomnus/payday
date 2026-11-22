package notify

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lesomnus/payday/watch"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Routes describes a hint's audience for routing only. A named topic and a
// resource with the same textual ID remain distinct. Receipt routes name their
// reader; shared changes and policy invalidations reach every interested reader.
func Routes(namespace string, h Hint) []watch.Route {
	rs := make([]watch.Route, 0, len(h.Topics))
	for _, topic := range h.Topics {
		rs = append(rs, watch.RouteKey("payday.notify", namespace, h.Tenant.String(), topic.Key(), h.Reader.String()))
	}
	return rs
}

// Signal reads the publication-time hint a generated notification change
// carries. The generated store checks the entity before calling it: arbitrary
// application signals must not be interpreted as notification metadata.
func Signal(data []byte) (Hint, error) {
	var h Hint
	if err := json.Unmarshal(data, &h); err != nil {
		return Hint{}, status.Error(codes.Internal, "invalid notification hint")
	}
	return h, nil
}

type resolution struct {
	done  chan struct{}
	hints []Hint
	err   error
}

func (s *Service) resolve(ctx context.Context, event watch.Event) ([]Hint, error) {
	r := event.Shared(s, func() any {
		r := &resolution{done: make(chan struct{})}
		go func() {
			defer close(r.done)
			defer func() {
				if recover() != nil {
					r.err = status.Error(codes.Internal, "notification change resolver failed")
				}
			}()
			// No caller's identity or cancellation belongs to this shared read.
			// Current policies are asked later, independently by every listener.
			read, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for _, change := range event.Changes {
				var h Hint
				var relevant bool
				var err error
				if change.By == InvalidationMethod || change.By == ReceiptMethod {
					h, err = Signal(change.Signal)
					relevant = true
				} else {
					h, relevant, err = s.store.Change(read, change)
				}
				if err != nil {
					r.err = err
					return
				}
				if relevant {
					r.hints = append(r.hints, h)
				}
			}
		}()
		return r
	}).(*resolution)
	select {
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	case <-r.done:
		return r.hints, r.err
	}
}
