package migrate_test

import (
	"context"
	"database/sql"
	"net/url"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	entschema "entgo.io/ent/dialect/sql/schema"
	"entgo.io/ent/schema/field"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/vfs/memdb"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/migrate"
)

// open returns an empty SQLite database that lives in memory. The migrations an
// app ships are likely PostgreSQL, but the machinery is the same, and this is
// the one database a test can have all to itself.
func open(t *testing.T) *sql.DB {
	t.Helper()

	db, err := driver.Open(memdb.TestDB(t, url.Values{"_pragma": {"foreign_keys(1)"}}))
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	return db
}

// schema is a stand-in for what an app hands over as [migrate.Migrations.Tables]
// -- two tables and an edge between them, which is the smallest thing that has
// an order the statements must come in.
//
// It is built here rather than imported from a generated package because this
// package is meant to work without knowing any app, and a test that borrowed
// one would not be able to say that.
func schema() []*entschema.Table {
	tenant := []*entschema.Column{
		{Name: "id", Type: field.TypeUUID, Unique: true},
		{Name: "alias", Type: field.TypeString, Unique: true},
	}
	holder := []*entschema.Column{
		{Name: "id", Type: field.TypeUUID, Unique: true},
		{Name: "holder_tenant", Type: field.TypeUUID},
	}

	tenants := &entschema.Table{
		Name:       "tenant",
		Columns:    tenant,
		PrimaryKey: []*entschema.Column{tenant[0]},
	}
	holders := &entschema.Table{
		Name:       "holder",
		Columns:    holder,
		PrimaryKey: []*entschema.Column{holder[0]},
		ForeignKeys: []*entschema.ForeignKey{{
			Symbol:     "holder_tenant_tenant",
			Columns:    []*entschema.Column{holder[1]},
			RefColumns: []*entschema.Column{tenant[0]},
			RefTable:   tenants,
			OnDelete:   entschema.NoAction,
		}},
	}

	return []*entschema.Table{holders, tenants}
}

// moved is [schema] after a column was added to one of its entities, which is
// what a plan made after a change to the app is given.
func moved() []*entschema.Table {
	vs := schema()
	vs[0].Columns = append(vs[0].Columns, // holder
		&entschema.Column{Name: "email", Type: field.TypeString, Nullable: true})

	return vs
}

func tableNames(ctx context.Context, t *testing.T, db *sql.DB) []string {
	t.Helper()

	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	require.NoError(t, err)
	defer rows.Close()

	vs := []string{}
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		vs = append(vs, v)
	}
	require.NoError(t, rows.Err())

	return vs
}

// TestMigrations walks one directory of files from nothing to applied, in
// order, because that is the order the properties hold in: there is nothing to
// apply until something was planned, and nothing to say about applying twice
// until it was applied once.
func TestMigrations(t *testing.T) {
	ctx := t.Context()

	dir, err := migrate.OpenDir(t.TempDir())
	require.NoError(t, err)

	m := migrate.Migrations{
		Dir:     dir,
		Dialect: dialect.SQLite,
		Tables:  schema(),
	}

	db := open(t)

	t.Run("the first plan is the whole schema", func(t *testing.T) {
		x := require.New(t)

		fs, err := m.Plan(ctx, open(t), "init")
		x.NoError(err)
		x.Len(fs, 1)
		x.Contains(fs[0].Name(), "_init.sql")
		x.Contains(string(fs[0].Bytes()), "CREATE TABLE `tenant`")
	})

	t.Run("what was planned is what is applied", func(t *testing.T) {
		x := require.New(t)

		fs, err := m.Pending(ctx, db, dialect.SQLite)
		x.NoError(err)
		x.Len(fs, 1)

		fs, err = m.Apply(ctx, db, dialect.SQLite)
		x.NoError(err)
		x.Len(fs, 1)
		x.Subset(tableNames(ctx, t, db), []string{"tenant", "holder", migrate.RevisionTable})
	})

	t.Run("applying again applies nothing", func(t *testing.T) {
		x := require.New(t)

		// Which files a database ran is recorded in the database itself, so a
		// deployment that applies on every start pays for it once.
		fs, err := m.Pending(ctx, db, dialect.SQLite)
		x.NoError(err)
		x.Empty(fs)

		fs, err = m.Apply(ctx, db, dialect.SQLite)
		x.NoError(err)
		x.Empty(fs)
	})

	t.Run("a schema that did not move plans nothing", func(t *testing.T) {
		x := require.New(t)

		fs, err := m.Plan(ctx, open(t), "noop")
		x.NoError(err)
		x.Empty(fs)
	})

	t.Run("a schema that moved plans only the difference", func(t *testing.T) {
		x := require.New(t)

		nextSecond(t)

		// The dev database is empty and every file written so far is replayed
		// onto it, so what comes out is the difference against the files and
		// not against whatever state some database happens to be in.
		u := m
		u.Tables = moved()

		fs, err := u.Plan(ctx, open(t), "add_holder_email")
		x.NoError(err)
		x.Len(fs, 1)
		x.Contains(fs[0].Name(), "_add_holder_email.sql")

		sql := string(fs[0].Bytes())
		x.Contains(sql, "email")
		x.NotContains(sql, "CREATE TABLE `tenant`")
	})
}

// TestPlanVersion has a directory of its own because the file it provokes
// cannot be taken back: nothing here or in atlas deletes a migration, so the
// directory this leaves behind is the broken one the refusal is about.
func TestPlanVersion(t *testing.T) {
	t.Run("a second plan made inside the same second is refused", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()

		dir, err := migrate.OpenDir(t.TempDir())
		x.NoError(err)

		m := migrate.Migrations{Dir: dir, Dialect: dialect.SQLite, Tables: schema()}

		_, err = m.Plan(ctx, open(t), "init")
		x.NoError(err)

		// A migration is named for the second it was written in and that name
		// is read back as its version, so a directory holding two of one second
		// cannot say which of them a database ran. Saying so here, where the
		// answer is to delete a file, beats saying it at the deployment that
		// applies them.
		u := m
		u.Tables = moved()

		fs, err := u.Plan(ctx, open(t), "add_holder_email")
		x.ErrorContains(err, "same second")
		x.ErrorContains(err, "_init.sql")
		x.Empty(fs)
	})
}

// nextSecond waits out the second the last migration was named for.
//
// It is the one thing in these tests that costs real time, and it is spent
// rather than worked around because the hazard it steps over is the one
// [migrate.Migrations.Plan] refuses: two migrations planned by a person are
// minutes apart and never meet it, two planned by a test are microseconds apart
// and always would.
func nextSecond(t *testing.T) {
	t.Helper()

	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
}

func TestDialect(t *testing.T) {
	ctx := t.Context()

	dir, err := migrate.OpenDir(t.TempDir())
	require.NoError(t, err)

	// The files here say they are PostgreSQL; the database below speaks SQLite.
	m := migrate.Migrations{
		Dir:     dir,
		Dialect: dialect.Postgres,
		Tables:  schema(),
	}

	t.Run("a database speaking another dialect is refused", func(t *testing.T) {
		x := require.New(t)

		db := open(t)

		_, err := m.Apply(ctx, db, dialect.SQLite)
		x.ErrorIs(err, migrate.ErrDialect)

		// Both are named, because the mistake is a deployment pointed at the
		// wrong kind of server and neither half alone says which way round it
		// is wrong.
		x.ErrorContains(err, dialect.Postgres)
		x.ErrorContains(err, dialect.SQLite)

		_, err = m.Pending(ctx, db, dialect.SQLite)
		x.ErrorIs(err, migrate.ErrDialect)
	})

	t.Run("and is refused before anything is written to it", func(t *testing.T) {
		x := require.New(t)

		db := open(t)

		_, err := m.Apply(ctx, db, dialect.SQLite)
		x.ErrorIs(err, migrate.ErrDialect)

		// Recording where a database stands is the first thing applying does,
		// and it is already a write. A refusal that left this behind would have
		// half-migrated the database it was refusing.
		x.NotContains(tableNames(ctx, t, db), migrate.RevisionTable)
	})
}

func TestOpenDir(t *testing.T) {
	t.Run("a file that was changed after it was written is refused", func(t *testing.T) {
		x := require.New(t)
		ctx := t.Context()

		p := t.TempDir()
		dir, err := migrate.OpenDir(p)
		x.NoError(err)

		m := migrate.Migrations{Dir: dir, Dialect: dialect.SQLite, Tables: schema()}

		fs, err := m.Plan(ctx, open(t), "init")
		x.NoError(err)
		x.Len(fs, 1)

		x.NoError(dir.WriteFile(fs[0].Name(), []byte("SELECT 1;")))

		_, err = migrate.OpenDir(p)
		x.ErrorContains(err, "atlas.sum")
	})
}

// TestCurrent is the guard on the step an upgrade is most likely to skip.
//
// payday owns some of an app's schema, so a field added upstream arrives in the
// app's ent schema the next time it generates -- and nothing about that is
// loud. It compiles, and the tests pass against a database the tests created a
// moment ago. The one place it shows is the database somebody already had.
func TestCurrent(t *testing.T) {
	ctx := t.Context()

	dir, err := migrate.OpenDir(t.TempDir())
	require.NoError(t, err)

	m := migrate.Migrations{
		Dir:     dir,
		Dialect: dialect.SQLite,
		Tables:  schema(),
	}

	db := open(t)

	t.Run("a database that has run nothing is behind", func(t *testing.T) {
		x := require.New(t)

		_, err := m.Plan(ctx, open(t), "init")
		x.NoError(err)

		err = m.Current(ctx, db, dialect.SQLite)
		x.ErrorIs(err, migrate.ErrBehind)

		// It says which files, since "behind" without them is a deployment
		// that has to go and look.
		x.Contains(err.Error(), "_init.sql")
	})

	t.Run("and is current once it has run them", func(t *testing.T) {
		x := require.New(t)

		_, err := m.Apply(ctx, db, dialect.SQLite)
		x.NoError(err)

		x.NoError(m.Current(ctx, db, dialect.SQLite))
	})

	t.Run("a database that speaks something else is refused before that", func(t *testing.T) {
		x := require.New(t)

		// Which refusal comes first matters: told "behind" about a database
		// that could not run the files anyway, a deployment would go and apply
		// them and find out the hard way.
		err := m.Current(ctx, db, dialect.Postgres)
		x.ErrorIs(err, migrate.ErrDialect)
	})
}

// TestCheck.
//
// Five cases, and the second is the one that decides whether this is usable at
// all: every database any app has ever created with `Schema.Create` has to
// pass, or the guard cannot be turned on without baselining every deployment
// first.
func TestCheck(t *testing.T) {
	ctx := t.Context()

	// A database the ent schema built, which is what `<app> init` leaves and
	// what every test in every app runs against.
	create := func(t *testing.T, vs []*entschema.Table) *sql.DB {
		t.Helper()

		db := open(t)
		m, err := entschema.NewMigrate(entsql.OpenDB(dialect.SQLite, db))
		require.NoError(t, err)
		require.NoError(t, m.Create(ctx, vs...))

		return db
	}

	t.Run("an empty database is not one to serve on", func(t *testing.T) {
		x := require.New(t)

		err := migrate.Check(ctx, open(t), dialect.SQLite, schema())
		x.ErrorIs(err, migrate.ErrDrift)

		// And it says what is missing rather than that something is. Nothing
		// has been created here, and the message reads as that.
		x.Contains(err.Error(), "CREATE TABLE")
		x.Contains(err.Error(), "tenant")
	})

	t.Run("a database the schema built is in step", func(t *testing.T) {
		x := require.New(t)

		x.NoError(migrate.Check(ctx, create(t, schema()), dialect.SQLite, schema()))
	})

	t.Run("a column the code has and the database does not is refused", func(t *testing.T) {
		x := require.New(t)

		// The upgrade that was generated and not migrated: the ent schema grew
		// a column, the database is the one from before.
		err := migrate.Check(ctx, create(t, schema()), dialect.SQLite, moved())
		x.ErrorIs(err, migrate.ErrDrift)
		x.Contains(err.Error(), "email")
	})

	t.Run("a column the database has and the code does not is left alone", func(t *testing.T) {
		x := require.New(t)

		// The other way round, which is not the same thing. An operator's extra
		// column, or one left behind by a migration that dropped a field, is
		// not a reason to refuse to serve -- and a guard that refused it would
		// be one nobody could turn on.
		x.NoError(migrate.Check(ctx, create(t, moved()), dialect.SQLite, schema()))
	})

	t.Run("a table that is gone is refused", func(t *testing.T) {
		x := require.New(t)

		db := create(t, schema())
		_, err := db.ExecContext(ctx, `DROP TABLE holder`)
		x.NoError(err)

		err = migrate.Check(ctx, db, dialect.SQLite, schema())
		x.ErrorIs(err, migrate.ErrDrift)
		x.Contains(err.Error(), "holder")
	})

	t.Run("it writes nothing, including no revision table", func(t *testing.T) {
		x := require.New(t)

		// It runs on every start, so a check that left a table behind would be
		// a check that changed the thing it was asked to look at. In particular
		// it must not create what `NewRevisions` does.
		db := open(t)
		x.Error(migrate.Check(ctx, db, dialect.SQLite, schema()))
		x.Empty(tableNames(ctx, t, db))
	})
}
