package rename

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/protobuf-orm/ent/dialect"
)

// Table is one table an earlier build made under another name: an entity's,
// which the schema renamed and says it did (`(payday.entity).formerly`).
type Table struct {
	// From is the name an earlier build made it under, and To the one this
	// build makes it under.
	From string
	To   string
}

// ErrBoth is a database that holds a table under both its names, each with
// rows in it: there is nothing to say which of them is the table.
var ErrBoth = errors.New("the table is there under both its names, with rows in each")

// Tables moves each table of `vs` that `db` holds under its old name to its
// new one, with its rows and what is named after it, and does nothing for one
// it does not hold. So it is the same call on a database made by any earlier
// build, by this one, and on an empty one, as often as it is made.
//
// # Why before the migration
//
// Because the migration would not move it. `Schema.Create` makes the tables
// the schema declares and finds missing, and drops none: for an entity that
// changed its name it makes the new table, empty, and the rows stay in the old
// one, which nothing reads again. Nothing fails.
//
// # What is moved with it
//
// What ent named after the table: its indexes, `<table>_<name>`; its primary
// key on PostgreSQL, `<table>_pkey`; its foreign keys, `<table>_<other>_<edge>`;
// and the foreign keys of other tables that point at it, `<other>_<table>_<edge>`.
// Under the old names a migration finds none of them -- it makes each index
// again from every row, beside the old one, and on PostgreSQL drops each
// foreign key and adds it again, which checks every row.
//
// On PostgreSQL every one of those is a change to the catalog, made in one
// transaction with the table's: nothing is copied or built, and every row and
// every index is the one it was. A name that would be longer than PostgreSQL
// keeps is left as it is, since ent makes such a name of a digest this cannot
// guess, and the migration makes that one again.
//
// SQLite renames the table in place and rewrites what points at it, but it
// renames no index and no constraint. So the old indexes are dropped and the
// migration makes them under the new names, from the rows. The foreign keys
// keep their old names in the definitions of the tables they are in, where
// nothing reads them: SQLite knows a foreign key by what it points at, which
// the rename rewrote, and a migration compares none by its name -- so none of
// those tables is made again for it.
//
// # Both names
//
// A table held under both names is what a migration that ran first leaves:
// the new table made beside the old, empty. That one is dropped, and the old
// one moved onto its name. A database whose new table holds rows as well --
// a build that renamed it, and one before it, both wrote to it -- is refused
// with [ErrBoth], and nothing is changed: which rows are the table's is for
// somebody to decide.
//
// It is run before a migration, by one process at a time, as the migration
// is, and before [Columns] and [Indexes], which look for what they move on
// the table by the name it has now: a column an earlier build named otherwise,
// and an index it named after its columns, are on the old table until this
// has moved it.
func Tables(ctx context.Context, db *sql.DB, d string, vs []Table) error {
	if len(vs) == 0 {
		return nil
	}

	var t tables
	switch d {
	case dialect.Postgres:
		t = postgres{}
	case dialect.SQLite:
		t = sqlite{}
	default:
		return fmt.Errorf("nothing here renames the tables of a %q database", d)
	}

	for _, v := range vs {
		if err := table(ctx, db, t, v); err != nil {
			return fmt.Errorf("table %s, which this build calls %s: %w", v.From, v.To, err)
		}
	}

	return nil
}

// tables is what moving a table takes on one kind of database.
type tables interface {
	// exists says the schema the migration works in holds a table called
	// `name`.
	exists(ctx context.Context, q querier, name string) (bool, error)

	// drop takes out the empty table a migration made under the new name.
	drop(ctx context.Context, q querier, name string) error

	// move renames the table, and what it can of what is named after it.
	move(ctx context.Context, q querier, v Table) error
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func table(ctx context.Context, db *sql.DB, t tables, v Table) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	from, err := t.exists(ctx, tx, v.From)
	if err != nil {
		return err
	}
	if !from {
		// Never made, or moved already.
		return nil
	}

	to, err := t.exists(ctx, tx, v.To)
	if err != nil {
		return err
	}
	if to {
		n := 0
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+quote(v.To)).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: %s and %s; move what one of them holds into the other, drop it, and migrate again", ErrBoth, v.From, v.To)
		}
		if err := t.drop(ctx, tx, v.To); err != nil {
			return fmt.Errorf("the empty %s a migration made beside it: %w", v.To, err)
		}
	}

	if err := t.move(ctx, tx, v); err != nil {
		return err
	}

	return tx.Commit()
}

// renamed is `name` with the table's old name at its start made the new one,
// and false for a name that does not begin with it -- which is no name ent
// made after the table.
func renamed(name string, v Table) (string, bool) {
	rest, ok := strings.CutPrefix(name, v.From+"_")
	if !ok {
		return "", false
	}

	return v.To + "_" + rest, true
}

// postgresMax is the longest name PostgreSQL keeps whole; it drops the rest
// without a word.
const postgresMax = 63

type postgres struct{}

func (postgres) exists(ctx context.Context, q querier, name string) (bool, error) {
	n := 0
	err := q.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename = $1`, name).Scan(&n)
	return n > 0, err
}

func (postgres) drop(ctx context.Context, q querier, name string) error {
	// What points at it is what a migration pointed at it, which the next
	// one makes again at the table moved here.
	_, err := q.ExecContext(ctx, "DROP TABLE "+quote(name)+" CASCADE")
	return err
}

func (p postgres) move(ctx context.Context, q querier, v Table) error {
	if _, err := q.ExecContext(ctx, "ALTER TABLE "+quote(v.From)+" RENAME TO "+quote(v.To)); err != nil {
		return err
	}

	// Its indexes, the primary key's among them: renaming the index of a
	// constraint renames the constraint.
	idx, err := names(ctx, q,
		`SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND tablename = $1`, v.To)
	if err != nil {
		return fmt.Errorf("its indexes: %w", err)
	}
	for _, n := range idx {
		to, ok := renamed(n, v)
		if !ok || len(to) > postgresMax {
			continue
		}
		if _, err := q.ExecContext(ctx, "ALTER INDEX "+quote(n)+" RENAME TO "+quote(to)); err != nil {
			return fmt.Errorf("index %s: %w", n, err)
		}
	}

	// Its own constraints that are no index's: its foreign keys, and a check.
	own, err := names(ctx, q, `
		SELECT c.conname FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = current_schema() AND t.relname = $1 AND c.contype IN ('f', 'c')`, v.To)
	if err != nil {
		return fmt.Errorf("its constraints: %w", err)
	}
	for _, n := range own {
		to, ok := renamed(n, v)
		if !ok || len(to) > postgresMax {
			continue
		}
		if _, err := q.ExecContext(ctx, "ALTER TABLE "+quote(v.To)+" RENAME CONSTRAINT "+quote(n)+" TO "+quote(to)); err != nil {
			return fmt.Errorf("constraint %s: %w", n, err)
		}
	}

	// And the foreign keys of the tables that point at it, which ent names
	// after both.
	rows, err := q.QueryContext(ctx, `
		SELECT t.relname, c.conname FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_class r ON r.oid = c.confrelid
		JOIN pg_namespace n ON n.oid = r.relnamespace
		WHERE n.nspname = current_schema() AND r.relname = $1 AND c.contype = 'f' AND c.conrelid <> c.confrelid`, v.To)
	if err != nil {
		return fmt.Errorf("what points at it: %w", err)
	}
	type fk struct{ table, name string }
	fks := []fk{}
	for rows.Next() {
		var v fk
		if err := rows.Scan(&v.table, &v.name); err != nil {
			rows.Close()
			return fmt.Errorf("what points at it: %w", err)
		}
		fks = append(fks, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("what points at it: %w", err)
	}
	for _, f := range fks {
		rest, ok := strings.CutPrefix(f.name, f.table+"_"+v.From+"_")
		if !ok {
			continue
		}
		to := f.table + "_" + v.To + "_" + rest
		if len(to) > postgresMax {
			continue
		}
		if _, err := q.ExecContext(ctx, "ALTER TABLE "+quote(f.table)+" RENAME CONSTRAINT "+quote(f.name)+" TO "+quote(to)); err != nil {
			return fmt.Errorf("constraint %s of %s: %w", f.name, f.table, err)
		}
	}

	return nil
}

type sqlite struct{}

func (sqlite) exists(ctx context.Context, q querier, name string) (bool, error) {
	n := 0
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return n > 0, err
}

func (sqlite) drop(ctx context.Context, q querier, name string) error {
	_, err := q.ExecContext(ctx, "DROP TABLE "+quote(name))
	return err
}

func (sqlite) move(ctx context.Context, q querier, v Table) error {
	// SQLite rewrites the foreign keys of other tables to name the table by
	// its new name, as it renames it.
	if _, err := q.ExecContext(ctx, "ALTER TABLE "+quote(v.From)+" RENAME TO "+quote(v.To)); err != nil {
		return err
	}

	// It renames no index: the migration makes each again under its new name.
	// One SQLite made for a constraint has no statement, and is not ent's to
	// name.
	idx, err := names(ctx, q, `SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND sql IS NOT NULL`, v.To)
	if err != nil {
		return fmt.Errorf("its indexes: %w", err)
	}
	for _, n := range idx {
		if _, ok := renamed(n, v); !ok {
			continue
		}
		if _, err := q.ExecContext(ctx, "DROP INDEX "+quote(n)); err != nil {
			return fmt.Errorf("index %s: %w", n, err)
		}
	}

	return nil
}

// names is the first column of what `query` answers.
func names(ctx context.Context, q querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}

	return out, rows.Err()
}
