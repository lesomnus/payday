package config

import (
	"fmt"
	"time"

	"github.com/lesomnus/payday/watch"
)

// The brokers payday ships. A deployment names one; there is no default, and
// see [WatchConfig] for why that is the whole point of this type.
const (
	// BrokerMemory publishes inside this process, and is right for exactly one
	// replica.
	BrokerMemory = "memory"

	// BrokerNone serves no Watch at all. It is a value rather than an absence
	// so that "this deployment has no watchers" is something written down and
	// not something inferred from a field nobody filled in.
	BrokerNone = "none"
)

// DefaultDrainEvery is how often the outbox is looked at when nobody said.
//
// A second, because the queue is empty in the ordinary case and a pass over an
// empty indexed table costs nothing -- and because the alternative to a short
// interval is an event that is durable and arrives a minute late, which is the
// combination nobody wants.
const DefaultDrainEvery = time.Second

// WatchConfig is where the events a Watch reads come from.
//
// # Why there is no default
//
// The broker payday ships publishes inside the process. That is correct for one
// replica and **silently wrong for two**: a client watching against replica A
// never hears about a write that landed on replica B, and nothing reports it --
// not the client, which is holding an open stream that looks healthy, and not
// the server, which published to everyone it knew about.
//
// A default would make that the answer an app gets by saying nothing, and the
// day it becomes wrong is the day somebody adds a replica -- which is a
// deployment change nobody associates with events going missing. So the field
// is required, and scaling to two replicas means reading a line that says
// `broker: memory` and having to decide about it.
//
// It is one of the few things in payday that a deployment has to write out. The
// rule for what earns that is in the plan: **does it go quietly wrong when it
// is left out?** This does, in the worst way available -- it goes wrong later,
// elsewhere, and without an error.
type WatchConfig struct {
	// Broker is where events are published: [BrokerMemory], [BrokerNone], or
	// nothing, which is refused.
	Broker string `yaml:"broker"`

	// Outbox writes every change as a row inside the transaction that made it,
	// for a loop to publish afterwards.
	//
	// Off by default, which is the one place this type takes the quiet answer,
	// and it is a different question from the one above. A broker left unnamed
	// gives a deployment something that looks like it works; an outbox left off
	// gives it exactly what the code says -- events published in this process,
	// and a crash between the commit and the publish loses one. That is written
	// down in `payday/watch` rather than being a surprise, and the cost of the
	// other answer is a row and a delete for every write in every app that
	// never had a subscriber.
	Outbox bool `yaml:"outbox"`

	// DrainEvery is how often the outbox loop looks. Zero is
	// [DefaultDrainEvery], and it means nothing when [WatchConfig.Outbox] is
	// off.
	DrainEvery time.Duration `yaml:"drain_every"`
}

// Build answers with the broker this deployment named, and refuses one that
// named none.
//
// It answers nil for [BrokerNone], which is what a server with no Watch is
// built with -- and not an error, because "no watchers here" is a thing to be
// able to say.
func (c WatchConfig) Build() (watch.Broker, error) {
	switch c.Broker {
	case BrokerMemory:
		return watch.Memory(), nil

	case BrokerNone:
		return nil, nil

	case "":
		return nil, fmt.Errorf(
			"watch.broker: name one. `%s` publishes inside this process, which is "+
				"right for one replica and silently wrong for two -- a client watching "+
				"against one never hears about a write that landed on another, and "+
				"nothing anywhere reports it. `%s` if this deployment serves no Watch",
			BrokerMemory, BrokerNone)

	default:
		return nil, fmt.Errorf("watch.broker: %q is not one payday has; it has %s and %s",
			c.Broker, BrokerMemory, BrokerNone)
	}
}

// Every is [WatchConfig.DrainEvery] with the default filled in.
func (c WatchConfig) Every() time.Duration {
	if c.DrainEvery <= 0 {
		return DefaultDrainEvery
	}

	return c.DrainEvery
}
