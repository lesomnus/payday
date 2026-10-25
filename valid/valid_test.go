package valid_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/lesomnus/otx/log"
	"github.com/protobuf-orm/ent/dialect"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/valid"
)

// open is a database of the test's own, and the dialect it speaks by ent's
// name for it.
func open(t *testing.T) (*sql.DB, string) {
	t.Helper()

	drv, dsn := pdtest.DB(t)
	db, err := sql.Open(drv, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	d := dialect.SQLite
	if drv == "pgx" {
		d = dialect.Postgres
	}

	return db, d
}

// postgres is [open] for a test of what only PostgreSQL leaves behind.
func postgres(t *testing.T) *sql.DB {
	t.Helper()

	db, d := open(t)
	if d != dialect.Postgres {
		t.Skipf("only PostgreSQL keeps an index a build did not finish; set %s", pdtest.Postgres)
	}

	return db
}

func exec(t *testing.T, db *sql.DB, qs ...string) {
	t.Helper()

	for _, q := range qs {
		_, err := db.ExecContext(t.Context(), q)
		require.NoError(t, err, q)
	}
}

// isValid says PostgreSQL holds an index called `name` and uses it.
func isValid(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	ok := false
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT i.indisvalid FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relname = $1`, name).Scan(&ok))

	return ok
}

// qualified is `name` with the test's schema, as a statement takes it.
func qualified(t *testing.T, db *sql.DB, name string) string {
	t.Helper()

	v := ""
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT format('%I.%I', current_schema(), $1::text)`, name).Scan(&v))

	return v
}

// statements is what `s` says to run, a line each.
func statements(s string) []string {
	vs := []string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ";") && !strings.HasPrefix(line, "--") {
			vs = append(vs, line)
		}
	}

	return vs
}

// logged is a context whose log is kept, and what it has been told since it
// was last asked: a record each, by its attributes.
func logged(t *testing.T) (context.Context, func() []map[string]any) {
	t.Helper()

	b := &bytes.Buffer{}
	ctx := log.Into(t.Context(), slog.New(slog.NewJSONHandler(b, nil)))

	return ctx, func() []map[string]any {
		vs := []map[string]any{}
		for d := json.NewDecoder(bytes.NewReader(b.Bytes())); d.More(); {
			v := map[string]any{}
			require.NoError(t, d.Decode(&v))
			vs = append(vs, v)
		}
		b.Reset()

		return vs
	}
}

func TestIndexes(t *testing.T) {
	// What the schema declares: the index a list of robots is read by, unique
	// within a tenant.
	tables := []*entschema.Table{{Name: "robot", Indexes: []*entschema.Index{{Name: "robot_alias", Unique: true}}}}

	// A build of it that did not finish: two robots of one tenant share an
	// alias, so the build fails and leaves the index, invalid.
	broken := func(t *testing.T, db *sql.DB) {
		t.Helper()

		exec(t, db,
			`CREATE TABLE robot (tenant integer NOT NULL, alias text NOT NULL)`,
			`INSERT INTO robot VALUES (1, 'arm'), (1, 'arm'), (2, 'arm')`)
		_, err := db.ExecContext(t.Context(), `CREATE UNIQUE INDEX CONCURRENTLY robot_alias ON robot (tenant, alias)`)
		require.ErrorContains(t, err, "could not create unique index")
		require.False(t, isValid(t, db, "robot_alias"))
	}

	t.Run("an index the schema declares that a build left invalid is refused, with what makes it again", func(t *testing.T) {
		x := require.New(t)
		db := postgres(t)
		broken(t, db)

		err := valid.Indexes(t.Context(), db, dialect.Postgres, tables)
		x.ErrorIs(err, valid.ErrInvalid)
		x.ErrorContains(err, "robot_alias, on robot")

		run := statements(err.Error())
		x.Equal([]string{
			"DROP INDEX CONCURRENTLY " + qualified(t, db, "robot_alias") + ";",
			"CREATE UNIQUE INDEX CONCURRENTLY robot_alias ON " + qualified(t, db, "robot") + " USING btree (tenant, alias);",
		}, run)

		// Which is the whole of it once the rows it would refuse are not
		// there: each runs on its own, and the index is made.
		exec(t, db, `DELETE FROM robot WHERE tenant = 1`, `INSERT INTO robot VALUES (1, 'arm')`)
		exec(t, db, run...)
		x.True(isValid(t, db, "robot_alias"))
		x.NoError(valid.Indexes(t.Context(), db, dialect.Postgres, tables))
	})

	t.Run("one the schema does not declare is said, and refuses nothing", func(t *testing.T) {
		x := require.New(t)
		db := postgres(t)
		broken(t, db)

		ctx, said := logged(t)
		x.NoError(valid.Indexes(ctx, db, dialect.Postgres, nil))
		x.NoError(valid.Indexes(ctx, db, dialect.Postgres, []*entschema.Table{{Name: "cell", Indexes: []*entschema.Index{{Name: "robot_alias"}}}}),
			"an index of that name, on another table, is not this one")

		vs := said()
		x.Len(vs, 2)
		for _, v := range vs {
			x.Equal("WARN", v["level"])
			x.Equal("robot_alias", v["index"])
			x.Equal("robot", v["table"])
			x.Equal("DROP INDEX CONCURRENTLY "+qualified(t, db, "robot_alias")+"; "+
				"CREATE UNIQUE INDEX CONCURRENTLY robot_alias ON "+qualified(t, db, "robot")+" USING btree (tenant, alias);", v["run"])
		}
	})

	t.Run("what a REINDEX CONCURRENTLY left of a rebuild is only dropped", func(t *testing.T) {
		x := require.New(t)
		db := postgres(t)
		ctx, said := logged(t)

		// An index over a function that divides by what the session says, so
		// that a rebuild in a session that says zero fails partway -- after
		// the copy it builds is in the catalog.
		conn, err := db.Conn(ctx)
		x.NoError(err)
		defer conn.Close()
		for _, q := range []string{
			`CREATE TABLE cell (x integer NOT NULL)`,
			`INSERT INTO cell SELECT g FROM generate_series(1, 100) g`,
			`CREATE FUNCTION part(v integer) RETURNS integer LANGUAGE sql IMMUTABLE AS $$ SELECT v / current_setting('valid.parts')::integer $$`,
			`SET valid.parts = '1'`,
			`CREATE INDEX cell_part ON cell (part(x))`,
			`SET valid.parts = '0'`,
		} {
			_, err := conn.ExecContext(ctx, q)
			x.NoError(err, q)
		}
		_, err = conn.ExecContext(ctx, `REINDEX INDEX CONCURRENTLY cell_part`)
		x.ErrorContains(err, "division by zero")
		x.True(isValid(t, db, "cell_part"), "the index it was rebuilding")
		x.False(isValid(t, db, "cell_part_ccnew"), "the copy it was building")

		x.NoError(valid.Indexes(ctx, db, dialect.Postgres, []*entschema.Table{{Name: "cell", Indexes: []*entschema.Index{{Name: "cell_part"}}}}),
			"the index the schema declares is the one in place")

		vs := said()
		x.Len(vs, 1)
		x.Equal("cell_part_ccnew", vs[0]["index"])
		run := "DROP INDEX CONCURRENTLY " + qualified(t, db, "cell_part_ccnew") + ";"
		x.Equal(run, vs[0]["run"], "a copy made again would be the index twice")

		exec(t, db, run)
		x.True(isValid(t, db, "cell_part"))
		x.NoError(valid.Indexes(ctx, db, dialect.Postgres, nil))
		x.Empty(said(), "nothing is left to say")
	})

	t.Run("a database that holds none is not refused", func(t *testing.T) {
		x := require.New(t)
		db, d := open(t)
		exec(t, db,
			`CREATE TABLE robot (tenant integer NOT NULL, alias text NOT NULL)`,
			`CREATE UNIQUE INDEX robot_alias ON robot (tenant, alias)`)

		ctx, said := logged(t)
		x.NoError(valid.Indexes(ctx, db, d, tables))
		x.Empty(said())
	})

	t.Run("nor is a database that is not PostgreSQL, which keeps no index a build did not finish", func(t *testing.T) {
		x := require.New(t)
		db, _ := open(t)

		x.NoError(valid.Indexes(t.Context(), db, dialect.SQLite, tables))
		x.NoError(valid.Indexes(t.Context(), db, dialect.MySql, tables), "and asks it nothing")
	})
}
