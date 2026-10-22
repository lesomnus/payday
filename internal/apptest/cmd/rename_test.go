package cmd_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/protobuf-orm/ent/dialect"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/rename"

	"github.com/lesomnus/payday/internal/apptest/cli"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	entmigrate "github.com/lesomnus/payday/internal/apptest/internal/ent/migrate"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// TestAnEarlierBuildsIndexesAreRenamedRatherThanMadeAgain: protoc-gen-orm-ent
// stores an index under the name the schema declares now, `robot_page`, where
// it stored it under ent's name for its columns, `robot_tenant_id_date_created_id`.
// A migration finds an index by name, so on a database an earlier build made,
// every one of them would be made again from every row and the old one left
// beside it. `pd.IndexRenames` is what `cli.Migrate` hands `rename.Indexes`
// first, and after it the migration finds every index where it is.
func TestAnEarlierBuildsIndexesAreRenamedRatherThanMadeAgain(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	// What the generator lists is what ent called each index of this schema
	// that now has a name of its own: its table and its columns, read off the
	// tables ent's migration is made of rather than worked out a second time.
	want := []rename.Index{}
	for _, tb := range entmigrate.Tables {
		for _, i := range tb.Indexes {
			columns := []string{}
			for _, c := range i.Columns {
				columns = append(columns, c.Name)
			}
			if was := tb.Name + "_" + strings.Join(columns, "_"); was != i.Name {
				want = append(want, rename.Index{Table: tb.Name, From: was, To: i.Name})
			}
		}
	}
	x.NotEmpty(want)
	x.ElementsMatch(want, pd.IndexRenames)

	s, err := cmd.Build(ctx, cmd.Config{Db: dbOf(t), Watch: config.WatchConfig{Broker: config.BrokerMemory}})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	// A database this build made, which has nothing to rename.
	x.NoError(cli.Migrate(ctx, s))
	made := indexesOf(t, s.Db, s.Dialect)
	for _, v := range pd.IndexRenames {
		x.Contains(made, v.To)
		x.NotContains(made, v.From)
	}

	// The same database as an earlier build left it: every index the schema
	// names under the name of its columns.
	for _, v := range pd.IndexRenames {
		storedAs(t, s.Db, s.Dialect, v.To, v.From)
	}
	was := indexesOf(t, s.Db, s.Dialect)
	for _, v := range pd.IndexRenames {
		x.Contains(was, v.From)
		x.NotContains(was, v.To)
	}

	x.NoError(cli.Migrate(ctx, s))
	got := indexesOf(t, s.Db, s.Dialect)
	x.Equal(tables(made), tables(got), "the migration left indexes beside the ones it made, or made what it should have found")
	x.NoError(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables))

	if s.Dialect == dialect.Postgres {
		// Renamed, and so the index it was: one made again would be another
		// relation, built from every row.
		for _, v := range pd.IndexRenames {
			x.Equal(was[v.From], got[v.To], "%s was made again as %s rather than renamed", v.From, v.To)
		}
	}

	t.Run("and migrating again changes nothing", func(t *testing.T) {
		x := require.New(t)

		x.NoError(cli.Migrate(ctx, s))
		x.Equal(got, indexesOf(t, s.Db, s.Dialect))
	})
}

// indexesOf is every index the database holds that is not the engine's own,
// by name: its table, and on PostgreSQL the relation it is.
func indexesOf(t *testing.T, db *sql.DB, d string) map[string]string {
	t.Helper()

	q := `SELECT name, tbl_name FROM sqlite_master WHERE type = 'index' AND name NOT LIKE 'sqlite_autoindex_%'`
	if d == dialect.Postgres {
		q = `SELECT c.relname, t.relname || '/' || c.oid::text FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			JOIN pg_class t ON t.oid = i.indrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema()`
	}

	rows, err := db.QueryContext(t.Context(), q)
	require.NoError(t, err)
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var name, at string
		require.NoError(t, rows.Scan(&name, &at))
		out[name] = at
	}
	require.NoError(t, rows.Err())

	return out
}

// tables is [indexesOf] with only the table each is on, which is what two
// databases holding the same indexes agree on.
func tables(vs map[string]string) map[string]string {
	out := make(map[string]string, len(vs))
	for k, v := range vs {
		out[k], _, _ = strings.Cut(v, "/")
	}

	return out
}

// storedAs moves the index called `name` to `as`, which is what an earlier
// build called it: renamed on PostgreSQL, and made again from its own
// statement on SQLite, which renames no index.
func storedAs(t *testing.T, db *sql.DB, d string, name string, as string) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())

	if d == dialect.Postgres {
		_, err := db.ExecContext(ctx, `ALTER INDEX "`+name+`" RENAME TO "`+as+`"`)
		require.NoError(t, err)
		return
	}

	var stmt string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&stmt))
	for _, q := range []string{"`" + name + "`", `"` + name + `"`, name} {
		if strings.Contains(stmt, q) {
			stmt = strings.Replace(stmt, q, `"`+as+`"`, 1)
			break
		}
	}

	_, err := db.ExecContext(ctx, `DROP INDEX "`+name+`"`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, stmt)
	require.NoError(t, err, stmt)
}
