package watch

import "context"

type callKey struct{}

// CallMethod identifies the outer call even while Batch replaces gRPC's method
// with that of an operation. Direct writers use fallback unless Transaction names
// their origin explicitly.
func CallMethod(ctx context.Context, fallback string) string {
	if m, ok := ctx.Value(callKey{}).(string); ok && m != "" {
		return m
	}
	return fallback
}

// Transaction collects a custom transaction's notifications separately. Run must
// commit before returning nil and roll back on error. It must pass the supplied
// context to every write. Failed attempts are discarded even when an enclosing RPC
// catches their errors. Without an enclosing interceptor, success publishes here.
// Durable recording still belongs to the transaction's outbox recorder.
func (w *Watch) Transaction(ctx context.Context, method string, run func(context.Context) error) error {
	parent, nested := use(ctx)
	cs := &changes{}
	child := into(context.WithValue(ctx, callKey{}, CallMethod(ctx, method)), cs)
	if err := run(child); err != nil {
		return err
	}
	collected := cs.taken()
	if nested {
		for _, c := range collected {
			parent.add(c)
		}
	} else {
		w.publish(ctx, nil, nil, method, collected)
	}
	return nil
}
