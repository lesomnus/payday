// Package brokerpg makes "postgres" a broker a configuration may name.
//
// It is a package rather than a line in `config` for the reason `dbpgx` is one:
// importing it links a database client in, and an app that publishes somewhere
// else should not carry one. So an app that runs on PostgreSQL says so once,
// where it says what else it is built out of:
//
//	import _ "github.com/lesomnus/payday/config/brokerpg"
//
// and then `watch.broker: postgres` is a line a deployment can write.
//
// # It is the app's own database
//
// `watch.dsn` is empty for this one and should stay empty. `LISTEN`/`NOTIFY`
// stores nothing -- a notification goes to whoever is listening and is then
// forgotten -- so a second database would be a second thing to run for no
// benefit, and a second thing to get wrong.
//
// What that buys is the whole point: for an app already on Postgres, the
// difference between one replica and several stops being infrastructure and
// becomes a line of configuration.
package brokerpg

import (
	"context"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/watch"
	"github.com/lesomnus/payday/watch/watchpg"
)

// Name is what a deployment writes.
const Name = "postgres"

func init() {
	config.RegisterBroker(Name, func(c config.WatchConfig, db config.DbConfig) (watch.Broker, error) {
		dsn := c.Dsn
		if dsn == "" {
			// The rows' own database, which is the answer for this broker
			// rather than a fallback. `watch.dsn` is here for a deployment
			// whose Postgres is reachable at a different address from the one
			// the app writes through -- a pooler in front of the writer is the
			// case, and `LISTEN` does not survive one.
			dsn = db.Dsn

			// Only if it is a Postgres. Named by dialect and not by driver,
			// because a deployment may have registered its own.
			v, err := db.Speaks()
			if err != nil {
				return nil, err
			}
			if err := watchpg.Dialect(v); err != nil {
				return nil, err
			}
		}

		// Not the caller's: this dials to check, and what it builds outlives
		// the call. A server being built is what is doing the calling.
		return watchpg.New(context.WithoutCancel(context.Background()), dsn)
	})
}
