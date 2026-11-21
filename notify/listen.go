package notify

import (
	"context"
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

// ReceiptMethod identifies the hint Mark notes of a receipt it wrote: its
// tenant, its topic and its reader, carried in the change's Signal. A receipt
// is notify's own write and no server's (payday#197), so nothing else notes
// it. No RPC has this name either, and ResourceService ignores the change for
// the reason it ignores an invalidation.
const ReceiptMethod = "/payday.notify/internal/Receipt"

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
	routes := Routes(s.namespace, Hint{Tenant: req.Tenant, Topics: topics})
	routes = append(routes, Routes(s.namespace, Hint{Tenant: req.Tenant, Topics: topics, Reader: reader})...)
	sub, err := s.watch.Open(ctx, watch.Interest{Routes: routes})
	if err != nil {
		return err
	}
	defer sub.Close()
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
		case event, ok := <-sub.Events:
			if !ok {
				return status.Error(codes.ResourceExhausted, "notification stream lost its broker connection or fell behind; reconnect and reconcile")
			}
			invalidated := make(map[Topic]bool)
			hints, err := s.resolve(ctx, event)
			if err != nil {
				return err
			}
			for _, h := range hints {
				if h.Tenant != req.Tenant || (!h.Reader.IsZero() && h.Reader != reader) {
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
	if err := s.tenant(ctx, tenantID); err != nil {
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
	if err := s.tenant(ctx, tenantID); err != nil {
		return err
	}
	ts, err := normalize(topics)
	if err != nil {
		return err
	}
	if s.watch == nil || s.watch.Broker() == nil {
		return status.Error(codes.Unavailable, "notification broker is unavailable")
	}
	return invalidate(ctx, s.watch, s.namespace, tenantID, ts)
}
