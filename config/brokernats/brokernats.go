// Package brokernats registers "nats" for watch.broker. Blank import it in an
// app that uses NATS, set watch.dsn to its server URLs, and choose a deployment
// watch.namespace shared by that app's replicas. Database settings are unused.
// For custom TLS/authentication options, register a builder calling watchnats.New.
package brokernats

import (
	"context"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/watch"
	"github.com/lesomnus/payday/watch/watchnats"
)

const Name = "nats"

func init() {
	config.RegisterBroker(Name, func(ctx context.Context, c config.WatchConfig, _ config.DbConfig) (watch.Broker, error) {
		return watchnats.New(ctx, c.Dsn, c.Namespace)
	})
}
