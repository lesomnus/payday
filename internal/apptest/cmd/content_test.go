package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/protobuf-patch/patchpb"
	"github.com/lesomnus/protobuf-patch/patchproto"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/entuuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/config"
	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	"github.com/lesomnus/payday/internal/apptest/internal/ent"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
)

// What the trail's tests write into a robot: an alias, which is the robot's; an
// address, which is the person's who keeps it (`keeper`, declared personal);
// and a verifier, which is nobody's to read (`secret`).
const (
	keeperBefore = "kim@example.com"
	keeperAfter  = "lee@example.com"
)

var robotSecret = []byte("hunter2-and-something-long-enough-to-find")

// kept is a robot made and then changed, and the rows of the trail about it,
// oldest first: its Add, and the Patch that renamed it and changed its keeper.
func (b *built) kept(t *testing.T, ctx context.Context) (*app.Robot, []*ent.Audit) {
	t.Helper()
	x := require.New(t)

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
		Keeper: keeperBefore,
		Secret: robotSecret,
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Robot().Patch(ctx, app.RobotPatchRequest_builder{
		Ref:              app.RobotRef_builder{Id: v.GetId()}.Build(),
		Alias:            z.Ptr("arm-02"),
		Keeper:           z.Ptr(keeperAfter),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	rows := b.trailOf(t, ctx, v.GetId())
	x.Len(rows, 2)
	x.Equal(app.RobotService_Add_FullMethodName, rows[0].Action)
	x.Equal(app.RobotService_Patch_FullMethodName, rows[1].Action)

	return v, rows
}

// trailOf is every row of the trail about the row `id` names, oldest first.
func (b *built) trailOf(t *testing.T, ctx context.Context, id []byte) []*ent.Audit {
	t.Helper()

	k, err := entuuid.FromBytes(id)
	require.NoError(t, err)
	vs, err := b.Ent.Audit.Query().
		Where(entaudit.ObjectId(k)).
		Order(ent.Asc(entaudit.FieldDateCreated), ent.Asc(entaudit.FieldId)).
		All(ctx)
	require.NoError(t, err)

	return vs
}

// applied is a row of the trail's patch applied to an empty robot: what the
// write said, as the row it would make of nothing.
func applied(t *testing.T, row *ent.Audit) *app.Robot {
	t.Helper()

	var doc patchpb.Patch
	require.NoError(t, proto.Unmarshal(row.Patch, &doc))
	v, err := patchproto.Apply(&app.Robot{}, &doc)
	require.NoError(t, err)

	return v
}

// nothingOfThePerson is the assertion every level shares: the person's address
// and the verifier are nowhere in a row of the trail, as bytes, whatever the
// row holds.
func nothingOfThePerson(t *testing.T, row *ent.Audit) {
	t.Helper()

	for _, b := range [][]byte{row.Patch, row.Value} {
		require.NotContains(t, string(b), keeperBefore, "the trail holds the old address")
		require.NotContains(t, string(b), keeperAfter, "the trail holds the new address")
		require.NotContains(t, string(b), string(robotSecret), "the trail holds the verifier")
	}
}

// TestTheTrailKeepsWhatAWriteSaid: by default a row of the trail is the
// document a write was compiled from -- an Add's is the row it made -- with
// what was set to a personal or a secret field taken out, and the numbers of
// every field it set (#92).
func TestTheTrailKeepsWhatAWriteSaid(t *testing.T) {
	b, ctx := build(t)
	_, rows := b.kept(t, ctx)
	add, patch := rows[0], rows[1]

	t.Run("an Add is the row it made, less what is not the trail's", func(t *testing.T) {
		x := require.New(t)
		nothingOfThePerson(t, add)

		made := applied(t, add)
		x.Equal("arm-01", made.GetAlias())
		x.Equal(b.Tenant.Bytes(), made.GetTenant().GetId())
		x.Empty(made.GetKeeper())
		x.Empty(made.GetSecret())

		x.Subset(add.Fields, []uint32{2, 4, 8, 32}, "that it set the tenant, the alias, the secret and the keeper")
		x.Empty(add.Value, "the default keeps no row")
	})
	t.Run("a Patch is what it set, less the same", func(t *testing.T) {
		x := require.New(t)
		nothingOfThePerson(t, patch)

		x.Equal([]uint32{4, 32}, patch.Fields, "that it set the alias and the keeper, and nothing else")
		x.Equal("arm-02", applied(t, patch).GetAlias())
		x.Empty(applied(t, patch).GetKeeper())
		x.Empty(patch.Value)
	})
}

// TestTheTrailKeepsTheRowWhereItIsAskedTo: at the `value` level a row of the
// trail is the row as the write left it as well -- without the person's
// address and without the verifier.
func TestTheTrailKeepsTheRowWhereItIsAskedTo(t *testing.T) {
	x := require.New(t)
	b, ctx := keeping(t, "value")
	_, rows := b.kept(t, ctx)

	for _, row := range rows {
		nothingOfThePerson(t, row)

		var got app.Robot
		x.NoError(proto.Unmarshal(row.Value, &got))
		x.NotEmpty(got.GetAlias(), "the row it kept is not the robot")
		x.Empty(got.GetKeeper())
		x.Empty(got.GetSecret())
	}
	x.Equal([]uint32{4, 32}, rows[1].Fields)
	x.Equal("arm-02", applied(t, rows[1]).GetAlias(), "the patch is kept beside the row")
}

// TestTheTrailKeepsWhichFieldsAndNothingElseWhereItIsToldTo: at the `fields`
// level a row of the trail says which fields a write set and nothing they were
// set to -- and is still a row, with who, what and when on it.
func TestTheTrailKeepsWhichFieldsAndNothingElseWhereItIsToldTo(t *testing.T) {
	x := require.New(t)
	b, ctx := keeping(t, "fields")
	v, rows := b.kept(t, ctx)

	for _, row := range rows {
		x.Empty(row.Patch)
		x.Empty(row.Value)
		x.Equal(v.GetId(), row.ObjectId[:])
	}
	x.Equal([]uint32{4, 32}, rows[1].Fields)
}

// TestEachKindKeepsWhatItsKindIsTold: a kind `by:` names keeps its own level,
// and every other kind the deployment's.
func TestEachKindKeepsWhatItsKindIsTold(t *testing.T) {
	x := require.New(t)
	b, ctx := buildFrom(t, cmd.Config{
		Db:    dbOf(t),
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
		Audit: config.AuditConfig{
			Content: "fields",
			By:      map[string]config.AuditKeepConfig{"robot": {Content: "value"}},
		},
	})

	_, rows := b.kept(t, ctx)
	x.NotEmpty(rows[1].Value, "the robot's level is the one by: gave it")

	_, err := b.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref:              app.HolderRef_builder{Id: b.Holder.Bytes()}.Build(),
		Desc:             z.Ptr("on call"),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)
	hs := b.trailOf(t, ctx, b.Holder.Bytes())
	x.NotEmpty(hs)
	last := hs[len(hs)-1]
	x.Empty(last.Patch, "a holder's level is the deployment's")
	x.Empty(last.Value)
	x.Equal([]uint32{6}, last.Fields)
}

// TestAHolderIsSomebody: payday's own Holder declares what it calls somebody,
// and what is said and labelled of them, personal -- so the trail of a holder
// renamed says that it was, and not to what.
func TestAHolderIsSomebody(t *testing.T) {
	x := require.New(t)
	b, ctx := keeping(t, "value")

	_, err := b.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref:              app.HolderRef_builder{Id: b.Holder.Bytes()}.Build(),
		Name:             z.Ptr("Kim Somebody"),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	hs := b.trailOf(t, ctx, b.Holder.Bytes())
	x.NotEmpty(hs)
	last := hs[len(hs)-1]
	x.Equal([]uint32{5}, last.Fields)
	x.NotContains(string(last.Patch), "Kim Somebody")
	x.NotContains(string(last.Value), "Kim Somebody")
	x.NotContains(string(hs[0].Patch), "admin", "the alias it was made with")
}

// TestADocumentThatNamesAFieldByNameIsWithheldAllTheSame: an `Apply` is handed
// a document a caller wrote, which may name a field by its name or its JSON
// name and not its number. What the trail holds no value of is matched by all
// three -- matched by number alone, a verifier set by its name went onto the
// trail whole.
func TestADocumentThatNamesAFieldByNameIsWithheldAllTheSame(t *testing.T) {
	x := require.New(t)
	b, ctx := keeping(t, "value")

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	assign := func(f *patchpb.Field, v *patchpb.Value) *patchpb.Entry {
		return patchpb.Entry_builder{
			Targets: patchpb.Targets_builder{Selectors: []*patchpb.Selector{
				patchpb.Selector_builder{Key: patchpb.Key_builder{Field: f}.Build()}.Build(),
			}}.Build(),
			Assign: patchpb.Assign_builder{Value: v}.Build(),
		}.Build()
	}
	doc := patchpb.Patch_builder{
		MessageType: proto.String("app.Robot"),
		Delta: patchpb.Delta_builder{Entries: []*patchpb.Entry{
			assign(patchpb.Field_builder{Name: proto.String("keeper")}.Build(), patchpb.Value_builder{S: proto.String(keeperAfter)}.Build()),
			assign(patchpb.Field_builder{JsonName: proto.String("secret")}.Build(), patchpb.Value_builder{X: robotSecret}.Build()),
			assign(patchpb.Field_builder{Name: proto.String("alias")}.Build(), patchpb.Value_builder{S: proto.String("arm-02")}.Build()),
		}}.Build(),
	}.Build()
	_, err = b.Ungated.Robot().Apply(ctx, app.RobotApplyRequest_builder{
		Ref:   app.RobotRef_builder{Id: v.GetId()}.Build(),
		Patch: doc,
	}.Build())
	x.NoError(err)

	rows := b.trailOf(t, ctx, v.GetId())
	x.Len(rows, 2)
	last := rows[1]
	nothingOfThePerson(t, last)
	x.Equal([]uint32{4, 8, 32}, last.Fields, "a field named by its name is a field set")
	x.Contains(string(last.Patch), "arm-02", "what is the trail's stays on it")
}

// TestAContentLevelIsAWordTheTrailKnows: a level that is not one, and a kind
// this app does not have, are refused where the configuration is read -- a
// trail told to keep more than it keeps is found out by somebody looking for
// what is not there.
func TestAContentLevelIsAWordTheTrailKnows(t *testing.T) {
	x := require.New(t)

	_, err := config.AuditConfig{Content: "values"}.Contents()
	x.ErrorContains(err, "values")

	_, err = config.AuditConfig{By: map[string]config.AuditKeepConfig{"robot": {Content: "row"}}}.Contents()
	x.ErrorContains(err, "by.robot.content")

	_, err = config.AuditConfig{By: map[string]config.AuditKeepConfig{"spaceship": {Content: "value"}}}.Contents()
	x.ErrorContains(err, "spaceship")

	v, err := config.AuditConfig{Content: " VALUE ", By: map[string]config.AuditKeepConfig{"robot": {Content: "fields"}}}.Contents()
	x.NoError(err)
	x.Equal("value", v.Default.String())
	x.Len(v.By, 1)
}
