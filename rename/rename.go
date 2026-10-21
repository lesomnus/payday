// Package rename moves what an earlier build made under one name to the name
// this build makes it under, before a migration looks for it.
//
// A migration finds a thing by its name. Ent's -- `Schema.Create` -- makes
// what the schema declares and finds missing, and drops no index, so an index
// whose name changed is made again under the new one, built from every row
// while the table is held against writes, and the old one stays beside it:
// written on every insert and read by nothing. Renamed first, it is found, and
// the migration has nothing to do.
//
// Indexes are what this renames, because they are what has moved:
// protoc-gen-orm-ent stored an index under a name made of its columns,
// `robot_tenant_id_date_created_id`, and stores it now under the name the
// schema declares, `robot_page` (protobuf-orm/protoc-gen-orm-ent#15).
// `pd gen` writes what that means for an app's schema as `pd.IndexRenames`,
// and an app's `Migrate` hands it here before it creates:
//
//	if err := rename.Indexes(ctx, s.Db, s.Dialect, pd.IndexRenames); err != nil {
//		return err
//	}
//	return entmigrate.NewSchema(s.Drv).Create(ctx)
package rename

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/protobuf-orm/ent/dialect"
)

// Index is one index an earlier build made under another name.
type Index struct {
	// Table is the table it is on, which is asked as well as the name: a name
	// that is somewhere else is not this index.
	Table string

	// From is the name an earlier build made it under, and To the one this
	// build makes it under.
	From string
	To   string
}

// Indexes renames each index of `vs` that `db` holds under its old name, and
// does nothing for one it does not. So it is the same call on a database made
// by any earlier build, by this one, and on an empty one, as often as it is
// made.
//
// On PostgreSQL a rename is one change to the catalog: the index is the one it
// was, and nothing is built. Where an earlier migration already made the new
// one beside it, the old one is dropped instead, `CONCURRENTLY`, which blocks
// no read or write while it waits out the transactions already on the table.
//
// SQLite renames no index, so there the old one is dropped and the migration
// makes the new one, from the rows -- which a database SQLite is up to holding
// does quickly.
//
// It is run before a migration, by one process at a time, as the migration
// is: two at once would each find the old name and one of them would not.
func Indexes(ctx context.Context, db *sql.DB, d string, vs []Index) error {
	if len(vs) == 0 {
		return nil
	}

	var (
		list string
		move func(Index, bool) []string
	)
	switch d {
	case dialect.Postgres:
		// The schema the migration works in, which is the one its search path
		// finds: another schema's index of the same name is not this one.
		list = `SELECT indexname, tablename FROM pg_indexes WHERE schemaname = current_schema()`
		move = func(v Index, both bool) []string {
			if both {
				return []string{"DROP INDEX CONCURRENTLY IF EXISTS " + quote(v.From)}
			}
			return []string{"ALTER INDEX IF EXISTS " + quote(v.From) + " RENAME TO " + quote(v.To)}
		}
	case dialect.SQLite:
		list = `SELECT name, tbl_name FROM sqlite_master WHERE type = 'index'`
		move = func(v Index, _ bool) []string {
			return []string{"DROP INDEX IF EXISTS " + quote(v.From)}
		}
	default:
		return fmt.Errorf("nothing here renames the indexes of a %q database", d)
	}

	on, err := indexes(ctx, db, list)
	if err != nil {
		return err
	}
	for _, v := range vs {
		if t, ok := on[v.From]; !ok || t != v.Table {
			continue
		}

		_, both := on[v.To]
		for _, q := range move(v, both) {
			if _, err := db.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("index %s, which this build calls %s: %w", v.From, v.To, err)
			}
		}
	}

	return nil
}

// indexes is the table each index `q` lists is on, by the index's name.
func indexes(ctx context.Context, db *sql.DB, q string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list the indexes: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var name, table string
		if err := rows.Scan(&name, &table); err != nil {
			return nil, fmt.Errorf("list the indexes: %w", err)
		}
		out[name] = table
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the indexes: %w", err)
	}

	return out, nil
}

// quote writes a name as an identifier, which both databases read the same.
func quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
