// Package leasepg makes "postgres" a lease a configuration may name (#54).
//
// It is a package rather than a line in `config` for the reason `brokerpg` is
// one: importing it links a database client in. An app that runs on PostgreSQL
// says so once, where it says what else it is built out of:
//
//	import _ "github.com/lesomnus/payday/config/leasepg"
//
// and then `spin.lease: postgres` is a line a deployment can write.
//
// # It is the app's own database
//
// `spin.dsn` is empty for this one and should stay empty unless the app's
// address goes through a transaction pooler: an advisory lock stores nothing,
// so a second database would be a second thing to run for no benefit. What that
// buys is the point of the whole seam -- for an app already on Postgres, going
// from one replica to several is a line of configuration and not a piece of
// infrastructure, for its background work as for its watches.
package leasepg

import (
	"context"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/spin"
	"github.com/lesomnus/payday/spin/spinpg"
)

// Name is what a deployment writes.
const Name = "postgres"

func init() {
	config.RegisterLease(Name, func(c config.SpinConfig, db config.DbConfig) (spin.Lease, error) {
		dsn := c.Dsn
		if dsn == "" {
			// The rows' own database, which is the answer for this lease
			// rather than a fallback -- and only if it is a Postgres, named
			// by dialect, since a deployment may have registered its own
			// driver.
			dsn = db.Dsn
			v, err := db.Speaks()
			if err != nil {
				return nil, err
			}
			if err := spinpg.Dialect(v); err != nil {
				return nil, err
			}
		}

		// Not the caller's: this dials to check, and what it builds outlives
		// the call.
		return spinpg.New(context.WithoutCancel(context.Background()), dsn)
	})
}
