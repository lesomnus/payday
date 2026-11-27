package pdtest

import (
	"database/sql"
	"os"
	"regexp"
	"strings"
	"sync"
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

// TestSchemaNameKeepsTheCounter, which is what a long test name took away.
//
// The counter used to be part of what was truncated, so a test whose name was
// already at the limit got one schema for both its databases -- and the second
// `DROP SCHEMA` took the first one's tables, which is the bug the counter
// exists to prevent, back for exactly the tests most likely to hit it. An
// integration test that stands two apps up has a long name because it says what
// it does.
func TestSchemaNameKeepsTheCounter(t *testing.T) {
	x := require.New(t)

	const pkg = "example.com/app/server"
	short := "TestShort"
	x.Regexp(`^t_testshort_[0-9a-f]{8}$`, schemaName(pkg, short, 0))
	x.Equal(schemaName(pkg, short, 0)+"_1", schemaName(pkg, short, 1))

	// Long enough that the whole of it does not fit, which is where this went
	// wrong: both calls truncated to the same thing.
	long := strings.Repeat("a", 80)
	first, second := schemaName(pkg, long, 0), schemaName(pkg, long, 1)

	x.Regexp(`^t_a+_[0-9a-f]{8}$`, first)
	x.Len(first, 63, "a name at the limit is the limit")
	x.Len(second, 63)
	x.NotEqual(first, second, "two databases of one test got one schema")
	x.True(strings.HasSuffix(second, "_1"), "the counter was truncated away")
}

// TestSchemaNameIsTheTestsAlone holds the names that used to collide apart.
//
// An app had TestAnAddressSomebodyErasedNeverProvedKeepsNobodyFromArriving in
// one package and the same name with HereEither after it in another; cut to 63
// bytes both were one schema, and `go test ./...`, running the two packages
// side by side against one server, had each drop the other's tables. The same
// name in two packages collides however short it is, and so do two names the
// mapping to an identifier makes one.
func TestSchemaNameIsTheTestsAlone(t *testing.T) {
	const pkg = "example.com/app/server"
	const erased = "TestAnAddressSomebodyErasedNeverProvedKeepsNobodyFromArriving"

	for _, tc := range []struct {
		why     string
		pkgA, a string
		pkgB, b string
	}{
		{"two names that agree for 63 bytes", pkg, erased, pkg, erased + "HereEither"},
		{"the same two, in two packages", "example.com/app/server", erased, "example.com/app/cli", erased + "HereEither"},
		{"one name in two packages", "example.com/app/server", "TestA", "example.com/app/cli", "TestA"},
		{"one last element in two packages", "example.com/app/server", "TestA", "example.com/lib/server", "TestA"},
		{"a subtest and a test the mapping makes one", pkg, "TestA/b", pkg, "TestA_b"},
		{"two cases the mapping makes one", pkg, "TestAB", pkg, "TestAb"},
	} {
		t.Run(tc.why, func(t *testing.T) {
			x := require.New(t)
			for n := range 3 {
				x.NotEqual(schemaName(tc.pkgA, tc.a, n), schemaName(tc.pkgB, tc.b, n))
			}
		})
	}

	t.Run("and a name for a person to read", func(t *testing.T) {
		x := require.New(t)
		v := schemaName(pkg, erased, 0)
		x.True(strings.HasPrefix(v, "t_testanaddresssomebodyerasedneverproved"), v)
	})
}

// TestSchemaNameIsAnIdentifier: whatever the test is called, what comes out is
// 63 bytes or fewer and a name PostgreSQL takes without quotes, so that it is
// the name the server keeps and not one it cuts or folds.
func TestSchemaNameIsAnIdentifier(t *testing.T) {
	x := require.New(t)

	unquoted := regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	for _, name := range []string{
		"TestShort",
		"TestA/b",
		"TestA/with_spaces_and-dashes.and.dots",
		"TestÜnïcödé/한국어",
		strings.Repeat("TestLong", 25), // 200 bytes
		strings.Repeat("é", 100),       // 200 bytes, none of them ASCII
	} {
		for _, n := range []int{0, 1, 12, 1234} {
			v := schemaName("example.com/app/server", name, n)
			x.LessOrEqual(len(v), 63, "%q, %d: %q", name, n, v)
			x.Regexp(unquoted, v, "%q, %d", name, n)
		}
	}
}

// TestTestPackageIsThisPackage: the package that goes into the digits is the
// one under test, by its whole import path, so that two packages of one last
// element are two.
func TestTestPackageIsThisPackage(t *testing.T) {
	require.Equal(t, "github.com/lesomnus/payday/pdtest", testPackage())
}

// TestTwoTestsOfAgreeingNamesKeepTheirTables is the collision itself, on a
// server: two tests whose names agree for well past 63 bytes, at once, each
// with a table, and neither finding the other's or losing its own.
//
// On SQLite every call was always a database of its own, so this cannot fail
// there; under [Postgres] it is the regression test. Before the digits both got
// one schema, and whichever reached the server second dropped what the first
// had made.
func TestTwoTestsOfAgreeingNamesKeepTheirTables(t *testing.T) {
	const agreed = "two_tests_whose_names_agree_for_far_longer_than_postgres_lets_a_schema_name_run"

	var made, looked sync.WaitGroup
	made.Add(2)
	looked.Add(2)
	// The tables are `kept_left` and `kept_right`: `left` and `right` alone are
	// words PostgreSQL keeps for itself.
	for _, side := range []string{"left", "right"} {
		table := "kept_" + side
		t.Run(agreed+"_"+side, func(t *testing.T) {
			t.Parallel()
			x := require.New(t)

			// Each released once on every way out, so that one failing does not
			// leave the other waiting for it.
			madeDone, lookedDone := sync.OnceFunc(made.Done), sync.OnceFunc(looked.Done)
			defer madeDone()
			defer lookedDone()

			drv, dsn := DB(t)
			db, err := sql.Open(drv, dsn)
			x.NoError(err)
			defer db.Close()

			_, err = db.Exec(`CREATE TABLE ` + table + ` (id INTEGER PRIMARY KEY)`)
			x.NoError(err)

			// Both schemas exist and both tables are made before either looks.
			madeDone()
			made.Wait()

			var n int
			x.NoError(db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n))
			other := map[string]string{"kept_left": "kept_right", "kept_right": "kept_left"}[table]
			x.Error(db.QueryRow(`SELECT COUNT(*) FROM `+other).Scan(&n), "%s is reachable from %s's database", other, table)

			// Neither drops its schema until both have looked.
			lookedDone()
			looked.Wait()
		})
	}
}
