// Package migrate plans and applies the versioned migrations of a database.
//
// The ent schema is the one description of what the database should look like;
// planning turns a change of it into a file of SQL statements that brings a
// database from the shape it has to the shape it should have. The files are
// kept in the app's repository, reviewed like any other code, and applied in
// order.
//
// Nothing here talks to a service or needs a tool of its own: the planning and
// the applying are both done by the atlas packages ent already depends on,
// which are Apache-2.0. The Atlas CLI, licensed separately, is not used.
//
// # What the files are written for is the app's to say
//
// A migration is SQL and SQL is not the same everywhere, so a directory of
// migration files is written for one database and not for the others -- even
// though the app itself runs on any database ent speaks. Which one that is was
// a constant in this package when this was one app's own code. A framework
// cannot hold that constant, since it would be deciding for every app that
// depends on it, so it is a field of [Migrations] and the app fills it in.
//
// The refusal that hung off that constant is the part worth keeping, and it
// moved inwards rather than out. Applying to a database that speaks something
// else runs statements meant for another dialect: at best it fails halfway
// through a schema change, at worst it succeeds and means something different.
// That check used to live in the command that called this package, where it
// protected the one caller that remembered to make it; here it protects all of
// them.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"ariga.io/atlas/sql/migrate"
	atpostgres "ariga.io/atlas/sql/postgres"
	atsqlite "ariga.io/atlas/sql/sqlite"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	entschema "entgo.io/ent/dialect/sql/schema"
)

// DefaultDir is where the migration files are kept. It is what the template
// writes and what a command falls back to when nothing said otherwise; this
// package never reads it on its own.
const DefaultDir = "migrations"

// ErrDialect is what a database speaking something other than the migrations
// are written in is refused with.
//
// It is a sentinel so that a caller can tell this apart from a database that is
// merely unreachable: one is a deployment pointed at the wrong kind of server
// and is answered by fixing the configuration, the other is answered by waiting.
var ErrDialect = errors.New("the database does not speak what the migrations are written in")

// Migrations is a directory of migration files beside what the app says about
// them -- which database they were written for, and what shape they are meant
// to arrive at.
//
// The three are one value rather than three arguments because they never differ
// within a process: an app builds this once, out of its generated ent, and the
// commands that plan and that apply are given the same one. Passing the dialect
// per call is how the caller and this package came to disagree about it before.
type Migrations struct {
	// Dir holds the files. [OpenDir] opens one and refuses it if anything in it
	// was edited after it was written.
	Dir migrate.Dir

	// Dialect is the database the files are written for, by the names ent knows
	// them by -- `dialect.Postgres`, `dialect.SQLite`.
	//
	// Nothing defaults it. A default would be a guess about SQL somebody else
	// wrote, and the whole point of this field is that only the app knows which
	// SQL that was.
	Dialect string

	// Tables is the shape the migrations bring a database to: `migrate.Tables`
	// of the app's generated ent.
	//
	// It arrives as a value and not as an import because it is the one thing
	// here that generated code owns, and payday does not know the name of the
	// package it is generated into. Only [Migrations.Plan] reads it -- applying
	// runs SQL that was already written and needs no schema.
	Tables []*entschema.Table
}

// OpenDir opens the directory of migration files at `path` and makes sure none
// of them was touched after it was written, which is what `atlas.sum` records.
func OpenDir(path string) (*migrate.LocalDir, error) {
	d, err := migrate.NewLocalDir(path)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	if err := migrate.Validate(d); err != nil {
		return nil, fmt.Errorf("%q does not match its atlas.sum: %w", path, err)
	}

	return d, nil
}

// Plan writes the migration files that bring a database to the state the ent
// schema describes, and returns the files it wrote.
//
// `dev` is a dev database: an empty database of the kind the migrations are
// written for, onto which the files that are already written are replayed to
// work out what the current state is. It is written to and emptied again, so it
// must not be a database anyone cares about.
//
// It takes no dialect. Planning is about the files, not about this deployment,
// and the files are [Migrations.Dialect] by definition -- so a dev database of
// another kind is not a choice to be honored but a mistake, and it is one that
// announces itself: the atlas driver of the dialect reads the catalog the way
// that database keeps it, and the wrong database has no such catalog.
func (m Migrations) Plan(ctx context.Context, dev *sql.DB, name string) ([]migrate.File, error) {
	before, err := m.Dir.Files()
	if err != nil {
		return nil, fmt.Errorf("read the migration directory: %w", err)
	}

	v, err := entschema.NewMigrate(entsql.OpenDB(m.Dialect, dev),
		entschema.WithDir(m.Dir),
		// One statement per version, which is what the executor reads back.
		entschema.WithFormatter(migrate.DefaultFormatter),
		// Replay what is already written instead of trusting the state the dev
		// database happens to be in.
		entschema.WithMigrationMode(entschema.ModeReplay),
		// Plan the destructive changes as well; they are reviewed before they
		// are applied, and a change that is silently skipped is worse.
		entschema.WithDropColumn(true),
		entschema.WithDropIndex(true),
	)
	if err != nil {
		return nil, fmt.Errorf("new migrate: %w", err)
	}
	if err := v.NamedDiff(ctx, name, m.Tables...); err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}

	after, err := m.Dir.Files()
	if err != nil {
		return nil, fmt.Errorf("read the migration directory: %w", err)
	}

	return after[len(before):], nil
}

// Pending returns the migration files `db` did not run yet, in the order they
// are to be applied.
func (m Migrations) Pending(ctx context.Context, db *sql.DB, dialect string) ([]migrate.File, error) {
	ex, err := m.executor(ctx, db, dialect)
	if err != nil {
		return nil, err
	}

	return pending(ctx, ex)
}

// Apply runs every migration file `db` did not run yet and returns them. A file
// is recorded as applied only once every statement in it has run.
func (m Migrations) Apply(ctx context.Context, db *sql.DB, dialect string) ([]migrate.File, error) {
	ex, err := m.executor(ctx, db, dialect)
	if err != nil {
		return nil, err
	}

	fs, err := pending(ctx, ex)
	if err != nil {
		return nil, err
	}
	if err := ex.ExecuteN(ctx, len(fs)); err != nil {
		return nil, fmt.Errorf("execute: %w", err)
	}

	return fs, nil
}

func pending(ctx context.Context, ex *migrate.Executor) ([]migrate.File, error) {
	fs, err := ex.Pending(ctx)
	if err != nil {
		// Having nothing left to run is the state a database that is up to date
		// is in, and a deployment applies on every start, so it is by far the
		// common answer rather than a failure.
		if errors.Is(err, migrate.ErrNoPendingFiles) {
			return nil, nil
		}

		return nil, fmt.Errorf("read what is pending: %w", err)
	}

	return fs, nil
}

func (m Migrations) executor(ctx context.Context, db *sql.DB, d string) (*migrate.Executor, error) {
	if d != m.Dialect {
		return nil, fmt.Errorf("%w: they are %s and it is %s", ErrDialect, m.Dialect, d)
	}

	drv, err := driver(db, d)
	if err != nil {
		return nil, err
	}

	rrw, err := NewRevisions(ctx, db, d)
	if err != nil {
		return nil, err
	}

	v, err := migrate.NewExecutor(drv, m.Dir, rrw)
	if err != nil {
		return nil, fmt.Errorf("new executor: %w", err)
	}

	return v, nil
}

// driver adapts a connection to the atlas driver of the dialect it speaks. Add
// a case to migrate another kind of database; the drivers live under
// `ariga.io/atlas/sql`.
func driver(db *sql.DB, d string) (migrate.Driver, error) {
	switch d {
	case dialect.Postgres:
		return atpostgres.Open(db)
	case dialect.SQLite:
		return atsqlite.Open(db)
	default:
		return nil, fmt.Errorf("nothing migrates a %q database yet: see payday/migrate", d)
	}
}
