package cmd_test

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/xlitest"
	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/payday/internal/apptest"
	entaccess "github.com/lesomnus/payday/internal/apptest/internal/ent/access"
	entowed "github.com/lesomnus/payday/internal/apptest/internal/ent/owed"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"
)

// TestAListOfIdentifiersIsStoredAsBeforeAndPrintedAsIdentifiers: the access
// record's `subjects` and what is owed's `held_by` say they hold identifiers
// (`(orm.field) = {type: TYPE_UUID}`), so a person reading an access record
// reads whose data the call carried rather than `(16 bytes)` for each. The
// option says what the elements are and changes nothing of how they are
// stored: a repeated field is kept as the proto's own elements, whatever its
// type says (protobuf-orm/protoc-gen-orm-ent#26). So the column holds what the
// build before the option wrote -- a JSON array of each identifier's sixteen
// bytes in base64, which is what encoding/json makes of a [][]byte -- and a
// row written by either reads back as the other.
func TestAListOfIdentifiersIsStoredAsBeforeAndPrintedAsIdentifiers(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	alice := b.person(t, ctx, "alice")

	// What a column holds, read as SQL reads it and not as ent decodes it.
	column := func(table, col string, id any) []string {
		t.Helper()
		q, args := entsql.Dialect(b.Drv.Dialect()).Select(col).From(entsql.Table(table)).Where(entsql.EQ("id", id)).Query()
		rows := &entsql.Rows{}
		x.NoError(b.Drv.Query(ctx, q, args, rows))
		defer rows.Close()
		x.True(rows.Next())
		var raw []byte
		x.NoError(rows.Scan(&raw))

		var vs []string
		x.NoError(json.Unmarshal(raw, &vs), "%s.%s is a JSON array of strings: %s", table, col, raw)
		return vs
	}
	before := func(ks ...pdid.Id) []string {
		b, err := json.Marshal(subjects(ks...))
		x.NoError(err)
		var vs []string
		x.NoError(json.Unmarshal(b, &vs))
		for i, k := range ks {
			x.Equal(base64.StdEncoding.EncodeToString(k.Bytes()), vs[i], "the encoding the build before wrote")
		}
		return vs
	}

	t.Run("the access record", func(t *testing.T) {
		x := require.New(t)

		// Written by this build, through the recorder.
		conn := b.dialed(t, ctx)
		_, err := app.NewHolderServiceClient(conn).Get(b.travels(ctx), app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		rs := b.accessed(t, ctx)
		x.NotEmpty(rs)
		r := rs[len(rs)-1]
		x.Equal(subjects(alice), r.Subjects, "read back equal")
		x.Equal(before(alice), column(entaccess.Table, entaccess.FieldSubjects, r.Id), "stored as before")

		// Written as the build before wrote it, and read back by this one.
		old, err := b.Ent.Access.Create().
			SetId(pdid.New(pd.AccessDomain).Uuid()).SetTenantId(r.TenantId).SetActorId(r.ActorId).SetTraceId([]byte{}).
			SetAction(r.Action).SetKind(r.Kind).SetResult(r.Result).SetActorTenantId(r.ActorTenantId).SetValue([]byte{}).
			SetSubjects(subjects(alice, b.Holder)).SetSubjectCount(2).
			Save(ctx)
		x.NoError(err)
		x.Equal(before(alice, b.Holder), column(entaccess.Table, entaccess.FieldSubjects, old.Id))
		again, err := b.Ent.Access.Get(ctx, old.Id)
		x.NoError(err)
		x.Equal(subjects(alice, b.Holder), again.Subjects)

		// And a person reading it reads identifiers.
		tree, err := pdcmd.New(pdcmd.Static(conn))
		x.NoError(err)
		got := xlitest.Harness{Cmd: &xli.Command{Name: "app", Commands: tree.Commands()}, Ctx: b.travels(ctx)}.Run(t,
			"access", "get", pdid.Id(r.Id).String())
		x.NoError(got.Err)
		x.Contains(got.Stdout, alice.String(), "a subject printed as the identifier it is")
		x.NotContains(got.Stdout, "16 bytes")
	})

	t.Run("what is owed", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Ent.Owed.Create().
			SetId(pdid.New(pd.OwedDomain).Uuid()).SetObject(alice.Uuid()).SetHeldBy(subjects(b.Tenant)).
			Save(ctx)
		x.NoError(err)
		x.Equal(before(b.Tenant), column(entowed.Table, entowed.FieldHeldBy, v.Id), "stored as before")
		again, err := b.Ent.Owed.Get(ctx, v.Id)
		x.NoError(err)
		x.Equal(subjects(b.Tenant), again.HeldBy, "read back equal")
	})
}
