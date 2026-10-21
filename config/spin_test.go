package config_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/spin"
)

// TestAnUnnamedLeaseIsRefused pins the refusal [config.SpinConfig] is built
// around, as [config.WatchConfig]'s is: no default, since the one there would
// be runs every loop in every process -- right for one replica and silently
// wrong for two -- and the refusal says so and names the ways out.
func TestAnUnnamedLeaseIsRefused(t *testing.T) {
	x := require.New(t)

	_, err := config.SpinConfig{}.Build(t.Context(), config.DbConfig{})
	x.ErrorContains(err, "spin.lease")
	x.ErrorContains(err, config.LeaseLocal)
	x.ErrorContains(err, config.LeaseNone)
	x.ErrorContains(err, "one replica", "the reason, not just the names")

	_, err = config.SpinConfig{Lease: "zookeeper"}.Build(t.Context(), config.DbConfig{})
	x.ErrorContains(err, `"zookeeper"`)
	x.ErrorContains(err, "RegisterLease")
}

// TestTheLeasesPaydayShipsAreWhatTheySay: `local` holds every name in this
// process, and `none` holds nothing at all.
func TestTheLeasesPaydayShipsAreWhatTheySay(t *testing.T) {
	x := require.New(t)

	l, err := config.SpinConfig{Lease: config.LeaseLocal}.Build(t.Context(), config.DbConfig{})
	x.NoError(err)
	held, release, err := l.Hold(t.Context(), "app.sweep")
	x.NoError(err)
	x.NoError(held.Err())
	release()

	l, err = config.SpinConfig{Lease: config.LeaseNone}.Build(t.Context(), config.DbConfig{})
	x.NoError(err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = l.Hold(ctx, "app.sweep")
	x.ErrorIs(err, context.Canceled, "held by a lease that holds nothing")

	x.Contains(config.Leases(), config.LeaseLocal)
	x.Contains(config.Leases(), config.LeaseNone)
}

// TestARegisteredLeaseIsNamedLikeTheOthers: what [config.RegisterLease] records
// is built from its name, with the serve command's context, its configuration
// and the app's database, and listed beside the ones payday ships.
func TestARegisteredLeaseIsNamedLikeTheOthers(t *testing.T) {
	x := require.New(t)

	type mark struct{}
	var told config.SpinConfig
	var under context.Context
	config.RegisterLease("test-registered", func(ctx context.Context, c config.SpinConfig, db config.DbConfig) (spin.Lease, error) {
		told, under = c, ctx
		return spin.Local(), nil
	})

	ctx := context.WithValue(t.Context(), mark{}, "serve")
	l, err := config.SpinConfig{Lease: "test-registered", Dsn: "somewhere"}.Build(ctx, config.DbConfig{})
	x.NoError(err)
	x.NotNil(l)
	x.Equal("somewhere", told.Dsn)
	x.Equal("serve", under.Value(mark{}), "built on a context other than the one it was given")
	x.Contains(config.Leases(), "test-registered")
}
