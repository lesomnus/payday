package rename

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/protobuf-orm/ent/dialect"
)

// Column is one column an earlier build made under another name.
type Column struct {
	// Table is the table it is on.
	Table string

	// From is the name an earlier build made it under, and To the one this
	// build makes it under.
	From string
	To   string
}

// Columns moves each column of `vs` that `db` holds under its old name to its
// new one, with what it holds, and does nothing for one it does not hold. So
// it is the same call on a database made by any earlier build, by this one,
// and on an empty one, as often as it is made.
//
// # Why before the migration
//
// Because the migration would not move it. `Schema.Create` makes what the
// schema declares and finds missing, and drops no column: for a column that
// changed its name it adds the new one, empty, beside the old, and every row
// written before reads as never having had a value. Nothing fails. The values
// are still in the old column, which nothing reads again.
//
// On SQLite it is worse where an index names the column, as the past's do. The
// index has to be made again over the new column, and the migration does that
// by making the whole table again, from the schema -- without the old column,
// and so without what it held. Nothing is left to carry, and nothing fails
// then either.
//
// # What it does
//
// A column held under its old name alone is renamed -- one change to the
// catalog on PostgreSQL, and on SQLite (3.25 and later) the table's own
// definition rewritten in place. Nothing is copied, and every row keeps its
// value.
//
// A column held under both is what a migration that ran before this one left
// where it did not make the table again: the new one added beside the old. What the old one holds is copied into the
// new one wherever the new one is still NULL -- which is every row written
// before, since a column a migration adds to rows already there is NULL in
// each -- and the old one is dropped, in one transaction. Rows the build after
// it wrote keep the value they were written with.
//
// It is run before a migration, by one process at a time, as the migration
// is, and as [Indexes] is -- and after [Tables]: a column is found on its
// table by the name the table has now, and one still under the name its
// entity had holds no column this finds.
func Columns(ctx context.Context, db *sql.DB, d string, vs []Column) error {
	if len(vs) == 0 {
		return nil
	}

	var list string
	switch d {
	case dialect.Postgres:
		// The schema the migration works in, which is the one its search path
		// finds: a table of the same name in another schema is not this one.
		list = `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1`
	case dialect.SQLite:
		list = `SELECT name FROM pragma_table_info(?)`
	default:
		return fmt.Errorf("nothing here renames the columns of a %q database", d)
	}

	for _, v := range vs {
		on, err := columns(ctx, db, list, v.Table)
		if err != nil {
			return err
		}

		switch {
		case !on[v.From]:
			// Never made, or moved already.
			continue

		case !on[v.To]:
			q := "ALTER TABLE " + quote(v.Table) + " RENAME COLUMN " + quote(v.From) + " TO " + quote(v.To)
			if _, err := db.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("column %s of %s, which this build calls %s: %w", v.From, v.Table, v.To, err)
			}

		default:
			if err := carry(ctx, db, v); err != nil {
				return fmt.Errorf("column %s of %s, which this build calls %s and a migration made beside it: %w", v.From, v.Table, v.To, err)
			}
		}
	}

	return nil
}

// carry copies what the old column holds into the new one where the new one
// holds nothing, and drops the old one, in one transaction: a copy that
// stopped halfway leaves both, and the next call carries the rest.
func carry(ctx context.Context, db *sql.DB, v Column) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	table, from, to := quote(v.Table), quote(v.From), quote(v.To)
	if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET "+to+" = "+from+" WHERE "+to+" IS NULL"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "ALTER TABLE "+table+" DROP COLUMN "+from); err != nil {
		return err
	}

	return tx.Commit()
}

// columns is which columns `table` has, by name: none for a table there is
// not, which is a database no build made it in yet.
func columns(ctx context.Context, db *sql.DB, q, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, q, table)
	if err != nil {
		return nil, fmt.Errorf("list the columns of %s: %w", table, err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("list the columns of %s: %w", table, err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the columns of %s: %w", table, err)
	}

	return out, nil
}
