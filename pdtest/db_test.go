package pdtest

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSecondDBIsASecondDatabase pins what [DB]'s comment promises: two calls
// in one test are two databases, and the second arriving costs the first
// nothing.
//
// It runs on whichever backend the environment names, and the halves pull
// different weight. On SQLite every call was always a database of its own, so
// these assertions cannot fail there and the load-bearing check is [TestNth].
// Under [Postgres] this is the regression test: both calls once computed one
// schema name, so the second call's `DROP SCHEMA` removed the first app's
// tables -- and it was an app's integration suite that caught it, because this
// package had no test saying otherwise.
func TestSecondDBIsASecondDatabase(t *testing.T) {
	x := require.New(t)

	drv1, dsn1 := DB(t)
	db1, err := sql.Open(drv1, dsn1)
	x.NoError(err)
	defer db1.Close()

	_, err = db1.Exec(`CREATE TABLE arm (id INTEGER PRIMARY KEY)`)
	x.NoError(err)
	_, err = db1.Exec(`INSERT INTO arm (id) VALUES (1)`)
	x.NoError(err)

	// The table exists before the second call on purpose: the old bug was in
	// what that call dropped, not in where it pointed.
	drv2, dsn2 := DB(t)
	x.Equal(drv1, drv2)
	x.NotEqual(dsn1, dsn2)

	var n int
	x.NoError(db1.QueryRow(`SELECT COUNT(*) FROM arm`).Scan(&n))
	x.Equal(1, n)

	db2, err := sql.Open(drv2, dsn2)
	x.NoError(err)
	defer db2.Close()

	// Another database, not another handle on the first: what the first holds
	// is not reachable through the second.
	x.Error(db2.QueryRow(`SELECT COUNT(*) FROM arm`).Scan(&n))

	t.Run("and under postgres, because the schema is named for the call", func(t *testing.T) {
		if os.Getenv(Postgres) == "" {
			t.Skipf("%s is not set; whether the count reaches the server is that server's half", Postgres)
		}

		var s1, s2 string
		x.NoError(db1.QueryRow(`SELECT current_schema()`).Scan(&s1))
		x.NoError(db2.QueryRow(`SELECT current_schema()`).Scan(&s2))
		x.NotEqual(s1, s2)
		x.True(strings.HasSuffix(s2, "_1"), "second schema %q should carry the call count", s2)
	})
}

// TestNth is the half of the property above that a machine without a server
// can still fail. The counter is what turns a second call into a second
// schema, and SQLite never consults it -- so a break in it rides green
// everywhere until a suite next meets a real Postgres, which is exactly the
// direction db.go says hides mistakes.
func TestNth(t *testing.T) {
	x := require.New(t)

	// Keyed off this test's own name so that counting here shifts nothing for
	// any other test in this binary: the counter is process-wide on purpose.
	name := t.Name() + "/counted"
	x.Equal(0, nth(name))
	x.Equal(1, nth(name))
	x.Equal(2, nth(name))

	// Per name, not global: another test's first database is still its first.
	x.Equal(0, nth(name+"/another"))
}
