package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWritesImmediately pins the DSN rewriting [DbConfig.Open] does, including
// the two DSNs it must leave alone.
func TestWritesImmediately(t *testing.T) {
	for _, c := range []struct {
		name    string
		dialect string
		dsn     string
		want    string
	}{
		{
			name:    "a file DSN with no transaction mode gets one",
			dialect: DialectSQLite,
			dsn:     "file:roster.db?_pragma=foreign_keys%281%29",
			want:    "file:roster.db?_pragma=foreign_keys%281%29&_txlock=immediate",
		},
		{
			name:    "and one with no query at all",
			dialect: DialectSQLite,
			dsn:     "file:roster.db",
			want:    "file:roster.db?_txlock=immediate",
		},
		{
			// An app that says what it wants keeps it, which is how the old
			// behaviour is still available.
			name:    "a transaction mode already given is left alone",
			dialect: DialectSQLite,
			dsn:     "file:roster.db?_txlock=deferred",
			want:    "file:roster.db?_txlock=deferred",
		},
		{
			// A bare path is a filename and not a URI: SQLite would open a file
			// called `roster.db?_txlock=immediate`.
			name:    "a bare path is a filename, so it is not touched",
			dialect: DialectSQLite,
			dsn:     "roster.db",
			want:    "roster.db",
		},
		{
			name:    "PostgreSQL has none of this",
			dialect: DialectPostgres,
			dsn:     "postgres://app:app@127.0.0.1/app?sslmode=disable",
			want:    "postgres://app:app@127.0.0.1/app?sslmode=disable",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, writesImmediately(c.dialect, c.dsn))
		})
	}
}

// TestDsnDefaults pins what a driver's registered defaults do to a DSN.
func TestDsnDefaults(t *testing.T) {
	RegisterDsnDefault("driver_test", "_timefmt", "unixepoch_nano")

	for _, c := range []struct {
		name   string
		driver string
		dsn    string
		want   string
	}{
		{
			name:   "a DSN with no time format gets one",
			driver: "driver_test",
			dsn:    "file:roster.db?_txlock=immediate",
			want:   "file:roster.db?_timefmt=unixepoch_nano&_txlock=immediate",
		},
		{
			name:   "a time format already given is left alone",
			driver: "driver_test",
			dsn:    "file:roster.db?_timefmt=rfc3339",
			want:   "file:roster.db?_timefmt=rfc3339",
		},
		{
			// A bare path is a filename and not a URI, as it is for _txlock.
			name:   "a bare path is a filename, so it is not touched",
			driver: "driver_test",
			dsn:    "roster.db",
			want:   "roster.db",
		},
		{
			// Which is every driver but the two SQLite ones: what they set is
			// spelled their own way, so nothing is set for a driver that did
			// not say what it spells it.
			name:   "a driver that registered nothing gets nothing",
			driver: "pgx",
			dsn:    "postgres://app:app@127.0.0.1/app?sslmode=disable",
			want:   "postgres://app:app@127.0.0.1/app?sslmode=disable",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, withDsnDefaults(c.driver, c.dsn))
		})
	}
}
