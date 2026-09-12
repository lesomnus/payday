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
