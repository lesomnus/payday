package watch

import "context"

// ConfirmedBroker can acknowledge acceptance by the broker. This does not promise
// receipt by every subscriber or durable replay after a subscriber disconnects.
type ConfirmedBroker interface {
	PublishConfirmed(context.Context, Event) error
}

// Confirm uses an acknowledged path where available. Legacy custom brokers keep
// their existing Publish contract; they must implement ConfirmedBroker before an
// outbox can retry failed asynchronous sends.
func Confirm(ctx context.Context, b Broker, v Event) error {
	if c, ok := b.(ConfirmedBroker); ok {
		return c.PublishConfirmed(ctx, v)
	}
	b.Publish(ctx, v)
	return nil
}
func (b *memory) PublishConfirmed(ctx context.Context, v Event) error { b.Publish(ctx, v); return nil }
