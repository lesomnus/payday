package sandbox_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cli"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	"github.com/lesomnus/payday/internal/apptest/sandbox"
)

const at = "sandbox.sql"

// The two halves of the script, and the marker between them. The schema is
// checked and the rows are not, so there has to be somewhere the checking
// stops -- and a reader opening the file wants to know which part is which
// anyway.
const (
	head = `-- Written by TestTheScriptIsWhatThePageGets. Do not edit; regenerate.
--
-- One transaction, so the page pays for one commit and not one per row, and
-- foreign keys deferred to it: the rows below are dumped a table at a time in
-- name order, which is not an order that satisfies them. They satisfy them
-- together, and together is what COMMIT checks.
BEGIN;
PRAGMA defer_foreign_keys = ON;

-- The schema, as ent creates it. Checked by TestTheScriptIsThisSchema.
`
	mid  = "\n-- The rows, as the server wrote them. A fixture; nothing compares these.\n"
	tail = "\nCOMMIT;\n"
)

// TestTheScriptIsThisSchema, which is the one thing about a database written
// down in another file that can go wrong quietly.
//
// The rows are a fixture: they are somebody's idea of enough to look at, and
// nothing follows from them being different. The schema is not. A column added
// to an entity and not to this file is a sandbox that raises `no such column`
// on the first read, in a browser, with the generator that would have fixed it
// three commands away.
//
// So this creates the schema the way the process creates it -- ent, Atlas and
// all, which is fine in a test -- and compares. It is the reason the script can
// be trusted without the page being loaded.
func TestTheScriptIsThisSchema(t *testing.T) {
	if update() {
		// The embed still holds what the file said when this binary was
		// compiled, so under [pdtest.Update] this would compare the outgoing
		// script against the incoming schema. The run after the write is the
		// one that means anything.
		t.Skipf("%s is set; this checks the file, and the file is being written", pdtest.Update)
	}

	x := require.New(t)
	ctx := t.Context()

	s, db := built(t, x)
	x.NoError(cli.Migrate(ctx, s))

	want, err := schemaOf(ctx, db)
	x.NoError(err)

	got, _, found := strings.Cut(sandbox.Script, mid)
	x.True(found, "%s has no rows section; %s=1 go test ./sandbox/ writes it", at, pdtest.Update)

	x.Equal(want, strings.TrimPrefix(got, head),
		"%s is not the schema ent creates.\n\n    %s=1 go test -C internal/apptest ./sandbox/\n\nwrites it",
		at, pdtest.Update)
}

// TestTheScriptIsWhatThePageGets writes the file when asked to, and otherwise
// says the file it wrote loads and holds what the page expects to find.
//
// Loading it into a fresh database rather than trusting the text is the whole
// assertion: a script that does not execute is a sandbox that shows an error
// where an app should be, and the SQL here is generated, so nobody reads it.
func TestTheScriptIsWhatThePageGets(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	// What is checked is the script this run would ship. Under [pdtest.Update]
	// that is the one just written, and not `sandbox.Script` -- an embed is
	// read at build time, so the package variable still holds whatever the file
	// said when the test binary was compiled. Asserting on that would pass on
	// the previous script and write an unexamined new one.
	script := sandbox.Script
	if update() {
		s, db := built(t, x)
		x.NoError(cli.Migrate(ctx, s))
		x.NoError(seed(ctx, s.Ungated))

		b, err := dump(ctx, db)
		x.NoError(err)
		x.NoError(os.WriteFile(at, []byte(b), 0o644))

		t.Logf("wrote %s, %d bytes", at, len(b))
		script = b
	}

	// A database that has never heard of ent, which is what the page has.
	db := opened(t, x)
	if script == sandbox.Script {
		x.NoError(sandbox.Load(ctx, db))
	} else {
		_, err := db.ExecContext(ctx, script)
		x.NoError(err)
	}

	// The tenant the page signs in to, and the holder it signs in as. Without
	// both, every call is refused for a reason that names neither.
	var n int
	x.NoError(db.QueryRowContext(ctx, `SELECT count(*) FROM tenant WHERE alias = 'acme'`).Scan(&n))
	x.Equal(1, n, "the tenant the page signs in to")

	x.NoError(db.QueryRowContext(ctx, `SELECT count(*) FROM holder WHERE alias = 'admin'`).Scan(&n))
	x.Equal(1, n, "the holder the page signs in as")

	// And enough of them that a table has to be scrolled, which is the only
	// reason the other 200 are there.
	x.NoError(db.QueryRowContext(ctx, `SELECT count(*) FROM holder`).Scan(&n))
	x.Equal(201, n)
}

func update() bool {
	v := os.Getenv(pdtest.Update)

	return v != "" && v != "0" && v != "false"
}

// built stands the app up on a SQLite of its own, the way the process does.
//
// The wazero driver and not the page's, because this is a process: `dbsqlite3`
// is what `cmd/driver.go` links for exactly this, and the point of generating
// the script here rather than in the browser is that everything a generator
// needs is available.
func built(t *testing.T, x *require.Assertions) (*cmd.Server, *sql.DB) {
	t.Helper()

	c := cmd.Config{
		Db: config.DbConfig{
			Driver: "sqlite3",
			Dsn:    "file:" + filepath.Join(t.TempDir(), "seed.db"),

			// One, so that what is dumped is what one connection wrote. The
			// page has one for its own reasons; see `wasm/main.go`.
			MaxOpenConns: 1,
		},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	}

	s, err := cmd.Build(t.Context(), c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })

	return s, s.Db
}

// opened is a database with nothing in it.
func opened(t *testing.T, x *require.Assertions) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "loaded.db"))
	x.NoError(err)
	t.Cleanup(func() { db.Close() })

	return db
}

// seed puts a tenant and somebody in it into s, and then enough holders that a
// table has to be scrolled.
//
// Through the server and not into the tables, which is the whole reason this
// runs at all rather than the rows being written by hand: the trail has to
// record them and the outbox has to carry them, so what the page starts with is
// a database somebody could have arrived at by using the app.
//
// A tenant cannot be put up from inside one -- the Gate layer refuses it to
// everybody, which is the answer a real deployment gives -- so this is
// `s.Ungated`, and it is why the sandbox has to be seeded at all.
func seed(ctx context.Context, s app.Server) error {
	t, err := s.Tenant().Add(ctx, app.TenantAddRequest_builder{
		Alias: "acme",
		Name:  "Acme",
	}.Build())
	if err != nil {
		return fmt.Errorf("the tenant: %w", err)
	}

	if _, err := s.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: t.GetId()}.Build(),
		Alias:  "admin",
	}.Build()); err != nil {
		return fmt.Errorf("the holder: %w", err)
	}

	// A page of rows is not the same thing as a table: paging, a cursor that
	// advances, a header that stays put and a column somebody turns off halfway
	// down are all things that only happen past the first screenful.
	for i := range 200 {
		if _, err := s.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: t.GetId()}.Build(),
			Alias:  fmt.Sprintf("op-%03d", i),
		}.Build()); err != nil {
			return fmt.Errorf("holder %d: %w", i, err)
		}
	}

	return nil
}

// dump writes db out as the SQL that reproduces it.
func dump(ctx context.Context, db *sql.DB) (string, error) {
	var b strings.Builder

	b.WriteString(head)

	s, err := schemaOf(ctx, db)
	if err != nil {
		return "", err
	}

	b.WriteString(s)
	b.WriteString(mid)

	ts, err := tablesOf(ctx, db)
	if err != nil {
		return "", err
	}

	for _, t := range ts {
		if err := rowsOf(ctx, &b, db, t); err != nil {
			return "", fmt.Errorf("%s: %w", t, err)
		}
	}

	b.WriteString(tail)

	return b.String(), nil
}

// schemaOf is every statement SQLite would need to make these tables again.
//
// Read out of `sqlite_master` rather than asked of ent, because what has to be
// reproduced is the database and not the intention -- an index ent creates as a
// side effect of an edge is in one and not the other.
//
// Tables before indexes, and then by name: a stable order, so that a diff of
// this file is a diff of the schema and not of the order SQLite happened to
// return it in.
func schemaOf(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT sql FROM sqlite_master
		WHERE sql IS NOT NULL
		ORDER BY CASE type WHEN 'table' THEN 0 ELSE 1 END, name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var b strings.Builder
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return "", err
		}

		b.WriteString(s)
		b.WriteString(";\n")
	}

	return b.String(), rows.Err()
}

// tablesOf names the tables that hold rows, in a stable order.
func tablesOf(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ts []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}

		ts = append(ts, s)
	}

	return ts, rows.Err()
}

// rowsOf writes t's rows as INSERT statements.
//
// The values come back from SQLite's own `quote()`, which is the point: it
// answers with the SQL literal for whatever is actually in the column --
// `X'..'` for a blob, `NULL` for a null, the right number of quotes for a
// string with one in it. Formatting them here would mean deciding what a value
// is from Go's side, and the driver has already decided that once by looking at
// the column's declared type. A UUID stored as a blob and handed back as a
// `time.Time` is the kind of thing that finds out in a browser.
func rowsOf(ctx context.Context, b *strings.Builder, db *sql.DB, t string) error {
	cs, err := columnsOf(ctx, db, t)
	if err != nil {
		return err
	}

	qs := make([]string, len(cs))
	ns := make([]string, len(cs))
	for i, c := range cs {
		qs[i] = fmt.Sprintf("quote(%s)", quoted(c.name))
		ns[i] = quoted(c.name)
	}

	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		`SELECT %s FROM %s ORDER BY rowid`, strings.Join(qs, ", "), quoted(t)))
	if err != nil {
		return err
	}
	defer rows.Close()

	vs := make([]string, len(cs))
	ps := make([]any, len(cs))
	for i := range vs {
		ps[i] = &vs[i]
	}

	for rows.Next() {
		if err := rows.Scan(ps...); err != nil {
			return err
		}

		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = normalised(cs[i].decl, v)
		}

		fmt.Fprintf(b, "INSERT INTO %s (%s) VALUES (%s);\n",
			quoted(t), strings.Join(ns, ", "), strings.Join(out, ", "))
	}

	return rows.Err()
}

// column is what a column is called and what it was declared as.
type column struct {
	name string
	decl string
}

func columnsOf(ctx context.Context, db *sql.DB, t string) ([]column, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT name, type FROM pragma_table_info(%s)`, literal(t)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cs []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.name, &c.decl); err != nil {
			return nil, err
		}

		cs = append(cs, c)
	}

	return cs, rows.Err()
}

// canonical is the layout a timestamp is written in here.
//
// It is what the page's driver writes, and the point is that it is fixed width
// -- nine fractional digits whatever the instant, and a `T`.
const canonical = "2006-01-02T15:04:05.000000000Z"

// normalised is `v` in that layout, when the column holds a time.
//
// # Why the dump cannot just write down what it read
//
// Because two drivers wrote it. The script is generated by a process, on
// `ncruces/go-sqlite3`, and executed by a page, on `sqlite3-wasm`. SQLite has
// no date type: a timestamp is TEXT, and every comparison on it -- including
// the `WHERE col > ?` a keyset cursor is made of -- is a comparison of bytes.
// So byte order has to be time order, and that only holds if everything in the
// column is written the same way.
//
// It was not. `ncruces` writes `time.RFC3339Nano`, which drops trailing zeros,
// so `.1` and `.15` are a tenth and fifteen hundredths ordered by their
// lengths. Measured: of 201 seeded holders, four came back on two pages, one
// at each boundary that landed on such a pair -- and the panel, which pages by
// being scrolled, showed them twice.
//
// The separator was the same bug louder, and is fixed upstream: `sqlite3-wasm`
// writes a `T` and nine digits now, and this makes the rows it is handed agree
// with the rows it will go on to write.
func normalised(decl string, v string) string {
	switch strings.ToUpper(decl) {
	case "DATE", "DATETIME", "TIMESTAMP":
	default:
		return v
	}

	// `quote()` gave a SQL literal; a time is a quoted string, and anything
	// else -- NULL, an integer -- is not this function's to touch.
	if len(v) < 2 || v[0] != '\'' {
		return v
	}

	at, ok := parseTime(strings.ReplaceAll(v[1:len(v)-1], "''", "'"))
	if !ok {
		return v
	}

	return literal(at.UTC().Format(canonical))
}

// parseTime reads the forms either driver writes.
func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05.999999999Z",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999Z",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05.999999999",
	} {
		if v, err := time.Parse(layout, s); err == nil {
			return v, true
		}
	}

	return time.Time{}, false
}

func quoted(s string) string  { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func literal(s string) string { return `'` + strings.ReplaceAll(s, `'`, `''`) + `'` }
