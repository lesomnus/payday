// Package valid refuses a migration that would take an index PostgreSQL has
// not finished for one it made.
//
// PostgreSQL writes an index it is building `CONCURRENTLY` into the catalog
// first and builds it after, marked invalid until it is done, so that writes
// meanwhile keep it up. A build that fails -- a duplicate under a unique one, a
// deadlock, a timeout, somebody's cancel -- leaves it there as it was: written
// on every insert and update, and read by nothing, since the planner uses no
// index that is not valid. `REINDEX CONCURRENTLY` leaves the copy it was
// building the same way, under the name with `_ccnew` after it, and a `DROP
// INDEX CONCURRENTLY` that is interrupted leaves the one it was dropping.
//
// A migration finds an index by its name (see `rename`), so it finds one of
// those and takes it for made. ent's `Schema.Create` plans nothing for it,
// `entschema.Check` passes it, and the migration says the database is in this
// build's shape while the index it declares serves no read. Which is where an
// operator is who built an index ahead of a migration, `CONCURRENTLY` so as not
// to hold the table against writes while it built, and had the build fail:
// running it again `IF NOT EXISTS` skips it, and so does everything after.
//
// So an app's `Migrate` asks [Indexes] after `rename.Indexes` and before it
// creates:
//
//	if err := rename.Indexes(ctx, s.Db, s.Dialect, pd.IndexRenames); err != nil {
//		return err
//	}
//	if err := valid.Indexes(ctx, s.Db, s.Dialect, entmigrate.Tables); err != nil {
//		return err
//	}
//	return entmigrate.NewSchema(s.Drv).Create(ctx)
//
// After the renames, because an index is asked about under the name this build
// gives it, and because the one thing `rename.Indexes` leaves invalid -- an old
// name it was dropping `CONCURRENTLY` when it was stopped -- it finishes
// dropping the next time it runs. Before `Create`, so that a migration that
// refuses has made nothing of the schema's yet.
//
// # Refused, and not made again
//
// Making it again inside the migration is one of two things, and neither is
// the migration's to choose. Dropped and left to `Create`, it is built in
// `Create`'s transaction with the table held against writes the whole time --
// what the operator built it `CONCURRENTLY` to avoid. Built again
// `CONCURRENTLY`, it is a statement no transaction may hold, that waits out
// every transaction already on the table and reads the table twice, as long as
// the build that failed took, in the step a deployment runs before it serves;
// and if what failed it was the rows -- a unique index over two that are equal
// -- it fails again the same way and leaves another. A unique one may also
// still be refusing duplicates, if its build got as far as its second pass, and
// dropping it to build it again would stop that for as long as the build takes.
//
// So it is refused, and the refusal is the statements: what to drop and what
// to build, each `CONCURRENTLY`, written out from what PostgreSQL holds, for
// somebody who can watch them run and can tell why the first build failed.
//
// # Only an index the schema declares
//
// What is refused is an index the migration would take for made: one `tables`
// declares, under its name and on its table. Any other that PostgreSQL left
// invalid -- the copy a `REINDEX CONCURRENTLY` left, an operator's own index
// that did not finish -- is said in the context's log, with the statements
// that recover it, and refuses nothing. It makes nothing the migration says
// untrue; and a migration that refused it would refuse every start of a
// deployment that migrates as it starts -- an init container, a server with
// `db.migrate: true` -- over an index that is not the app's.
//
// # A server that serves over one
//
// A server that checks the database's shape instead of migrating it
// (`db.migrate: false`) asks the same, and logs what it is answered rather than
// refusing to start. By then the index is a cost to every write and a scan for
// every read that wanted it, which is a thing to fix and not a reason to stop
// serving: a server that refused would turn the next restart into an outage
// until somebody had built the index again, which on a table worth building one
// `CONCURRENTLY` for is not quick.
//
// # SQLite
//
// SQLite builds an index inside the transaction that asked for it, and a build
// that fails leaves nothing. There is nothing to find, so [Indexes] answers nil
// there, as it does on any database that is not PostgreSQL.
package valid

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/lesomnus/otx/log"
	"github.com/protobuf-orm/ent/dialect"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
)

// ErrInvalid is a database holding an index the schema declares that
// PostgreSQL left invalid.
var ErrInvalid = errors.New("an index the schema declares is one PostgreSQL left invalid")

// Indexes refuses `db` when an index `tables` declares is one PostgreSQL left
// invalid, with [ErrInvalid] and, for each, its name, the table it is on and
// the statements that make it again. Every other index it finds invalid it
// says in the context's log, with what recovers it, and does not refuse.
//
// It reads the schema the migration works in, which is the one its search path
// finds, as `rename.Indexes` does: another schema's indexes are not this
// app's. On anything but PostgreSQL it answers nil.
//
// An index that a build `CONCURRENTLY` is still making is invalid until the
// build is done, and is refused too: a migration that took it for made would be
// as wrong as one that took a failed build for made. Waiting for it is what
// recovers it.
func Indexes(ctx context.Context, db *sql.DB, d string, tables []*entschema.Table) error {
	if d != dialect.Postgres {
		return nil
	}

	vs, err := invalid(ctx, db)
	if err != nil {
		return err
	}

	declared := map[[2]string]bool{}
	for _, t := range tables {
		for _, i := range t.Indexes {
			declared[[2]string{t.Name, i.Name}] = true
		}
	}

	refused := []index{}
	for _, v := range vs {
		if declared[[2]string{v.table, v.name}] {
			refused = append(refused, v)
			continue
		}

		log.From(ctx).WarnContext(ctx, "valid: an index PostgreSQL left invalid, which every write keeps up and no read uses; "+
			"it is not one the schema declares, so nothing is refused for it",
			slog.String("index", v.name), slog.String("table", v.table), slog.String("run", strings.Join(v.recovery(), " ")))
	}
	if len(refused) == 0 {
		return nil
	}

	lines := []string{}
	for _, v := range refused {
		lines = append(lines, "-- "+v.name+", on "+v.table)
		lines = append(lines, v.remake()...)
		lines = append(lines, "")
	}

	return fmt.Errorf("%w, which is what a build of it CONCURRENTLY leaves when it does not finish: every write keeps it up, no read uses it, "+
		"and a migration would take it for made. Once no build of it is running, run each of these on its own and outside a transaction, "+
		"and migrate again:\n\n  %s", ErrInvalid, strings.TrimSpace(strings.Join(lines, "\n  ")))
}

// index is one index PostgreSQL holds invalid.
type index struct {
	// name and table are as the catalog has them, which is what a person
	// reads; qualified is the name with its schema, as a statement takes it.
	name, table, qualified string

	// def is the statement PostgreSQL would make it with now, from what its
	// catalog holds: `pg_get_indexdef`, which names the table with its schema.
	def string
}

// leftover is the name `REINDEX CONCURRENTLY` gives the copy it builds,
// `_ccnew`, and the one it gives the index the copy replaced, `_ccold`, while
// it drops that -- with a number after it where the name was taken. PostgreSQL
// shortens the name in front to make room, so the end is always this.
var leftover = regexp.MustCompile(`_cc(new|old)[0-9]*$`)

// remake is the statements that make `v` again, each `CONCURRENTLY`.
//
// Dropped first rather than rebuilt by `REINDEX INDEX CONCURRENTLY`, which
// would also do and is one statement: it is the recovery PostgreSQL's own
// documentation gives first, and the second statement says what is built.
func (v index) remake() []string {
	return []string{
		"DROP INDEX CONCURRENTLY " + v.qualified + ";",
		// Always `CREATE INDEX` or `CREATE UNIQUE INDEX` in front, so the
		// first `INDEX ` is the keyword.
		strings.Replace(v.def, "INDEX ", "INDEX CONCURRENTLY ", 1) + ";",
	}
}

// recovery is [index.remake] for an index the schema does not declare, which
// nothing will make again if it goes: what `REINDEX CONCURRENTLY` left of a
// rebuild is only dropped, since the index it was rebuilding is in place, and
// an index made again under its name would be the same index twice.
func (v index) recovery() []string {
	if leftover.MatchString(v.name) {
		return []string{"DROP INDEX CONCURRENTLY " + v.qualified + ";"}
	}

	return v.remake()
}

// invalid is every index of the schema the migration works in that PostgreSQL
// holds invalid, by table and then name.
//
// Indexes of a partitioned table's own (`relkind = 'I'`) are not among them:
// one of those is invalid until an index of each partition is attached to it,
// which is not a build that failed, and nothing ent makes is partitioned.
func invalid(ctx context.Context, db *sql.DB) ([]index, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.relname, t.relname, format('%I.%I', n.nspname, c.relname), pg_get_indexdef(c.oid)
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind = 'i' AND NOT i.indisvalid
		ORDER BY t.relname, c.relname`)
	if err != nil {
		return nil, fmt.Errorf("list the indexes PostgreSQL holds invalid: %w", err)
	}
	defer rows.Close()

	vs := []index{}
	for rows.Next() {
		v := index{}
		if err := rows.Scan(&v.name, &v.table, &v.qualified, &v.def); err != nil {
			return nil, fmt.Errorf("list the indexes PostgreSQL holds invalid: %w", err)
		}
		vs = append(vs, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list the indexes PostgreSQL holds invalid: %w", err)
	}

	return vs, nil
}
