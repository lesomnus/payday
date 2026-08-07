package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"

	// The app is what links a driver in, and this test file is standing in for
	// one. Nothing below names the package again: what it does, it does from
	// its init, which is the whole shape of the thing being tested.
	_ "github.com/lesomnus/payday/config/dbsqlite3"
)

func TestDrivers(t *testing.T) {
	t.Run("a driver an app linked in is a driver a file may name", func(t *testing.T) {
		x := require.New(t)

		x.Contains(config.Drivers(), "sqlite3")

		// And naming the driver is enough: the dialect follows from it, so a
		// file says the one thing a person knows.
		v, ok := config.DriverFor(config.DialectSQLite)
		x.True(ok)
		x.Equal("sqlite3", v)
	})
	t.Run("a dialect nothing linked in speaks has no driver", func(t *testing.T) {
		x := require.New(t)

		// Which is a real answer rather than a mistake: an app that runs on
		// PostgreSQL only has nothing that speaks MySQL, and the command that
		// asked can say so.
		_, ok := config.DriverFor("nonesuch")
		x.False(ok)
	})
	t.Run("a driver nobody linked in is refused, with what was", func(t *testing.T) {
		x := require.New(t)

		_, _, err := config.DbConfig{Driver: "nonesuch"}.Open(t.Context())
		x.ErrorContains(err, "nonesuch")
		// The list is the useful half of it: forgetting the blank import looks
		// exactly like misspelling the driver until something says which
		// drivers this binary actually has.
		x.ErrorContains(err, "sqlite3")
	})
	t.Run("a dialect written down is taken over the driver's own", func(t *testing.T) {
		x := require.New(t)

		// Which is how a driver payday has never heard of is used: name the
		// dialect it speaks and no registration is needed at all.
		db, dialect, err := config.DbConfig{
			Driver:  "sqlite3",
			Dialect: config.DialectPostgres,
			Dsn:     "file:test.db?mode=memory",
		}.Open(t.Context())
		x.NoError(err)
		defer db.Close()

		x.Equal(config.DialectPostgres, dialect)
	})
}

func TestDbOpen(t *testing.T) {
	t.Run("what comes back is a connection and what it speaks", func(t *testing.T) {
		x := require.New(t)

		// Not an ent client: the client is generated into the app, out of the
		// app's own schema, and payday has no name for that type. These two
		// are what one is made of.
		db, dialect, err := config.DbConfig{
			Driver: "sqlite3",
			Dsn:    "file:test.db?mode=memory",
		}.Open(t.Context())
		x.NoError(err)
		defer db.Close()

		x.Equal(config.DialectSQLite, dialect)
		x.NoError(db.PingContext(t.Context()))
	})
	t.Run("a database that cannot be reached is found out here", func(t *testing.T) {
		x := require.New(t)

		// `sql.Open` connects to nothing, so without the ping a deployment
		// pointed at a database that is not there starts, serves, reports
		// itself healthy, and fails at the first request instead.
		_, _, err := config.DbConfig{
			Driver: "sqlite3",
			Dsn:    "file:/no/such/directory/test.db",
		}.Open(t.Context())
		x.ErrorContains(err, "ping")
	})
}
