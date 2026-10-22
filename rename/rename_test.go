package rename_test

import (
	"database/sql"
	"testing"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/rename"
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

// has says `db` holds an index called `name`.
func has(t *testing.T, db *sql.DB, d string, name string) bool {
	t.Helper()

	q := `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`
	if d == dialect.Postgres {
		q = `SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1`
	}
	n := 0
	require.NoError(t, db.QueryRowContext(t.Context(), q, name).Scan(&n))

	return n > 0
}

// oid is what PostgreSQL knows an index by, which a rename keeps and a
// rebuild does not.
func oid(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()

	var v int64
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'i' AND n.nspname = current_schema() AND c.relname = $1`, name).Scan(&v))

	return v
}

func exec(t *testing.T, db *sql.DB, qs ...string) {
	t.Helper()

	for _, q := range qs {
		_, err := db.ExecContext(t.Context(), q)
		require.NoError(t, err, q)
	}
}

func TestIndexes(t *testing.T) {
	page := rename.Index{Table: "robot", From: "robot_tenant_id_alias", To: "robot_page"}

	t.Run("one under its old name is moved to its new", func(t *testing.T) {
		x := require.New(t)
		db, d := open(t)
		exec(t, db,
			`CREATE TABLE robot (tenant_id integer NOT NULL, alias text NOT NULL)`,
			`CREATE INDEX robot_tenant_id_alias ON robot (tenant_id, alias)`)
		was := int64(0)
		if d == dialect.Postgres {
			was = oid(t, db, page.From)
		}

		x.NoError(rename.Indexes(t.Context(), db, d, []rename.Index{page}))
		x.False(has(t, db, d, page.From), "the old name is still there")

		// PostgreSQL renames it, which is the index it was; SQLite renames
		// none, and the migration makes it under the new name.
		if d == dialect.Postgres {
			x.True(has(t, db, d, page.To))
			x.Equal(was, oid(t, db, page.To), "the index was made again rather than renamed")
		} else {
			x.False(has(t, db, d, page.To))
		}

		t.Run("and moving it again does nothing", func(t *testing.T) {
			x := require.New(t)
			x.NoError(rename.Indexes(t.Context(), db, d, []rename.Index{page}))
			x.False(has(t, db, d, page.From))
		})
	})

	t.Run("one a migration already made again goes, and the new one stays", func(t *testing.T) {
		x := require.New(t)
		db, d := open(t)
		exec(t, db,
			`CREATE TABLE robot (tenant_id integer NOT NULL, alias text NOT NULL)`,
			`CREATE INDEX robot_tenant_id_alias ON robot (tenant_id, alias)`,
			`CREATE INDEX robot_page ON robot (tenant_id, alias)`)

		x.NoError(rename.Indexes(t.Context(), db, d, []rename.Index{page}))
		x.False(has(t, db, d, page.From), "the index left beside the new one is still there")
		x.True(has(t, db, d, page.To))
	})

	t.Run("a name on another table is not this index", func(t *testing.T) {
		x := require.New(t)
		db, d := open(t)
		exec(t, db,
			`CREATE TABLE robot (tenant_id integer NOT NULL, alias text NOT NULL)`,
			`CREATE TABLE cell (tenant_id integer NOT NULL, alias text NOT NULL)`,
			`CREATE INDEX robot_tenant_id_alias ON cell (tenant_id, alias)`)

		x.NoError(rename.Indexes(t.Context(), db, d, []rename.Index{page}))
		x.True(has(t, db, d, page.From))
		x.False(has(t, db, d, page.To))
	})

	t.Run("a database that holds none of them is left as it is", func(t *testing.T) {
		x := require.New(t)
		db, d := open(t)

		x.NoError(rename.Indexes(t.Context(), db, d, []rename.Index{page}))
		x.NoError(rename.Indexes(t.Context(), db, d, nil))
	})

	t.Run("a database of another kind is refused", func(t *testing.T) {
		x := require.New(t)
		db, _ := open(t)

		x.ErrorContains(rename.Indexes(t.Context(), db, dialect.MySql, []rename.Index{page}), "mysql")
	})
}
