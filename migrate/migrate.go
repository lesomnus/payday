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
	"strings"

	"ariga.io/atlas/sql/migrate"
	atpostgres "ariga.io/atlas/sql/postgres"
	atsqlite "ariga.io/atlas/sql/sqlite"
	"github.com/protobuf-orm/ent/dialect"
	entsql "github.com/protobuf-orm/ent/dialect/sql"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
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

	return written(before, after)
}

// written answers with the files that are in `after` and were not in `before`,
// and refuses a directory that planning has just made unorderable.
//
// It is by name and not `after[len(before):]`, which is what this used to be,
// because `Files` answers in name order rather than in the order the files were
// written -- and a name begins with the second it was written in, so a file
// planned now sorts before one planned in the same second under an earlier
// description. The count was right for as long as no two files shared a second.
//
// That they can share one is the reason for the refusal. Atlas names a
// migration by the second it was written in and reads the same string back as
// its version, and a version is what a database records to say it ran the file;
// two files of one version are applied in whichever order their descriptions
// happen to sort in, and the revision the second writes lands on the row the
// first wrote. Nothing here can take the file back -- neither the directory nor
// atlas offers a delete -- so the answer is to say plainly which file to remove.
func written(before, after []migrate.File) ([]migrate.File, error) {
	seen := make(map[string]migrate.File, len(before))
	for _, f := range before {
		seen[f.Name()] = f
	}

	vs := []migrate.File{}
	for _, f := range after {
		if _, ok := seen[f.Name()]; ok {
			continue
		}
		for _, g := range before {
			if g.Version() == f.Version() {
				return nil, fmt.Errorf(
					"%s was written for the same second as %s, and two migrations of one version cannot be ordered: delete it and plan again in a moment",
					f.Name(), g.Name(),
				)
			}
		}

		vs = append(vs, f)
	}

	return vs, nil
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
	if len(fs) == 0 {
		// Not merely an optimization: `ExecuteN` with nothing to do is the
		// error that having nothing to do is reported as, and a deployment that
		// applies on every start is up to date almost every time.
		return nil, nil
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

// ErrBehind is a database that has not run every migration the code ships
// with.
var ErrBehind = errors.New("the database is behind the migrations this binary carries")

// Current refuses a database the code has moved on from.
//
// It is the one thing an app has to do that a framework cannot do for it, and
// the one thing it is easiest not to do. payday owns some of an app's schema:
// a field added to a holder here arrives in that app's ent schema the next time
// it generates, and **nothing about that is loud**. It compiles, the tests pass
// against a database the tests just created, and the first sign of trouble is a
// column that is not there in the one place it matters.
//
// So the sequence for an upgrade is two steps and this is what makes the second
// one impossible to skip:
//
//	pd gen                      # the schema moved
//	<app> migrate plan <name>   # write what that means as SQL, and read it
//	<app> migrate apply         # run it
//
// Called before serving, a deployment that ran the first and forgot the rest
// does not start. That is worth more than it costs: a server that answers on a
// database it disagrees with is one that fails per request, unevenly, in
// whichever handler happens to touch the column that moved.
//
// It is not the same question as "does the database hold what the ent schema
// says". This asks whether every file has run, which is what a deployment
// controls, and it is the right question for an app that keeps migration files
// -- a database somebody hand-patched into the right shape has still skipped a
// file, and the next one will be applied on top of a state nobody recorded.
//
// [Check] asks the other one, for the app that keeps no files.
func (m Migrations) Current(ctx context.Context, db *sql.DB, dialect string) error {
	fs, err := m.Pending(ctx, db, dialect)
	if err != nil {
		return err
	}
	if len(fs) == 0 {
		return nil
	}

	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.Name()
	}

	return fmt.Errorf("%w: %s", ErrBehind, strings.Join(names, ", "))
}

// ErrDrift is a database that does not hold what the ent schema describes.
var ErrDrift = errors.New("the database is not the shape this binary's schema describes")

// Check refuses a database the code has moved on from, by looking at it.
//
// It is [Migrations.Current]'s question asked the other way round, and it is
// the one an app that keeps no migration files has to ask. What it compares is
// the ent schema this binary was generated from against the database as it
// actually is -- so a deployment that ran `pd gen` after upgrading payday and
// stopped there does not start.
//
// That matters because nothing else about it is loud. A field added to a holder
// here arrives in the app's ent schema the next time it generates; it compiles,
// the tests pass against a database the tests just created, and the first sign
// of trouble is a column that is not there in the one place it matters.
//
// # What it is not
//
// Not "the database equals the schema". A column, an index or a table the app
// does not know about is left alone -- the diff is filtered to what would have
// to be **added** or widened. Which is the right policy for a guard that runs
// on every start: an operator's extra index is not a reason to refuse to serve,
// and a missing column is.
//
// # What it does not do
//
// Write. It inspects the database rather than replaying migrations, so it needs
// no dev database, no directory and no permission to change anything -- and in
// particular it does not create the revision table [NewRevisions] would.
//
// A database that [entschema.Schema.Create] built passes, which is the whole
// reason this is worth having: every app that has ever run `<app> init` is
// already in step, and none of them has to be baselined first.
//
// An empty database does not pass, and the message is a `CREATE TABLE` for
// every table there should be. That reads as what it is -- nothing has been
// created here.
func Check(ctx context.Context, db *sql.DB, dialect string, tables []*entschema.Table) error {
	// In memory, because nothing is meant to survive this: the plan is read for
	// its text and thrown away. A `LocalDir` here would leave a migration file
	// on disk every time a server started on a database it disagreed with.
	dir := &migrate.MemDir{}

	v, err := entschema.NewMigrate(entsql.OpenDB(dialect, db),
		entschema.WithDir(dir),
		// One statement per version and no down file, which is what makes the
		// text below readable. Without it the directory gets the up and the
		// down, and every difference reads as two.
		entschema.WithFormatter(migrate.DefaultFormatter),
		// Nothing to do is an error rather than an empty plan, which is the
		// only way to tell it apart from a plan that is empty for another
		// reason.
		entschema.WithErrNoPlan(true),
		// Deliberately not `WithDropColumn` or `WithDropIndex`; see above.
	)
	if err != nil {
		return fmt.Errorf("new migrate: %w", err)
	}

	switch err := v.NamedDiff(ctx, "drift", tables...); {
	case errors.Is(err, migrate.ErrNoPlan):
		return nil
	case err != nil:
		return fmt.Errorf("read the database: %w", err)
	}

	fs, err := dir.Files()
	if err != nil {
		return fmt.Errorf("read what is missing: %w", err)
	}

	return fmt.Errorf("%w:\n\n  %s", ErrDrift, strings.Join(statements(fs), "\n  "))
}

// statements is the SQL of a plan, without the comments atlas writes around it.
//
// The message is what somebody reads at three in the morning, and what they
// need from it is which change was not applied -- not a header saying which
// second the plan was made in.
func statements(fs []migrate.File) []string {
	vs := []string{}
	for _, f := range fs {
		for _, line := range strings.Split(string(f.Bytes()), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "--") {
				continue
			}

			vs = append(vs, line)
		}
	}

	return vs
}
