package config

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lesomnus/payday/spin"
)

// The leases payday ships (#54). A deployment names one; there is no default,
// for the reason [WatchConfig] has none.
const (
	// LeaseLocal holds every name in the process that asks, and is right for
	// a deployment of exactly one process.
	LeaseLocal = "local"

	// LeaseNone holds nothing: this process runs none of the loops only one
	// process of a deployment may, and leaves them to the processes that hold
	// their leases. A replica that only answers requests says so here.
	LeaseNone = "none"
)

// SpinConfig is which process of a deployment runs the loops only one of them
// may: the sweeps, the outbox drain, the trail's retention (`spin.Lease`).
//
// # Why there is no default
//
// It is [WatchConfig]'s problem one level over. Without a lease every process
// runs every loop, which is right for one replica and **silently wrong for
// two**: each sweeps what the other is sweeping, a notification goes out twice,
// an outbox is published twice -- and nothing reports it, since every process
// did exactly what it was built to. A default would make that what an app gets
// by saying nothing, and the day it became wrong would be the day somebody
// added a replica. So it is written out, and scaling to two means reading a
// line that says `lease: local` and deciding about it.
//
// The rule from the plan holds: it goes quietly wrong when left out, later and
// elsewhere.
type SpinConfig struct {
	// Lease is [LeaseLocal], [LeaseNone], a name something registered with
	// [RegisterLease], or nothing, which is refused.
	Lease string `yaml:"lease"`

	// Dsn is where the lease connects, for one that connects somewhere the app
	// is not already. **Empty is the app's own database**, which for the lease
	// most deployments will name is the answer rather than a fallback: a
	// Postgres lease is an advisory lock on the rows' own database. It is here
	// for an address that reaches the database directly when the app's goes
	// through a pooler that a session lock does not survive.
	Dsn string `yaml:"dsn"`
}

// leases holds what each registered name builds.
var leases = map[string]func(ctx context.Context, c SpinConfig, db DbConfig) (spin.Lease, error){}

// RegisterLease records how to build the lease named `name`, so that a
// deployment selects it by naming it. It is [RegisterBroker]'s shape and for
// its reason: a lease is a client for something -- an advisory lock, a key with
// a time to live -- that is linked in whether or not anything holds it, so each
// is a package of its own that an app blank imports:
//
//	import _ "github.com/lesomnus/payday/config/leasepg"
//
// It is told what [RegisterBroker] is told, and for its reasons: the context
// the serve command runs under -- what it dials under, and the log it says a
// lost hold in -- its own configuration, and the database the app is on, which
// the first lease anybody writes rides.
func RegisterLease(name string, build func(ctx context.Context, c SpinConfig, db DbConfig) (spin.Lease, error)) {
	leases[name] = build
}

// Leases lists every name a deployment may write, in lexical order and
// including the two payday ships.
func Leases() []string {
	vs := map[string]struct{}{LeaseLocal: {}, LeaseNone: {}}
	for k := range leases {
		vs[k] = struct{}{}
	}

	return slices.Sorted(maps.Keys(vs))
}

// Build answers with the lease this deployment named, and refuses one that
// named none.
//
// It is built where the loops are run -- an app's serve command, beside
// `spin.Run` -- and not where a server is, so that a test that builds a server
// and runs no loop has nothing to name. `ctx` is the serve command's, carrying
// the telemetry it built; see [RegisterLease].
func (c SpinConfig) Build(ctx context.Context, db DbConfig) (spin.Lease, error) {
	switch c.Lease {
	case LeaseLocal:
		return spin.Local(), nil

	case LeaseNone:
		return spin.Nowhere(), nil

	case "":
		return nil, fmt.Errorf(
			"spin.lease: name one. `%s` runs every loop in this process, which is right for one "+
				"replica and silently wrong for two -- each runs every sweep, and nothing reports it. "+
				"`%s` for a process that runs none of them; %s",
			LeaseLocal, LeaseNone, others())
	}

	build, ok := leases[c.Lease]
	if !ok {
		return nil, fmt.Errorf(
			"spin.lease: %q is not one this binary has; it has %s.\n\n"+
				"    a lease is linked in, like a database driver: import the package "+
				"that registers this name, or see config.RegisterLease to write one",
			c.Lease, strings.Join(Leases(), ", "))
	}

	l, err := build(ctx, c, db)
	if err != nil {
		return nil, fmt.Errorf("spin.lease: %s: %w", c.Lease, err)
	}
	if l == nil {
		return nil, fmt.Errorf("spin.lease: %s built nothing", c.Lease)
	}

	return l, nil
}

// others says what else there is to name, for the refusal of nothing.
func others() string {
	if len(leases) == 0 {
		return "a deployment of several processes names one that holds across them, " +
			"such as `postgres` with github.com/lesomnus/payday/config/leasepg imported"
	}

	return "or one of " + strings.Join(slices.Sorted(maps.Keys(leases)), ", ") + " to hold across processes"
}
