package valid

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/protobuf-orm/ent/dialect"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
)

// ErrUnknownRequired is a database holding, on a table the schema declares, a
// column the schema does not declare that takes no row without a value.
var ErrUnknownRequired = errors.New("a table the schema declares holds a column it does not, which takes no row without a value")

// Columns refuses `db` when a table `tables` declares holds a column the
// schema does not declare that is NOT NULL and has no default, with
// [ErrUnknownRequired] and, for each, its table, its name and the statement
// that drops it.
//
// Such a column is one an earlier build declared and this one does not -- a
// field taken out of the schema, or one an overlay declared and the app took
// back. The migration drops no column, so it leaves the column where it is,
// and `entschema.Check` asks after no column the schema does not declare, so
// the database reads as this build's shape. But nothing this build writes
// gives the column a value, and so every row written to its table is refused
// from then on: the server starts, and fails in whichever handler first
// writes there. An event's `desc` did that the day it stopped being a column
// (payday#224) -- every event after the upgrade refused, and nothing said why
// until one was written.
//
// Refused, and not dropped here: a column holds what it holds, and whether
// anything still reads it -- a report, another service on the same database
// -- is not the migration's to know. The answer is the statement that drops
// it, for somebody who can tell, or a default, which keeps what is there and
// lets a row be written without one.
//
// A column the schema does not declare that takes a row without a value --
// nullable, or with a default -- is left alone, and not said either. It costs
// a write nothing, and a deployment may have one on purpose: what
// `entschema.Check` leaves alone for the same reason. Nor is one that makes
// its own value -- an identity, a generated column -- refused, and a table the
// schema does not declare is not asked about at all.
//
// It reads the schema the migration works in, as [Indexes] does. On anything
// but PostgreSQL and SQLite it answers nil.
func Columns(ctx context.Context, db *sql.DB, d string, tables []*entschema.Table) error {
	var (
		vs  []column
		err error
	)
	switch d {
	case dialect.Postgres:
		vs, err = requiredPostgres(ctx, db)
	case dialect.SQLite:
		vs, err = requiredSQLite(ctx, db, tables)
	default:
		return nil
	}
	if err != nil {
		return err
	}

	declared := map[string]map[string]bool{}
	for _, t := range tables {
		cs := map[string]bool{}
		for _, c := range t.Columns {
			cs[c.Name] = true
		}
		declared[t.Name] = cs
	}

	refused := []column{}
	for _, v := range vs {
		if cs, ok := declared[v.table]; ok && !cs[v.name] {
			refused = append(refused, v)
		}
	}
	if len(refused) == 0 {
		return nil
	}

	lines := []string{}
	for _, v := range refused {
		lines = append(lines, "-- "+v.table+"."+v.name+", "+v.kind)
		lines = append(lines, "ALTER TABLE "+v.qualified+" DROP COLUMN "+v.quoted+";")
		if d == dialect.Postgres {
			lines = append(lines, "-- or keep it, and give it a default:")
			lines = append(lines, "-- ALTER TABLE "+v.qualified+" ALTER COLUMN "+v.quoted+" SET DEFAULT <a "+v.kind+">;")
		}
		lines = append(lines, "")
	}

	return fmt.Errorf("%w. The migration drops no column and nothing this build writes gives it a value, so every row "+
		"written to its table would be refused. Drop it if nothing reads it any more, or give it a default, and migrate again:\n\n  %s",
		ErrUnknownRequired, strings.TrimSpace(strings.Join(lines, "\n  ")))
}

// column is one NOT NULL column with no default, as the database holds it.
type column struct {
	// table and name are as the catalog has them, which is what a person
	// reads; qualified and quoted are the two as a statement takes them.
	table, name       string
	qualified, quoted string

	// kind is its type, as the database writes it.
	kind string
}

// requiredPostgres is every column of a table of the schema the migration
// works in that takes no row without a value: NOT NULL, no default of its own
// or of its type's, and no value it makes itself.
func requiredPostgres(ctx context.Context, db *sql.DB) ([]column, error) {
	rows, err := db.QueryContext(ctx, `SELECT t.relname, a.attname, format('%I.%I', n.nspname, t.relname), format('%I', a.attname),
			format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a
		JOIN pg_class t ON t.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		JOIN pg_type ty ON ty.oid = a.atttypid
		WHERE n.nspname = current_schema() AND t.relkind IN ('r', 'p')
			AND a.attnum > 0 AND NOT a.attisdropped
			AND a.attnotnull AND NOT a.atthasdef
			AND a.attidentity = '' AND a.attgenerated = ''
			AND ty.typdefault IS NULL
		ORDER BY t.relname, a.attnum`)
	if err != nil {
		return nil, fmt.Errorf("list the columns that take no row without a value: %w", err)
	}
	defer rows.Close()

	vs := []column{}
	for rows.Next() {
		v := column{}
		if err := rows.Scan(&v.table, &v.name, &v.qualified, &v.quoted, &v.kind); err != nil {
			return nil, fmt.Errorf("list the columns that take no row without a value: %w", err)
		}
		vs = append(vs, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the columns that take no row without a value: %w", err)
	}

	return vs, nil
}

// requiredSQLite is [requiredPostgres] on SQLite, of the tables `tables`
// declares, which are asked one at a time: SQLite answers about one table.
//
// A generated column is not among what it answers, and a key is declared, so
// neither needs leaving out here.
func requiredSQLite(ctx context.Context, db *sql.DB, tables []*entschema.Table) ([]column, error) {
	vs := []column{}
	for _, t := range tables {
		rows, err := db.QueryContext(ctx, `SELECT name, type FROM pragma_table_info(?) WHERE "notnull" AND dflt_value IS NULL ORDER BY cid`, t.Name)
		if err != nil {
			return nil, fmt.Errorf("list the columns of %s that take no row without a value: %w", t.Name, err)
		}
		for rows.Next() {
			v := column{table: t.Name, qualified: sqliteQuote(t.Name)}
			if err := rows.Scan(&v.name, &v.kind); err != nil {
				rows.Close()
				return nil, fmt.Errorf("list the columns of %s that take no row without a value: %w", t.Name, err)
			}
			v.quoted = sqliteQuote(v.name)
			vs = append(vs, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("list the columns of %s that take no row without a value: %w", t.Name, err)
		}
	}

	return vs, nil
}

// sqliteQuote is `name` as SQLite takes an identifier.
func sqliteQuote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
