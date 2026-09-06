// Package sandbox is the database the page starts with, already made.
//
// It is one SQL script: the schema every table of this app needs, and the rows
// `sandbox_test.go` produced by running the real server against a real SQLite
// and writing down what came out. The page executes it and has an app.
//
// # Why a script and not `Schema.Create`
//
// Because `Schema.Create` is ent's migration engine, and ent's migration engine
// is Atlas -- which brings its own diff planner, all three of its SQL dialects,
// and the HCL parser those import for a schema language nothing here writes.
// Measured, that is 10.6 MB of what a browser has to download to see the
// sandbox, spent deciding what to do to a database that does not exist yet.
//
// There is nothing for a migration to decide here. A reload is a new database,
// so what the page wants is not "bring this in line" but "be this".
//
// # And why the rows are here too
//
// The page used to seed itself: a tenant, somebody in it, and 200 more so that
// a table has to be scrolled. Through the server, correctly -- the trail and
// the outbox have to see those writes, and a row put in behind the stack is a
// row the sandbox is lying about.
//
// That is 202 round trips to a SQLite in another Worker before the first frame,
// and the answer is the same every time. So they are made once, here, by the
// same stack through the same server -- and what the page does is one `EXEC`.
//
// # What keeps it true
//
// `TestTheScriptIsThisSchema`. The rows are a fixture and nothing compares
// them, but the schema is checked against what ent would create today, so a
// change to an entity fails here rather than in a browser:
//
//	PDTEST_UPDATE=1 go test -C internal/apptest ./sandbox/
//
// writes it again.
package sandbox

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

// Script is the schema and the rows, as SQL.
//
// Exported because what a sandbox is made of is worth being able to print. It
// is also what `pd sandbox init` writes the shape of into a new app.
//
//go:embed sandbox.sql
var Script string

// Load puts the sandbox's database into db.
//
// One `ExecContext` and not a statement at a time: the driver's EXEC loops the
// multi-statement tail itself, so this is a single round trip to the Worker for
// the whole script. Statement by statement it would be one per row, which is
// the cost this package exists to avoid.
func Load(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, Script); err != nil {
		return fmt.Errorf("execute the sandbox script: %w", err)
	}

	return nil
}
