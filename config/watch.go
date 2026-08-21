package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
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
	// Broker is where events are published: [BrokerMemory], [BrokerNone], a
	// name something registered with [RegisterBroker], or nothing, which is
	// refused.
	Broker string `yaml:"broker"`

	// Dsn is where the broker connects, for one that connects somewhere the
	// app is not already.
	//
	// **Empty is the app's own database**, and for the broker most deployments
	// will name that is not a fallback but the answer: a Postgres broker is
	// `LISTEN`/`NOTIFY` on the rows' own database, which is why it needs no
	// address and no second piece of infrastructure. A broker that is a message
	// bus is told where it is here.
	Dsn string `yaml:"dsn"`

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

// brokers holds what each registered name builds.
//
// `memory` is not in here: it is what payday ships and [WatchConfig.Build]
// answers for it directly, the way an app that needs no other database still
// gets its driver named. What this map is for is the one after that.
var brokers = map[string]func(c WatchConfig, db DbConfig) (watch.Broker, error){}

// RegisterBroker records how to build the broker named `name`, so that a
// deployment can select one payday does not ship by naming it.
//
// # Why this exists, and why it took a while to
//
// [watch.Broker] has always been an interface, and that was called the seam.
// It was half of one. An app could **implement** a broker and could not
// **select** it: `Build` was a closed switch over two names, so the only way to
// use one was to bypass the configuration -- which is not a thing an app can do
// without also bypassing every other decision `WatchConfig` makes, and is not
// a thing a deployment can do at all.
//
// So the shape is [RegisterDriver]'s, for the same reason it is: a broker is a
// client for something -- NATS, Redis, a Postgres LISTEN -- that has to be
// linked in whether anything publishes to it or not, and payday importing every
// one of them would put all of them in every binary. Each registration is a
// package of its own, and an app blank imports the one it uses:
//
//	import _ "github.com/acme/thing/brokernats"
//
// # What it is told
//
// Its own configuration, and **the database the app is on**. The second is
// there because the first broker anybody writes rides that database: Postgres
// answers `LISTEN`/`NOTIFY`, so an app already talking to one needs no second
// piece of infrastructure to make its replicas hear each other. A broker that
// connects somewhere else ignores it and reads [WatchConfig.Dsn].
//
// The function is called once, when a server is built. A broker that cannot
// reach whatever it publishes to answers with an error there rather than
// panicking later, which is the same bargain [DbConfig.Open] makes.
func RegisterBroker(name string, build func(c WatchConfig, db DbConfig) (watch.Broker, error)) {
	brokers[name] = build
}

// Brokers lists every name a deployment may write, in lexical order and
// including the two payday ships.
func Brokers() []string {
	return slices.Sorted(maps.Keys(known()))
}

func known() map[string]struct{} {
	vs := map[string]struct{}{BrokerMemory: {}, BrokerNone: {}}
	for k := range brokers {
		vs[k] = struct{}{}
	}

	return vs
}

// Build answers with the broker this deployment named, and refuses one that
// named none.
//
// It answers nil for [BrokerNone], which is what a server with **no Watch** is
// built with -- and not an error, because "no watchers here" is a thing to be
// able to say. What makes that mean something is [watch.Stream], which refuses
// a subscriber outright rather than handing back a stream that never speaks;
// before it did, `none` was the quietest setting available instead of the
// loudest.
func (c WatchConfig) Build(db DbConfig) (watch.Broker, error) {
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
	}

	build, ok := brokers[c.Broker]
	if !ok {
		return nil, fmt.Errorf(
			"watch.broker: %q is not one this binary has; it has %s.\n\n"+
				"    a broker is linked in, like a database driver: import the package "+
				"that registers this name, or see config.RegisterBroker to write one",
			c.Broker, strings.Join(Brokers(), ", "))
	}

	b, err := build(c, db)
	if err != nil {
		return nil, fmt.Errorf("watch.broker: %s: %w", c.Broker, err)
	}
	if b == nil {
		// A registration that answers with nothing is `none` under another
		// name, which is a thing to have to write rather than to arrive at.
		return nil, fmt.Errorf(
			"watch.broker: %s: built no broker; say %s if that is what was meant",
			c.Broker, BrokerNone)
	}

	return b, nil
}

// Every is [WatchConfig.DrainEvery] with the default filled in.
func (c WatchConfig) Every() time.Duration {
	if c.DrainEvery <= 0 {
		return DefaultDrainEvery
	}

	return c.DrainEvery
}
