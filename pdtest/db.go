package pdtest

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
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
//
// # Why the SQLite half waits rather than refusing
//
// SQLite takes one writer at a time and, left alone, a connection that finds
// the database busy **fails the query** instead of waiting for it. Which is
// fine for a test that makes one call at a time and is not fine for any test
// with a stream in it: a server-streaming handler reads while the test writes,
// and the read comes back `database is locked` from a database that is working
// perfectly.
//
// What that produces is the worst kind of failure -- one that appears in a
// tenth of runs, in whichever test happens to overlap, and says nothing about
// the code under test. `busy_timeout` makes the second connection wait, which
// is what every other database does and what the PostgreSQL half already did.
//
// Five seconds because it is far longer than any contention a test creates and
// far shorter than a suite's own patience, so a genuine deadlock still fails
// rather than hanging until CI gives up.
//
// # And the contention waiting cannot cover
//
// `busy_timeout` is the answer for a reader holding a transaction open, which
// was the shape that paragraph was written about. It is not the answer for
// **two writers**: `BEGIN DEFERRED` takes the read lock first and asks for the
// write lock later, and when two of those overlap SQLite returns SQLITE_BUSY
// without invoking the busy handler at all, because waiting could deadlock.
// Which is the ordinary shape of a test that makes two calls at once, and it
// failed one run in three of an app's suite with `database is locked` from a
// database working perfectly.
//
// So the DSN asks for `_txlock=immediate` as well, which takes the write lock
// up front where there is nothing to deadlock. [config.DbConfig.Open] adds the
// same thing to any SQLite DSN that does not say it, so a test reaching the
// database through the app's own configuration was already covered; this is for
// the tests that open the DSN themselves, and so that the harness and a
// deployment are the same shape. `_timefmt=unixepoch_nano` is here for the same
// reason; see config's timesAsIntegers for why Open asks for it.
func DB(tb testing.TB) (string, string) {
	tb.Helper()

	dsn := os.Getenv(Postgres)
	if dsn == "" {
		return "sqlite3", memdb.TestDB(tb, url.Values{
			"_pragma": {
				"foreign_keys(1)",
				"busy_timeout(5000)",
			},
			"_txlock":  {"immediate"},
			"_timefmt": {"unixepoch_nano"},
		})
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

	// A second database in one test is a second database.
	//
	// Two apps in one process is the case: an integration test that stands one
	// app up and points another at it calls this twice, and without this both
	// got one schema -- so the second call's `DROP SCHEMA` removed the first
	// app's tables and every later read found nothing. It passed on SQLite,
	// where each call is its own file, which is exactly the direction that
	// hides a mistake.
	//
	// Counted rather than named by the caller, so that no existing test has to
	// say anything. Nothing could have wanted the old behaviour: a second call
	// always dropped what the first had made.
	name := schemaName(testPackage(), tb.Name(), nth(tb.Name()))
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

// schemaName is a test's name, and which of its databases this is, as an
// identifier PostgreSQL will take unquoted: `t_`, the name lower-cased with
// everything else made `_`, `_` and eight hex digits, and the counter after.
//
//	t_testseconddbisaseconddatabase_16e0a029
//	t_testseconddbisaseconddatabase_16e0a029_1
//
// The name is there for a person: a schema left behind says which test left it.
// The digits are there so that the schema is this test's alone: the first
// eight of the SHA-256 of the test binary's package and the test's whole name,
// as `testing` has it before any of the mapping.
//
// # Two tests whose names agree for 63 bytes shared a schema
//
// The name used to be all of it, cut to PostgreSQL's 63 bytes, and this comment
// said two tests whose names agree that far still collide and that that was the
// limit doing what it does. It happened in an app, across two packages: one
// holds TestAnAddressSomebodyErasedNeverProvedKeepsNobodyFromArriving and the
// other ...FromArrivingHereEither, named apart from the first on purpose, and
// `t_` with the first sixty-one bytes of either is the whole sixty-three. `go
// test ./...` runs packages side by side against one server, each test drops its
// schema as it starts and as it ends, and whichever lost found its tables gone
// -- `relation "tenant" does not exist`, `no schema has been selected to create
// in` -- in CI, twice in a day, after being green by timing alone. Nothing in
// any of it names a schema, let alone says it was shared.
//
// Cutting was not the only way there. Two tests of one name in two packages
// collide however short it is, and so do two names the mapping makes one --
// `TestA/b` and `TestA_b`, `TestAB` and `TestAb`. So the digits are always
// there, not only when the name is cut: one rule, and it covers all three.
// The 63 bytes are `t_`, the name, the nine of `_` and the digits, and the
// counter; the name is what gives way.
//
// The package is the import path the test binary's build information carries
// ([testPackage]), so two packages are two however alike their last elements
// are. What can still collide is two names whose hashes agree in 32 bits.
//
// # The counter is appended after the truncation, and has to be
//
// It used to be part of what was truncated -- `schemaName(name + "_1")` -- and
// that put back the very bug the counter exists to fix, for any test whose name
// is already at the limit: both calls truncate to the same 63 characters, so an
// app and the second app beside it get one schema and the second `DROP` takes
// the first one's tables. It came back as `Role already exists` in an app
// standing up two planes from one test whose name ran long, which is a message
// about the app and not about here.
//
// So the room is reserved: the name gives way to the counter rather than the
// other way round, and two **databases of one test** cannot share a schema.
// The digits are of the test, not of the database, so a test's two schemas
// share them and differ in the counter.
func schemaName(pkg, v string, n int) string {
	sum := sha256.Sum256([]byte(pkg + "\x00" + v))
	tag := "_" + hex.EncodeToString(sum[:])[:8]

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

	nth := ""
	if n > 0 {
		nth = fmt.Sprintf("_%d", n)
	}
	if len(v)+len(tag)+len(nth) > 63 {
		v = v[:63-len(tag)-len(nth)]
	}

	return v + tag + nth
}

// testPackage is the import path of the package this test binary tests.
//
// `go test` builds a binary per package, and its build information's Path is
// that package's import path with `.test` after it -- for a package of this
// module and of an app's, and for an external `_test` package, which shares
// its package's binary. Where there is no build information, the binary's own
// name is `<package>.test`, which is the last element only: two packages
// whose last elements agree would then share digits, and only their test names
// keep them apart.
var testPackage = sync.OnceValue(func() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Path != "" {
		return strings.TrimSuffix(bi.Path, ".test")
	}

	v := filepath.Base(os.Args[0])
	v = strings.TrimSuffix(v, ".exe")
	return strings.TrimSuffix(v, ".test")
})

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
