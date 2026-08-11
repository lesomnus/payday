package pdtest

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/ncruces/go-sqlite3/vfs/memdb"

	// Both halves of both drivers: the `database/sql` registration that
	// `sql.Open` needs, and the payday one that `config.DbConfig` needs.
	//
	// The second is why these are the driver **packages** rather than the bare
	// drivers. An app builds its harness by handing what [DB] answers to a
	// `config.DbConfig`, and a configuration can only name a driver something
	// registered -- so a helper that answered "pgx" without registering it
	// would be handing back an answer the app cannot use. Which it did, and
	// what hid it was that the app already importing `dbpgx` for its own sake.
	_ "github.com/lesomnus/payday/config/dbpgx"
	_ "github.com/lesomnus/payday/config/dbsqlite3"
)

// Postgres is the environment variable naming a database to run the suite
// against instead of an in-memory one.
//
//	PDTEST_POSTGRES='postgres://app:app@127.0.0.1/app?sslmode=disable' go test ./...
//
// # Why an app should care
//
// Because SQLite is not the database anybody deploys on, and it is permissive
// in the directions that hide mistakes. It has no real types, so a column takes
// whatever is handed to it; it sorts NULLs the other way round from PostgreSQL,
// which is a paging bug rather than a failure; its partial indexes, its
// uniqueness and its transactions all differ.
//
// Everything payday generates is SQL. A suite that only ever runs on SQLite is
// one that has never seen the statements it will actually issue.
//
// It is opt-in because SQLite needs no server, and a suite nobody can run
// without one is a suite nobody runs.
const Postgres = "PDTEST_POSTGRES"

// DB is a database for one test: what driver to open it with, and where.
//
//	driver, dsn := pdtest.DB(t)
//	db, err := sql.Open(driver, dsn)
//
// Without [Postgres] in the environment that is an in-memory SQLite database of
// this test's own, which is what it has always been. With it, it is a **schema
// of its own** inside the named PostgreSQL: created here, dropped when the test
// ends, and named after the test so that a leak says which one leaked.
//
// A schema rather than a database because it is fast enough to do per test, and
// per test is what keeps a suite from depending on the order it runs in. A
// shared server is the one thing SQLite never made anybody think about.
func DB(tb testing.TB) (string, string) {
	tb.Helper()

	dsn := os.Getenv(Postgres)
	if dsn == "" {
		return "sqlite3", memdb.TestDB(tb, url.Values{"_pragma": {"foreign_keys(1)"}})
	}

	return "pgx", pgSchema(tb, dsn)
}

// pgSchema makes a schema for this test and answers with a DSN that lands in
// it.
func pgSchema(tb testing.TB, dsn string) string {
	tb.Helper()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		tb.Fatalf("%s: %v", Postgres, err)
	}
	defer db.Close()

	name := schemaName(tb.Name())
	if n := nth(tb.Name()); n > 0 {
		// A second database in one test is a second database.
		//
		// Two apps in one process is the case: an integration test that stands
		// roster up and points custody at it calls this twice, and without
		// this both got one schema -- so the second call's `DROP SCHEMA`
		// removed the first app's tables and every later read found nothing.
		// It passed on SQLite, where each call is its own file, which is
		// exactly the direction that hides a mistake.
		//
		// Counted rather than named by the caller, so that no existing test has
		// to say anything. Nothing could have wanted the old behaviour: a
		// second call always dropped what the first had made.
		name = schemaName(fmt.Sprintf("%s_%d", tb.Name(), n))
	}
	if _, err := db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, name)); err != nil {
		tb.Fatalf("drop schema %s: %v", name, err)
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE SCHEMA %q`, name)); err != nil {
		tb.Fatalf("create schema %s: %v", name, err)
	}

	tb.Cleanup(func() {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return
		}
		defer db.Close()

		// Reported rather than ignored. A schema that outlives its test is a
		// slow leak on a shared server, and the run that finds it is not the
		// run that caused it.
		if _, err := db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, name)); err != nil {
			tb.Errorf("drop schema %s: %v", name, err)
		}
	})

	u, err := url.Parse(dsn)
	if err != nil {
		tb.Fatalf("%s: %v", Postgres, err)
	}

	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()

	return u.String()
}

// schemaName is a test's name as an identifier PostgreSQL will take.
//
// Truncated to 63 bytes, which is its limit -- silently, if nobody does it
// here: a longer name is cut by the server and two tests whose names agree for
// the first 63 characters would then share a schema and each drop the other's
// tables.
func schemaName(v string) string {
	v = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}

		return '_'
	}, v)

	v = "t_" + v
	if len(v) > 63 {
		v = v[:63]
	}

	return v
}

// nth is how many databases this test has already been given.
var nth = func() func(string) int {
	var mu sync.Mutex
	seen := map[string]int{}

	return func(name string) int {
		mu.Lock()
		defer mu.Unlock()

		n := seen[name]
		seen[name] = n + 1

		return n
	}
}()
