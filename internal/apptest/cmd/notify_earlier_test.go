package cmd_test

import (
	"database/sql"
	"slices"
	"testing"

	"github.com/protobuf-orm/ent/dialect"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/rename"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cli"
	entmigrate "github.com/lesomnus/payday/internal/apptest/internal/ent/migrate"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// What a store an earlier build of notify left is carried into: each test here
// makes one the way this build does, puts it back the way that build had it,
// and migrates it with the app's own `cli.Migrate`.

// receiptColumns is the columns of the receipts' table, by name.
func receiptColumns(t *testing.T, db *sql.DB, d string) []string {
	t.Helper()

	q := `SELECT name FROM pragma_table_info('notificationreceiptrecord')`
	if d == dialect.Postgres {
		q = `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'notificationreceiptrecord'`
	}
	rows, err := db.QueryContext(t.Context(), q)
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

// TestAReceiptsFirstReadIsCarriedAcrossItsRename: until payday#196 a
// receipt's first read was stored as `data_read`. A migration finds a column
// by its name and drops none, so on such a store it would add `date_read`,
// empty, beside it, and every receipt marked before would read as marked at no
// time at all. `pd.ColumnRenames` is what `cli.Migrate` hands `rename.Columns`
// first, and after it each receipt reads as it was marked.
func TestAReceiptsFirstReadIsCarriedAcrossItsRename(t *testing.T) {
	x := require.New(t)
	x.Contains(pd.ColumnRenames, rename.Column{Table: "notificationreceiptrecord", From: "data_read", To: "date_read"})

	t.Run("a store as the build before left it", func(t *testing.T) {
		x := require.New(t)
		f := notifications(t)
		ctx := f.b.travels(t.Context())

		v, err := f.client.Add(ctx, f.add("before"))
		x.NoError(err)
		marked, err := notificationMarked(f.client.Mark(ctx, app.NotificationMarkRequest_builder{Refs: []*app.NotificationRef{app.NotificationRef_builder{Id: v.GetId()}.Build()}, TenantId: f.b.Tenant.Bytes()}.Build()))
		x.NoError(err)
		x.False(marked.GetDateRead().AsTime().IsZero())

		_, err = f.b.Db.ExecContext(t.Context(), `ALTER TABLE notificationreceiptrecord RENAME COLUMN date_read TO data_read`)
		x.NoError(err)

		x.NoError(cli.Migrate(t.Context(), f.b.Server))
		x.NoError(entschema.Check(t.Context(), f.b.Db, f.b.Dialect, entmigrate.Tables))
		x.Contains(receiptColumns(t, f.b.Db, f.b.Dialect), "date_read")
		x.NotContains(receiptColumns(t, f.b.Db, f.b.Dialect), "data_read")

		page, err := f.client.Read(ctx, f.read())
		x.NoError(err)
		x.Len(page.GetItems(), 1)
		x.True(proto.Equal(marked, page.GetItems()[0].GetReceipt()), "the receipt is not the one marked before the migration")

		again, err := notificationMarked(f.client.Mark(ctx, app.NotificationMarkRequest_builder{Refs: []*app.NotificationRef{app.NotificationRef_builder{Id: v.GetId()}.Build()}, TenantId: f.b.Tenant.Bytes()}.Build()))
		x.NoError(err)
		x.True(proto.Equal(marked, again), "marking again kept a first read other than the one carried across")
	})

	// An app that migrated without `rename.Columns` first: the migration added
	// the new column beside the old, and the receipts marked since hold their
	// time there while the ones before hold it in the old one.
	t.Run("a store a migration already added the new column to", func(t *testing.T) {
		x := require.New(t)
		f := notifications(t)
		ctx := f.b.travels(t.Context())
		mark := func(key string) *app.NotificationReceipt {
			v, err := f.client.Add(ctx, f.add(key))
			x.NoError(err)
			r, err := notificationMarked(f.client.Mark(ctx, app.NotificationMarkRequest_builder{Refs: []*app.NotificationRef{app.NotificationRef_builder{Id: v.GetId()}.Build()}, TenantId: f.b.Tenant.Bytes()}.Build()))
			x.NoError(err)
			return r
		}

		before := mark("before")
		_, err := f.b.Db.ExecContext(t.Context(), `ALTER TABLE notificationreceiptrecord RENAME COLUMN date_read TO data_read`)
		x.NoError(err)
		x.NoError(entmigrate.NewSchema(f.b.Drv).Create(t.Context()))
		x.Contains(receiptColumns(t, f.b.Db, f.b.Dialect), "data_read")
		x.Contains(receiptColumns(t, f.b.Db, f.b.Dialect), "date_read")
		after := mark("after")

		x.NoError(cli.Migrate(t.Context(), f.b.Server))
		x.NotContains(receiptColumns(t, f.b.Db, f.b.Dialect), "data_read")

		page, err := f.client.Read(ctx, f.read())
		x.NoError(err)
		x.Len(page.GetItems(), 2)
		got := []*app.NotificationReceipt{page.GetItems()[0].GetReceipt(), page.GetItems()[1].GetReceipt()}
		for _, want := range []*app.NotificationReceipt{before, after} {
			x.True(slices.ContainsFunc(got, func(v *app.NotificationReceipt) bool { return proto.Equal(want, v) }),
				"%s was not read back as it was marked", want.GetId())
		}
	})
}
