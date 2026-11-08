package cmd_test

import (
	"context"
	"database/sql"
	"testing"
	"time"
	"uuid"

	"github.com/protobuf-orm/ent/dialect"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lesomnus/payday/entitle"
	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/rename"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cli"
	entmigrate "github.com/lesomnus/payday/internal/apptest/internal/ent/migrate"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// What a store an earlier build of the past and of the copy of entitlements
// left is carried into: until their times were `date_*`, as every other time
// payday keeps is, an event's was `occurred_at`, a fact's `valid_from` and
// `superseded_at`, and a grant's window `valid_from` and `valid_until`. Each
// test here makes a store the way this build does, puts it back the way that
// build had it, and migrates it with the app's own `cli.Migrate`.

// datesRenamed is those columns, as `pd.ColumnRenames` holds them.
var datesRenamed = []rename.Column{
	{Table: "event", From: "occurred_at", To: "date_occurred"},
	{Table: "fact", From: "valid_from", To: "date_start"},
	{Table: "fact", From: "superseded_at", To: "date_superseded"},
	{Table: "entitlement", From: "valid_from", To: "date_effective"},
	{Table: "entitlement", From: "valid_until", To: "date_expires"},
}

// columnsOf is the columns of `table`, by name.
func columnsOf(t *testing.T, db *sql.DB, d string, table string) []string {
	t.Helper()

	q := `SELECT name FROM pragma_table_info(?)`
	if d == dialect.Postgres {
		q = `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1`
	}
	rows, err := db.QueryContext(t.Context(), q, table)
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		out = append(out, v)
	}
	require.NoError(t, rows.Err())

	return out
}

// asBefore puts each column back under the name an earlier build stored it
// under, with what it holds and the indexes on it.
func asBefore(t *testing.T, db *sql.DB) {
	t.Helper()

	for _, v := range datesRenamed {
		_, err := db.ExecContext(t.Context(), `ALTER TABLE `+v.Table+` RENAME COLUMN `+v.To+` TO `+v.From)
		require.NoError(t, err)
	}
}

// dated is one time a row holds: its table, the row, and the column, by this
// build's name.
type dated struct {
	table  string
	row    uuid.UUID
	column string
}

// datesOf is every time the past and the copy hold, as this build reads them;
// nil for one that holds none.
func datesOf(t *testing.T, ctx context.Context, b *built) map[dated]*time.Time {
	t.Helper()
	x := require.New(t)

	out := map[dated]*time.Time{}
	put := func(table string, id uuid.UUID, column string, v *time.Time) {
		if v != nil && !v.IsZero() {
			u := v.UTC()
			out[dated{table, id, column}] = &u
		} else {
			out[dated{table, id, column}] = nil
		}
	}

	evs, err := b.Ent.Event.Query().All(ctx)
	x.NoError(err)
	for _, v := range evs {
		put("event", v.Id, "date_occurred", &v.DateOccurred)
	}
	fs, err := b.Ent.Fact.Query().All(ctx)
	x.NoError(err)
	for _, v := range fs {
		put("fact", v.Id, "date_start", &v.DateStart)
		put("fact", v.Id, "date_superseded", v.DateSuperseded)
	}
	gs, err := b.Ent.Entitlement.Query().All(ctx)
	x.NoError(err)
	for _, v := range gs {
		put("entitlement", v.Id, "date_effective", v.DateEffective)
		put("entitlement", v.Id, "date_expires", &v.DateExpires)
	}

	return out
}

// dating writes the past of a machine -- the event that made a fact of it,
// and one that corrected that fact, so that it is superseded -- and copies a
// grant from `src` given a window, into `b`: rows with every one of the
// columns, each holding a time.
func dating(t *testing.T, b *built, ctx context.Context, src *grants, alias string) {
	t.Helper()
	x := require.New(t)
	as := b.as(ctx)

	r := b.arm(t, as, alias)
	m, err := b.Walled.Machine().Add(as, app.MachineAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  alias,
		Model:  "X-1",
		Robot:  app.RobotRef_builder{Id: r.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	mid := idOf(t, m.GetId())
	model := m.ProtoReflect().Descriptor().Fields().ByName("model")

	var written past.FactRow
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		ctx, op, err := past.Begin(ctx, s, past.Event{
			Subject:      mid,
			DateOccurred: time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
			Value:        app.MachineStarted_builder{Machine: m.GetId()}.Build(),
		})
		if err != nil {
			return err
		}
		if err := op.Set(ctx, past.Fact{Subject: mid, DateStart: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), Values: []past.Value{
			past.To(model, protoreflect.ValueOfString("X-2")),
		}}); err != nil {
			return err
		}

		// Read back by the columns the past's own predicates name.
		sts, err := past.Facts(ctx, s, past.FactQuery{Events: []pdid.Id{op.Id()}})
		if err != nil {
			return err
		}
		x.Len(sts, 1)
		x.Len(sts[0].Rows, 1)
		written = sts[0].Rows[0]

		return nil
	}))
	x.NoError(b.operate(t, as, func(ctx context.Context, s past.Store) error {
		corrected := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
		_, op, err := past.Begin(ctx, s, past.Event{Kind: "correct", Value: app.Corrected_builder{
			Event:        written.Event.Bytes(),
			DateOccurred: timestamppb.New(corrected),
		}.Build()})
		if err != nil {
			return err
		}

		return op.Correct(ctx, written.Id, corrected)
	}))

	src.hold(grantOf(alias, b.Tenant, "apptest.robots", 1, entitle.Limited(2),
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2126, 9, 1, 0, 0, 0, 0, time.UTC)))
	_, err = entitle.Follower{Source: source(t, src), Copy: b.Entitlements}.Reconcile(ctx)
	x.NoError(err)
}

// TestThePastsAndTheCopysTimesAreCarriedAcrossTheirRename: a migration finds
// a column by its name, so on a store an earlier build made it finds none of
// these. It adds each, empty, beside the old one -- every event as having
// happened at no time, every fact as holding from no time and never
// superseded, every grant as long ended -- and on SQLite it makes a table
// whose index names one of them again, without the old column at all; and
// nothing fails. `pd.ColumnRenames` is what `cli.Migrate` hands
// `rename.Columns` first, and after it every row reads as it was written.
func TestThePastsAndTheCopysTimesAreCarriedAcrossTheirRename(t *testing.T) {
	x := require.New(t)
	x.Subset(pd.ColumnRenames, datesRenamed)

	t.Run("a store as the build before left it", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		src := newGrants()

		dating(t, b, ctx, src, "before")
		want := datesOf(t, ctx, b)
		superseded := 0
		for k, v := range want {
			if k.column == "date_superseded" {
				if v != nil {
					superseded++
				}
				continue
			}
			x.NotNil(v, "%s of %s holds no time", k.column, k.row)
		}
		x.Equal(1, superseded, "the fact the correction superseded")

		asBefore(t, b.Db)
		x.NoError(cli.Migrate(t.Context(), b.Server))
		x.NoError(entschema.Check(t.Context(), b.Db, b.Dialect, entmigrate.Tables))
		for _, v := range datesRenamed {
			x.Contains(columnsOf(t, b.Db, b.Dialect, v.Table), v.To)
			x.NotContains(columnsOf(t, b.Db, b.Dialect, v.Table), v.From)
		}
		x.Equal(want, datesOf(t, ctx, b), "a time is not the one written before the migration")

		// And the past and the copy are written, and read by these columns, as
		// they were before.
		dating(t, b, ctx, src, "after")
	})

	// An app that migrated without `rename.Columns` first. On PostgreSQL the
	// migration added each new column beside the old -- and moved the indexes
	// that name it onto the new one, empty -- so that what was written since
	// holds its time there while what was written before holds it in the old
	// one, and `rename.Columns` carries the one into the other.
	//
	// On SQLite it did that only where no index names the column. A table
	// whose index does, `event` and `fact`, it made again with the new columns
	// and without the old: what they held is gone, and there is nothing left
	// for `rename.Columns` to carry. Which is why it comes first.
	t.Run("a store a migration already added the new columns to", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		src := newGrants()

		dating(t, b, ctx, src, "before")
		want := datesOf(t, ctx, b)

		asBefore(t, b.Db)
		x.NoError(entmigrate.NewSchema(b.Drv).Create(t.Context()))
		gone := map[string]bool{}
		if b.Dialect == dialect.SQLite {
			gone = map[string]bool{"event": true, "fact": true}
		}
		for _, v := range datesRenamed {
			x.Contains(columnsOf(t, b.Db, b.Dialect, v.Table), v.To)
			if gone[v.Table] {
				x.NotContains(columnsOf(t, b.Db, b.Dialect, v.Table), v.From, "a table SQLite made again kept a column the schema does not have")
			} else {
				x.Contains(columnsOf(t, b.Db, b.Dialect, v.Table), v.From)
			}
		}
		for k := range want {
			if gone[k.table] {
				want[k] = nil
			}
		}

		dating(t, b, ctx, src, "after")
		for k, v := range datesOf(t, ctx, b) {
			if _, ok := want[k]; !ok {
				want[k] = v
			}
		}

		x.NoError(cli.Migrate(t.Context(), b.Server))
		x.NoError(entschema.Check(t.Context(), b.Db, b.Dialect, entmigrate.Tables))
		for _, v := range datesRenamed {
			x.NotContains(columnsOf(t, b.Db, b.Dialect, v.Table), v.From)
		}
		x.Equal(want, datesOf(t, ctx, b), "a time is not the one written on its side of the migration")
	})
}
