package notify

import (
	"context"
	"encoding/json"
	"slices"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/watch"
)

// InvalidationMethod identifies server-authored policy invalidations on the
// existing broker. No RPC has this name, and ResourceService ignores these
// key-only changes because they carry no Resource envelope.
const InvalidationMethod = "/payday.notify/internal/Invalidate"

func (s *Service) Listen(ctx context.Context, req ListenRequest, send func(Event) error) error {
	topics, reader, err := s.scope(ctx, req.Tenant, req.Topics)
	if err != nil {
		return err
	}
	if s.watch == nil || s.watch.Broker() == nil {
		return status.Error(codes.Unavailable, "notification broker is unavailable")
	}
	if send == nil {
		return status.Error(codes.Internal, "notification listener has no sender")
	}
	changes, stop := s.watch.Subscribe()
	defer stop()
	// Install the subscription before readiness, then reauthorize so a policy
	// change racing initial authorization cannot install a stale subscription.
	if err := s.reauthorize(ctx, req.Tenant, reader, topics); err != nil {
		return err
	}
	if err := send(Event{Ready: true, Reader: reader}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case event, ok := <-changes:
			if !ok {
				return status.Error(codes.ResourceExhausted, "notification stream lost its broker connection or fell behind; reconnect and reconcile")
			}
			invalidated := make(map[Topic]bool)
			for _, change := range event.Changes {
				var h Hint
				var relevant bool
				if change.By == InvalidationMethod {
					if err := json.Unmarshal(change.Signal, &h); err != nil {
						return status.Error(codes.Internal, "invalid notification policy hint")
					}
					relevant = true
				} else {
					h, relevant, err = s.store.Change(ctx, change)
					if err != nil {
						return err
					}
				}
				if !relevant || h.Tenant != req.Tenant || (!h.Reader.IsZero() && h.Reader != reader) {
					continue
				}
				for _, t := range h.Topics {
					if slices.Contains(topics, t) {
						invalidated[t] = true
					}
				}
			}
			if len(invalidated) == 0 {
				continue
			}
			if err := s.reauthorize(ctx, req.Tenant, reader, topics); err != nil {
				return err
			}
			out := make([]Topic, 0, len(invalidated))
			for _, t := range topics {
				if invalidated[t] {
					out = append(out, t)
				}
			}
			if err := send(Event{Reader: reader, Topics: out}); err != nil {
				return err
			}
		}
	}
}

func (s *Service) reauthorize(ctx context.Context, tenantID, reader pdid.Id, topics []Topic) error {
	if err := tenant(ctx, tenantID); err != nil {
		return subscriptionError(err, "notification subscription lost tenant access")
	}
	current, err := s.policy.Reader(ctx, tenantID)
	if err != nil {
		return subscriptionError(policyError(err), "notification subscription reader is no longer authorized")
	}
	if identifier(current) != nil || current != reader {
		return status.Error(codes.PermissionDenied, "notification subscription reader changed or is no longer authorized")
	}
	for _, t := range topics {
		if err := s.policy.Read(ctx, tenantID, reader, t); err != nil {
			return subscriptionError(policyError(err), "notification subscription lost topic access")
		}
	}
	return nil
}

// A dependency outage is not a revocation. Clients discard authorized cached
// state on PermissionDenied, while operational failures call for reconnection.
func subscriptionError(err error, message string) error {
	switch status.Code(err) {
	case codes.PermissionDenied, codes.NotFound, codes.Unauthenticated:
		return status.Error(codes.PermissionDenied, message)
	default:
		return err
	}
}

// Invalidate announces committed changes to an app's authorization dependencies.
// The app calls it after its transaction commits. It is not a client operation;
// no policy bypass or public invalidation RPC is installed by this method.
func (s *Service) Invalidate(ctx context.Context, tenantID pdid.Id, topics []Topic) error {
	if err := identifier(tenantID); err != nil {
		return err
	}
	ts, err := normalize(topics)
	if err != nil {
		return err
	}
	if s.watch == nil || s.watch.Broker() == nil {
		return status.Error(codes.Unavailable, "notification broker is unavailable")
	}
	signal, err := json.Marshal(Hint{Tenant: tenantID, Topics: ts})
	if err != nil {
		return err
	}
	return watch.Confirm(context.WithoutCancel(ctx), s.watch.Broker(), watch.Event{Tenant: tenantID,
		Method: InvalidationMethod, Changes: []watch.Change{{By: InvalidationMethod, Key: tenantID, Signal: signal}}})
}
