package leasepg_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/config/leasepg"
	"github.com/lesomnus/payday/pdtest"
)

// TestAPostgresLeaseIsOnlyOnPostgres: `spin.lease: postgres` is a name the
// configuration has once this is imported, it holds on the app's own database,
// and an app on anything else is told it needs another lease.
func TestAPostgresLeaseIsOnlyOnPostgres(t *testing.T) {
	x := require.New(t)
	x.Contains(config.Leases(), leasepg.Name)

	_, err := config.SpinConfig{Lease: leasepg.Name}.Build(config.DbConfig{Dialect: "sqlite3", Dsn: "file::memory:"})
	x.ErrorContains(err, "PostgreSQL")

	v := os.Getenv(pdtest.Postgres)
	if v == "" {
		t.Skipf("%s is not set", pdtest.Postgres)
	}
	l, err := config.SpinConfig{Lease: leasepg.Name}.Build(config.DbConfig{Dialect: "postgres", Dsn: v})
	x.NoError(err)
	held, release, err := l.Hold(t.Context(), t.Name())
	x.NoError(err)
	x.NoError(held.Err())
	release()
}
