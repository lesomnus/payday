package rename_test

import (
	"database/sql"
	"testing"

	"github.com/protobuf-orm/ent/dialect"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/rename"
)

// tableExists says `db` holds a table called `name`.
func tableExists(t *testing.T, db *sql.DB, d, name string) bool {
	t.Helper()

	q := `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
	if d == dialect.Postgres {
		q = `SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename = $1`
	}
	n := 0
	require.NoError(t, db.QueryRowContext(t.Context(), q, name).Scan(&n))

	return n > 0
}

// constraints is the names of the constraints of `table`, as PostgreSQL keeps
// them -- but its columns' NOT NULL, which 18 keeps as constraints and 17 does
// not ([namedAfter] holds those to the table's name).
func constraints(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), `
		SELECT c.conname FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = current_schema() AND t.relname = $1 AND c.contype <> 'n' ORDER BY c.conname`, table)
	require.NoError(t, err)
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		out = append(out, v)
	}
	require.NoError(t, rows.Err())

	return out
}

// namedAfter is every constraint and index in the schema, as PostgreSQL keeps
// them, whose name has `table`'s in it.
func namedAfter(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), `
		SELECT c.conname FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
		WHERE n.nspname = current_schema() AND strpos(c.conname, $1) > 0
		UNION ALL
		SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND strpos(indexname, $1) > 0`, table)
	require.NoError(t, err)
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		out = append(out, v)
	}
	require.NoError(t, rows.Err())

	return out
}

// tableOid is what PostgreSQL knows a table by, which a rename keeps.
func tableOid(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()

	var v int64
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' AND n.nspname = current_schema() AND c.relname = $1`, name).Scan(&v))

	return v
}

// referenced is the table `table`'s foreign keys point at, as SQLite reads
// them.
func referenced(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), `SELECT "table" FROM pragma_foreign_key_list(?)`, table)
	require.NoError(t, err)
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		out = append(out, v)
	}
	require.NoError(t, rows.Err())

	return out
}

// ofAnEarlierBuild is what an earlier build made of an entity this one calls
// `cell`, as ent made it: its table under the old name, its indexes and its
// foreign key named after that, and another table's foreign key to it named
// after both. Rows in each.
func ofAnEarlierBuild(t *testing.T, db *sql.DB) {
	t.Helper()

	exec(t, db,
		`CREATE TABLE tenant (id text NOT NULL, PRIMARY KEY (id))`,
		`CREATE TABLE workcell (id text NOT NULL, host text NOT NULL, tenant_id text NOT NULL, PRIMARY KEY (id),
			CONSTRAINT workcell_tenant_tenant FOREIGN KEY (tenant_id) REFERENCES tenant (id) ON DELETE NO ACTION)`,
		`CREATE INDEX workcell_page ON workcell (tenant_id, id)`,
		`CREATE UNIQUE INDEX workcell_host ON workcell (host)`,
		`CREATE TABLE robot (id text NOT NULL, cell_id text NULL, PRIMARY KEY (id),
			CONSTRAINT robot_workcell_cell FOREIGN KEY (cell_id) REFERENCES workcell (id) ON DELETE SET NULL)`,
		`INSERT INTO tenant (id) VALUES ('t')`,
		`INSERT INTO workcell (id, host, tenant_id) VALUES ('a', 'a.example', 't'), ('b', 'b.example', 't')`,
		`INSERT INTO robot (id, cell_id) VALUES ('r', 'a')`)
}

func TestTables(t *testing.T) {
	cell := rename.Table{From: "workcell", To: "cell"}

	t.Run("one under its old name is moved to its new, with its rows and what is named after it", func(t *testing.T) {
		x := require.New(t)
		db, d := open(t)
		ofAnEarlierBuild(t, db)
		was := int64(0)
		if d == dialect.Postgres {
			was = tableOid(t, db, "workcell")
		}

		x.NoError(rename.Tables(t.Context(), db, d, []rename.Table{cell}))
		x.False(tableExists(t, db, d, "workcell"))
		x.True(tableExists(t, db, d, "cell"))
		x.Equal(map[string]string{"a": "a.example", "b": "b.example"}, valuesOf(t, db, "cell", "host"))
		x.Equal(map[string]string{"r": "a"}, valuesOf(t, db, "robot", "cell_id"))

		if d == dialect.Postgres {
			x.Equal(was, tableOid(t, db, "cell"), "the table was made again rather than renamed")
			x.True(has(t, db, d, "cell_page"))
			x.True(has(t, db, d, "cell_host"))
			x.True(has(t, db, d, "cell_pkey"))
			x.False(has(t, db, d, "workcell_page"))
			x.False(has(t, db, d, "workcell_host"))
			x.ElementsMatch([]string{"cell_pkey", "cell_tenant_tenant"}, constraints(t, db, "cell"))
			x.ElementsMatch([]string{"robot_pkey", "robot_cell_cell"}, constraints(t, db, "robot"))
			// Nor anything else -- on 18, the NOT NULL of each of its columns.
			x.Empty(namedAfter(t, db, "workcell"), "named after the table it was")
		} else {
			// SQLite renames no index: the migration makes them again.
			x.False(has(t, db, d, "workcell_page"))
			x.False(has(t, db, d, "workcell_host"))
			x.Equal([]string{"cell"}, referenced(t, db, "robot"), "what pointed at it points at it still")
		}

		// The foreign key still holds, under whichever name.
		_, err := db.ExecContext(t.Context(), `INSERT INTO robot (id, cell_id) VALUES ('s', 'nowhere')`)
		x.Error(err, "a robot in a cell there is not")
		exec(t, db, `INSERT INTO robot (id, cell_id) VALUES ('s', 'b')`)

		t.Run("and moving it again does nothing", func(t *testing.T) {
			x := require.New(t)
			x.NoError(rename.Tables(t.Context(), db, d, []rename.Table{cell}))
			x.True(tableExists(t, db, d, "cell"))
			x.Equal(map[string]string{"a": "a.example", "b": "b.example"}, valuesOf(t, db, "cell", "host"))
		})
	})

	// What a migration that ran first leaves: the new table made, empty,
	// beside the old one that holds the rows.
	t.Run("one a migration already made again, empty, goes, and the old one is moved onto its name", func(t *testing.T) {
		x := require.New(t)
		db, d := open(t)
		ofAnEarlierBuild(t, db)
		exec(t, db, `CREATE TABLE cell (id text NOT NULL, host text NOT NULL, tenant_id text NOT NULL, PRIMARY KEY (id))`)

		x.NoError(rename.Tables(t.Context(), db, d, []rename.Table{cell}))
		x.False(tableExists(t, db, d, "workcell"))
		x.Equal(map[string]string{"a": "a.example", "b": "b.example"}, valuesOf(t, db, "cell", "host"))
	})

	// What a build before leaves when it runs again once this one moved the
	// table: the old one made again, empty, beside the one with the rows.
	t.Run("an old one a build before made again, empty, goes, and nothing is moved", func(t *testing.T) {
		x := require.New(t)
		db, d := open(t)
		ofAnEarlierBuild(t, db)
		x.NoError(rename.Tables(t.Context(), db, d, []rename.Table{cell}))
		exec(t, db, `CREATE TABLE workcell (id text NOT NULL, PRIMARY KEY (id))`)

		x.NoError(rename.Tables(t.Context(), db, d, []rename.Table{cell}))
		x.False(tableExists(t, db, d, "workcell"))
		x.Equal(map[string]string{"a": "a.example", "b": "b.example"}, valuesOf(t, db, "cell", "host"))
	})

	t.Run("one with rows under both names is refused, and nothing is changed", func(t *testing.T) {
		x := require.New(t)
		db, d := open(t)
		ofAnEarlierBuild(t, db)
		exec(t, db,
			`CREATE TABLE cell (id text NOT NULL, host text NOT NULL, tenant_id text NOT NULL, PRIMARY KEY (id))`,
			`INSERT INTO cell (id, host, tenant_id) VALUES ('c', 'c.example', 't')`)

		x.ErrorIs(rename.Tables(t.Context(), db, d, []rename.Table{cell}), rename.ErrBoth)
		x.True(tableExists(t, db, d, "workcell"))
		x.Equal(map[string]string{"a": "a.example", "b": "b.example"}, valuesOf(t, db, "workcell", "host"))
		x.Equal(map[string]string{"c": "c.example"}, valuesOf(t, db, "cell", "host"))
		x.True(has(t, db, d, "workcell_page"))
	})

	t.Run("a database that holds none of them is left as it is", func(t *testing.T) {
		x := require.New(t)
		db, d := open(t)

		x.NoError(rename.Tables(t.Context(), db, d, []rename.Table{cell}))
		x.NoError(rename.Tables(t.Context(), db, d, nil))
		x.False(tableExists(t, db, d, "cell"))
	})

	t.Run("a database of another kind is refused", func(t *testing.T) {
		x := require.New(t)
		db, _ := open(t)

		x.ErrorContains(rename.Tables(t.Context(), db, dialect.MySql, []rename.Table{cell}), "mysql")
	})
}

// TestTablesGoFirst: Columns and Indexes are handed each table by the name it
// has now, so a table still under the name its entity had holds nothing they
// find. Run after Tables, each moves what it was handed. Run before it,
// neither moves anything, and the table goes on to its new name with the
// column and the index under their old names -- which the migration then adds
// and makes again beside them.
func TestTablesGoFirst(t *testing.T) {
	cell := rename.Table{From: "workcell", To: "cell"}
	host := rename.Column{Table: "cell", From: "host", To: "hostname"}
	page := rename.Index{Table: "cell", From: "cell_tenant_id_id", To: "cell_page"}

	// With the index under the name of its columns, as a build before
	// protoc-gen-orm-ent#15 made it.
	earlier := func(t *testing.T) (*sql.DB, string) {
		db, d := open(t)
		ofAnEarlierBuild(t, db)
		exec(t, db, `DROP INDEX workcell_page`, `CREATE INDEX workcell_tenant_id_id ON workcell (tenant_id, id)`)
		return db, d
	}

	t.Run("after it, the column and the index are moved", func(t *testing.T) {
		x := require.New(t)
		db, d := earlier(t)

		x.NoError(rename.Tables(t.Context(), db, d, []rename.Table{cell}))
		x.NoError(rename.Columns(t.Context(), db, d, []rename.Column{host}))
		x.NoError(rename.Indexes(t.Context(), db, d, []rename.Index{page}))
		x.Contains(columnsOf(t, db, d, "cell"), "hostname")
		x.NotContains(columnsOf(t, db, d, "cell"), "host")
		x.Equal(map[string]string{"a": "a.example", "b": "b.example"}, valuesOf(t, db, "cell", "hostname"))
		x.False(has(t, db, d, "cell_tenant_id_id"))
		if d == dialect.Postgres {
			x.True(has(t, db, d, "cell_page"))
		}
	})

	t.Run("before it, neither finds anything", func(t *testing.T) {
		x := require.New(t)
		db, d := earlier(t)

		x.NoError(rename.Columns(t.Context(), db, d, []rename.Column{host}))
		x.NoError(rename.Indexes(t.Context(), db, d, []rename.Index{page}))
		x.NoError(rename.Tables(t.Context(), db, d, []rename.Table{cell}))
		x.Contains(columnsOf(t, db, d, "cell"), "host")
		x.NotContains(columnsOf(t, db, d, "cell"), "hostname")
		if d == dialect.Postgres {
			x.True(has(t, db, d, "cell_tenant_id_id"), "named after the table now, and still after its columns")
			x.False(has(t, db, d, "cell_page"))
		}
	})
}
