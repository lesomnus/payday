package cmd_test

import (
	"bytes"
	"database/sql"
	"log/slog"
	"strings"
	"testing"

	"github.com/lesomnus/otx/log"
	"github.com/protobuf-orm/ent/dialect"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/valid"

	"github.com/lesomnus/payday/internal/apptest/cli"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	entmigrate "github.com/lesomnus/payday/internal/apptest/internal/ent/migrate"
)

// TestAnIndexWhoseBuildDidNotFinishIsRefused: an index the schema declares,
// built by an operator ahead of the migration that declares it, `CONCURRENTLY`
// so that the table takes writes while it builds -- and a build that did not
// finish, which PostgreSQL leaves in the catalog, invalid. A migration finds an
// index by its name, so it took that one for made and said the database was in
// this build's shape. `cli.Migrate` refuses it instead, naming it and what makes
// it again; what it says to run makes it; and an index of the operator's own,
// left the same way, is said and refuses nothing.
func TestAnIndexWhoseBuildDidNotFinishIsRefused(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	s, err := cmd.Build(ctx, cmd.Config{Db: dbOf(t), Watch: config.WatchConfig{Broker: config.BrokerMemory}})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(cli.Migrate(ctx, s))
	if s.Dialect != dialect.Postgres {
		t.Skipf("only PostgreSQL keeps an index a build did not finish; set %s", pdtest.Postgres)
	}

	// The trail's index by time as a build of it is left that a write on the
	// table held up past the session's timeout; and the same statement again,
	// which finds it there and skips it.
	build := `CREATE INDEX CONCURRENTLY IF NOT EXISTS audit_by_time ON audit (date_created, id)`
	execAll(t, s.Db, `DROP INDEX audit_by_time`)
	cancelled(t, s.Db, "audit", build)
	execAll(t, s.Db, build)
	x.False(isValid(t, s.Db, "audit_by_time"))
	x.NoError(entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables), "the shape a server checks for, by name")

	err = cli.Migrate(ctx, s)
	x.ErrorIs(err, valid.ErrInvalid)
	x.ErrorContains(err, "audit_by_time, on audit")
	x.False(isValid(t, s.Db, "audit_by_time"))

	// What it says to run, as it says to: a statement at a time, outside a
	// transaction.
	run := []string{}
	for _, line := range strings.Split(err.Error(), "\n") {
		if line = strings.TrimSpace(line); strings.HasSuffix(line, ";") && !strings.HasPrefix(line, "--") {
			run = append(run, line)
		}
	}
	x.Len(run, 2)
	x.True(strings.HasPrefix(run[0], "DROP INDEX CONCURRENTLY "), run[0])
	x.True(strings.HasPrefix(run[1], "CREATE INDEX CONCURRENTLY audit_by_time ON "), run[1])
	execAll(t, s.Db, run...)
	x.True(isValid(t, s.Db, "audit_by_time"))
	x.NoError(cli.Migrate(ctx, s))

	t.Run("and one the schema does not declare is said, and refuses nothing", func(t *testing.T) {
		x := require.New(t)

		cancelled(t, s.Db, "audit", `CREATE INDEX CONCURRENTLY audit_action_by_time ON audit (action, date_created)`)
		x.False(isValid(t, s.Db, "audit_action_by_time"))

		b := &bytes.Buffer{}
		ctx := log.Into(t.Context(), slog.New(slog.NewTextHandler(b, nil)))
		x.NoError(cli.Migrate(ctx, s))
		x.Contains(b.String(), "index=audit_action_by_time")
		x.Contains(b.String(), "level=WARN")
	})
}

// cancelled runs `q`, a build `CONCURRENTLY` on `table`, as a timeout leaves
// one: a write is open on the table, the build waits for it to end before it
// reads a row, and the session's `statement_timeout` runs out first.
func cancelled(t *testing.T, db *sql.DB, table string, q string) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	tx, err := db.BeginTx(ctx, nil)
	x.NoError(err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `LOCK TABLE `+table+` IN ROW EXCLUSIVE MODE`)
	x.NoError(err)

	conn, err := db.Conn(ctx)
	x.NoError(err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `SET statement_timeout = '200ms'`)
	x.NoError(err)
	_, err = conn.ExecContext(ctx, q)
	x.ErrorContains(err, "statement timeout")
	_, err = conn.ExecContext(ctx, `RESET statement_timeout`)
	x.NoError(err)
}

// isValid says PostgreSQL holds an index called `name` and uses it.
func isValid(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	ok := false
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT i.indisvalid FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relname = $1`, name).Scan(&ok))

	return ok
}

func execAll(t *testing.T, db *sql.DB, qs ...string) {
	t.Helper()

	for _, q := range qs {
		_, err := db.ExecContext(t.Context(), q)
		require.NoError(t, err, q)
	}
}
