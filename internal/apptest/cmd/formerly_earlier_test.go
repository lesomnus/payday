package cmd_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/protobuf-orm/ent/dialect"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/rename"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cli"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	entmigrate "github.com/lesomnus/payday/internal/apptest/internal/ent/migrate"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// What a store of an entity the schema renamed is carried into when the same
// migration moves columns too. Robot says it was called Droid (`formerly`), so
// a store an earlier build made holds the robots in `droid`, with everything
// ent named after a table named after that; and a store of a build before the
// past's and the copy's times were `date_*` holds those under the names they
// had. One `cli.Migrate` moves both -- the table first, by `rename.Tables`, and
// then the columns, by `rename.Columns`, which looks for each on its table by
// the name the table has now.

// droid is the robots' table as an earlier build made it, as
// `pd.TableRenames` holds it.
var droid = rename.Table{From: "droid", To: "robot"}

// pointed writes a robot with every edge a robot has, and a row of each table
// that points at a robot: what a move of the robots' table carries besides
// the robots.
func pointed(t *testing.T, b *built, ctx context.Context) *app.Robot {
	t.Helper()
	x := require.New(t)
	tenant := app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build()
	ref := func(v *app.Robot) *app.RobotRef { return app.RobotRef_builder{Id: v.GetId()}.Build() }

	cell, err := b.Ungated.Cell().Add(ctx, app.CellAddRequest_builder{Tenant: tenant, Alias: "floor-1"}.Build())
	x.NoError(err)
	thing, err := b.Ungated.Thing().Add(ctx, app.ThingAddRequest_builder{Alias: "thing-1"}.Build())
	x.NoError(err)
	lead, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: tenant,
		Alias:  "lead",
		Cell:   app.CellRef_builder{Id: cell.GetId()}.Build(),
		Thing:  app.ThingRef_builder{Id: thing.GetId()}.Build(),
		Keeper: "keeper-1",
	}.Build())
	x.NoError(err)
	follow, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{Tenant: tenant, Alias: "follow"}.Build())
	x.NoError(err)

	_, err = b.Ungated.Joint().Add(ctx, app.JointAddRequest_builder{Robot: ref(lead), Alias: "elbow"}.Build())
	x.NoError(err)
	_, err = b.Ungated.Reading().Add(ctx, app.ReadingAddRequest_builder{Robot: ref(lead), Celsius: 21}.Build())
	x.NoError(err)
	_, err = b.Ungated.Pairing().Add(ctx, app.PairingAddRequest_builder{Lead: ref(lead), Follow: ref(follow)}.Build())
	x.NoError(err)
	_, err = b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: tenant, Alias: "operator", Robot: ref(lead)}.Build())
	x.NoError(err)

	return lead
}

// asEarlier is `tables` as an earlier build declared them: the table `v` under
// the name it had then, and with it every name ent makes after a table -- its
// indexes, `<table>_<name>`, and the foreign keys of it and of the tables that
// point at it, `<table>_<referenced>_<edge>` -- and each column of `cs` under
// the name it had. Copied whole, so that nothing of this build's is changed.
func asEarlier(tables []*entschema.Table, v rename.Table, cs []rename.Column) []*entschema.Table {
	name := func(table string) string {
		if table == v.To {
			return v.From
		}
		return table
	}

	columns := map[*entschema.Column]*entschema.Column{}
	of := func(vs []*entschema.Column) []*entschema.Column {
		out := make([]*entschema.Column, len(vs))
		for i, c := range vs {
			out[i] = columns[c]
		}
		return out
	}

	made := map[*entschema.Table]*entschema.Table{}
	out := make([]*entschema.Table, len(tables))
	for i, tb := range tables {
		u := &entschema.Table{Name: name(tb.Name), Schema: tb.Schema, Comment: tb.Comment, Pos: tb.Pos}
		for _, c := range tb.Columns {
			d := &entschema.Column{
				Name: c.Name, Type: c.Type, SchemaType: c.SchemaType, Attr: c.Attr, Size: c.Size,
				Precision: c.Precision, Key: c.Key, Unique: c.Unique, Increment: c.Increment,
				Nullable: c.Nullable, Default: c.Default, Enums: c.Enums, Collation: c.Collation,
				Comment: c.Comment,
			}
			for _, r := range cs {
				if r.Table == tb.Name && r.To == c.Name {
					d.Name = r.From
				}
			}
			columns[c] = d
			u.Columns = append(u.Columns, d)
		}
		u.PrimaryKey = of(tb.PrimaryKey)
		for _, idx := range tb.Indexes {
			n := idx.Name
			if rest, ok := strings.CutPrefix(n, tb.Name+"_"); ok {
				n = u.Name + "_" + rest
			}
			u.Indexes = append(u.Indexes, &entschema.Index{Name: n, Unique: idx.Unique, Columns: of(idx.Columns), Annotation: idx.Annotation})
		}
		if tb.Annotation != nil {
			a := *tb.Annotation
			a.Table = name(a.Table)
			u.Annotation = &a
		}

		made[tb] = u
		out[i] = u
	}
	for _, tb := range tables {
		u := made[tb]
		for _, fk := range tb.ForeignKeys {
			symbol := fk.Symbol
			if rest, ok := strings.CutPrefix(symbol, tb.Name+"_"+fk.RefTable.Name+"_"); ok {
				symbol = u.Name + "_" + name(fk.RefTable.Name) + "_" + rest
			}
			u.ForeignKeys = append(u.ForeignKeys, &entschema.ForeignKey{
				Symbol:     symbol,
				Columns:    of(fk.Columns),
				RefTable:   made[fk.RefTable],
				RefColumns: of(fk.RefColumns),
				OnUpdate:   fk.OnUpdate,
				OnDelete:   fk.OnDelete,
			})
		}
	}

	return out
}

// copyInto writes every row `from` holds into `to`: each table of `tables`
// into the one at its place in `into`, column by column, a table after those
// it points at.
func copyInto(t *testing.T, from, to *sql.DB, d string, tables, into []*entschema.Table) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	quoted := func(vs []*entschema.Column) string {
		out := make([]string, len(vs))
		for i, c := range vs {
			out[i] = `"` + c.Name + `"`
		}
		return strings.Join(out, ", ")
	}

	done := map[*entschema.Table]bool{}
	for len(done) < len(tables) {
		n := len(done)
		for i, tb := range tables {
			ready := !done[tb]
			for _, fk := range tb.ForeignKeys {
				ready = ready && (fk.RefTable == tb || done[fk.RefTable])
			}
			if !ready {
				continue
			}
			done[tb] = true

			u := into[i]
			params := make([]string, len(u.Columns))
			for j := range params {
				params[j] = "?"
				if d == dialect.Postgres {
					params[j] = fmt.Sprintf("$%d", j+1)
				}
			}
			insert := `INSERT INTO "` + u.Name + `" (` + quoted(u.Columns) + `) VALUES (` + strings.Join(params, ", ") + `)`

			rows, err := from.QueryContext(ctx, `SELECT `+quoted(tb.Columns)+` FROM "`+tb.Name+`"`)
			x.NoError(err)
			for rows.Next() {
				vs := make([]any, len(tb.Columns))
				ps := make([]any, len(vs))
				for j := range vs {
					ps[j] = &vs[j]
				}
				x.NoError(rows.Scan(ps...))
				_, err := to.ExecContext(ctx, insert, vs...)
				x.NoError(err, insert)
			}
			x.NoError(rows.Err())
			x.NoError(rows.Close())
		}
		x.Greater(len(done), n, "tables that point at each other")
	}
}

// contentsOf is every row of each of `tables`, by its columns' names, in the
// order of its key.
func contentsOf(t *testing.T, db *sql.DB, tables []*entschema.Table) map[string][]map[string]any {
	t.Helper()
	x := require.New(t)

	out := map[string][]map[string]any{}
	for _, tb := range tables {
		key := make([]string, len(tb.PrimaryKey))
		for i, c := range tb.PrimaryKey {
			key[i] = `"` + c.Name + `"`
		}

		rows, err := db.QueryContext(t.Context(), `SELECT * FROM "`+tb.Name+`" ORDER BY `+strings.Join(key, ", "))
		x.NoError(err)
		names, err := rows.Columns()
		x.NoError(err)
		for rows.Next() {
			vs := make([]any, len(names))
			ps := make([]any, len(names))
			for i := range vs {
				ps[i] = &vs[i]
			}
			x.NoError(rows.Scan(ps...))

			row := make(map[string]any, len(names))
			for i, n := range names {
				if v, ok := vs[i].(time.Time); ok {
					vs[i] = v.UTC().Format(time.RFC3339Nano)
				}
				row[n] = vs[i]
			}
			out[tb.Name] = append(out[tb.Name], row)
		}
		x.NoError(rows.Err())
		x.NoError(rows.Close())
	}

	return out
}

// holds says the store holds a table called `name`.
func holds(t *testing.T, db *sql.DB, d string, name string) bool {
	t.Helper()

	q := `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
	if d == dialect.Postgres {
		q = `SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename = $1`
	}
	n := 0
	require.NoError(t, db.QueryRowContext(t.Context(), q, name).Scan(&n))

	return n > 0
}

// namedAfter is what in the store is still named after the table `name`, or
// points at it: on PostgreSQL a table, an index or a constraint whose name
// has it -- on 18, a column's NOT NULL among them -- and on SQLite a table or
// an index whose name has it, and a table with a foreign key to it. A foreign
// key's own name SQLite keeps in the statement of the table it is in, where
// neither it nor a migration reads it, and a move leaves it as it was.
func namedAfter(t *testing.T, db *sql.DB, d string, name string) []string {
	t.Helper()

	q := `SELECT name FROM sqlite_master WHERE type IN ('table', 'index') AND instr(name, ?1) > 0
		UNION ALL
		SELECT m.name FROM sqlite_master m, pragma_foreign_key_list(m.name) f WHERE m.type = 'table' AND f."table" = ?1`
	if d == dialect.Postgres {
		q = `SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND strpos(tablename, $1) > 0
			UNION ALL
			SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND strpos(indexname, $1) > 0
			UNION ALL
			SELECT c.conname FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
			WHERE n.nspname = current_schema() AND strpos(c.conname, $1) > 0`
	}

	rows, err := db.QueryContext(t.Context(), q, name)
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

// TestAStoreOfARenamedEntityAndOfThePastsOldTimesIsMovedWhole: a migration
// finds a table, and a column, by its name, so on a store an earlier build made
// it finds neither -- it makes the robots' table again, empty, and adds each
// time beside the old one, empty, or on SQLite makes the past's tables again
// without them -- and nothing fails. `cli.Migrate` hands `pd.TableRenames` to
// `rename.Tables` and then `pd.ColumnRenames` to `rename.Columns`, and after it
// every row reads as it was written: the robots', those that point at a
// robot, and the past's and the copy's.
func TestAStoreOfARenamedEntityAndOfThePastsOldTimesIsMovedWhole(t *testing.T) {
	x := require.New(t)
	x.Equal([]rename.Table{droid}, pd.TableRenames)
	x.Subset(pd.ColumnRenames, datesRenamed)

	// And what else an earlier build wrote down under the name reads as the
	// robots' domain: a chunk of the archive labelled with the word, a patch
	// of the trail that names the message.
	for _, v := range []func() (pdid.Domain, bool){
		func() (pdid.Domain, bool) { return pdid.DomainOf("droid") },
		func() (pdid.Domain, bool) { return pdid.Lookup("app.Droid") },
	} {
		d, ok := v()
		x.True(ok)
		x.Equal(pd.RobotDomain, d)
	}
	x.Equal("robot", pd.RobotDomain.String())

	// The rows, as this build writes them, to a store of its own.
	a, ctx := build(t)
	src := newGrants()
	lead := pointed(t, a, ctx)
	dating(t, a, ctx, src, "before")
	want := contentsOf(t, a.Db, entmigrate.Tables)
	made := indexesOf(t, a.Db, a.Dialect)
	for _, v := range []string{"robot", "holder", "joint", "machine", "pairing", "reading", "event", "fact", "entitlement"} {
		x.NotEmpty(want[v], "no row of %s to carry", v)
	}

	// And a store an earlier build made of them: its own migration, on a store
	// of nothing, and each row where it kept it.
	s, err := cmd.Build(ctx, cmd.Config{Db: dbOf(t), Watch: config.WatchConfig{Broker: config.BrokerMemory}})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	earlier := asEarlier(entmigrate.Tables, droid, datesRenamed)
	x.NoError(entmigrate.Create(ctx, entmigrate.NewSchema(s.Drv), earlier))
	copyInto(t, a.Db, s.Db, s.Dialect, entmigrate.Tables, earlier)
	x.True(holds(t, s.Db, s.Dialect, droid.From))
	x.False(holds(t, s.Db, s.Dialect, droid.To))
	for _, v := range datesRenamed {
		x.Contains(columnsOf(t, s.Db, s.Dialect, v.Table), v.From)
	}
	x.NotEmpty(namedAfter(t, s.Db, s.Dialect, droid.From))
	was := indexesOf(t, s.Db, s.Dialect)
	relation := func(table string) int64 {
		var v int64
		x.NoError(s.Db.QueryRowContext(ctx, `SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relkind = 'r' AND n.nspname = current_schema() AND c.relname = $1`, table).Scan(&v))
		return v
	}
	robots := int64(0)
	if s.Dialect == dialect.Postgres {
		robots = relation(droid.From)
	}

	x.NoError(cli.Migrate(ctx, s))
	x.NoError(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables))
	x.False(holds(t, s.Db, s.Dialect, droid.From))
	for _, v := range datesRenamed {
		x.NotContains(columnsOf(t, s.Db, s.Dialect, v.Table), v.From)
	}
	x.Equal(want, contentsOf(t, s.Db, entmigrate.Tables), "a value is not the one written before the migration")
	x.Empty(namedAfter(t, s.Db, s.Dialect, droid.From), "named after the table it was")

	got := indexesOf(t, s.Db, s.Dialect)
	x.Equal(tables(made), tables(got), "an index left beside the ones this build makes, or one not made")
	if s.Dialect == dialect.Postgres {
		// The table and each index renamed: one made again would be another
		// relation, copied or built from every row.
		x.Equal(robots, relation(droid.To), "the robots' table was made again rather than moved")
		for n, v := range was {
			if rest, ok := strings.CutPrefix(n, droid.From+"_"); ok {
				n = droid.To + "_" + rest
			}
			_, oid, _ := strings.Cut(v, "/")
			_, now, _ := strings.Cut(got[n], "/")
			x.Equal(oid, now, "%s was made again rather than moved", n)
		}
	}

	// And the app reads it, and writes the past and the copy, as it did.
	b := &built{Server: s, Tenant: a.Tenant, Holder: a.Holder}
	v, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{Ref: app.RobotRef_builder{Id: lead.GetId()}.Build()}.Build())
	x.NoError(err)
	x.Equal(lead.GetAlias(), v.GetAlias())
	x.Equal(lead.GetCell().GetId(), v.GetCell().GetId())
	dating(t, b, ctx, src, "after")

	t.Run("and migrating again changes nothing", func(t *testing.T) {
		x := require.New(t)
		now := contentsOf(t, s.Db, entmigrate.Tables)

		x.NoError(cli.Migrate(ctx, s))
		x.NoError(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables))
		x.Equal(now, contentsOf(t, s.Db, entmigrate.Tables))
		x.Equal(got, indexesOf(t, s.Db, s.Dialect))
	})
}
