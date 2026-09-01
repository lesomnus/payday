package cmd_test

import (
	"testing"

	entsql "github.com/protobuf-orm/ent/dialect/sql"
	"github.com/protobuf-orm/ent/dialect/sql/sqljson"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/holder"
)

// A field of an entity may be a message, and this is where that is run.
//
// `Holder.profile` is added by this app's overlay -- see
// `proto/ext/payday/holder.ext.proto` and `proto/app/profile.proto` -- and the
// reason it exists is that nothing here had such a field before. Which is
// exactly why the failure lasted: a message field became an `encoding/json`
// column, every message payday generates uses the opaque Api and so has no
// exported fields at all, and every value round-tripped as `{}`. No error, no
// failing build, and an `Add` that answered with what it had been handed -- so
// it looked stored until somebody read it back, which nothing here did.
//
// The three tests below are the three claims the schema docs make about such a
// field: it comes back, a patch replaces it whole, and it is stored as protobuf
// Json -- whose *names* are the storage.

// TestAMessageFieldComesBack is the one the historical bug would have failed.
func TestAMessageFieldComesBack(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:   "ada",
		Profile: app.Profile_builder{DisplayName: "Ada Lovelace", HowMany: 3}.Build(),
	}.Build())
	x.NoError(err)

	// Read back rather than believed. What the `Add` answers with is the value
	// the server was handed, so it was right throughout the years this was
	// broken; the row is the only witness worth having.
	got, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: v.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	p := got.GetProfile()
	x.NotNil(p, "the field came back unset, which is one way of storing nothing")
	x.Equal("Ada Lovelace", p.GetDisplayName())
	x.Equal(int32(3), p.GetHowMany())

	// Said separately because the two above are what an empty message answers
	// with anyway: `{}` unmarshals into a `Profile` whose getters return "" and
	// 0, and a test that only compared those would have passed against the bug
	// had either value been the zero one.
	x.NotEmpty(p.GetDisplayName(), "the value round-tripped as an empty message")
}

// TestAPatchReplacesAMessageWhole is the documented semantics, and the one an
// app is most likely to be surprised by.
//
// The field is one value to the database -- a column holding a document, not a
// set of columns -- so there is nothing for a patch to merge *into*. It assigns
// what it was given, and what the new message does not carry is gone. Which is
// usually right for a profile, that being what one source said at one moment,
// and worth knowing before storing something a caller updates a field of.
func TestAPatchReplacesAMessageWhole(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:   "ada",
		Profile: app.Profile_builder{DisplayName: "Ada Lovelace", HowMany: 3}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(int32(3), v.GetProfile().GetHowMany(), "nothing was written, so the patch below replaces nothing")

	// One field set, the other left alone -- which for a message field is not
	// "left alone" at all.
	_, err = b.Ungated.Holder().Patch(ctx, app.HolderPatchRequest_builder{
		Ref: app.HolderRef_builder{Id: v.GetId()}.Build(),
		// The version, which every patch of a watchable entity carries.
		DateUpdated: v.GetDateUpdated(),
		Profile:     app.Profile_builder{DisplayName: "A. Lovelace"}.Build(),
	}.Build())
	x.NoError(err)

	got, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: v.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	x.Equal("A. Lovelace", got.GetProfile().GetDisplayName())
	x.Zero(got.GetProfile().GetHowMany(), "the patch merged into the stored message rather than replacing it")
}

// TestAMessageFieldIsReachableFromSql is the escape hatch the guide offers to
// an app that has to reach inside one after all, and the check that the names
// it tells them to use are the names that are there.
//
// It cannot be filtered or indexed through payday: there is no generated
// predicate for a field of it and no `List` filter, because it is one value to
// the database. ent's `sqljson` is the way down to the column anyway, and what
// this pins is the **spelling**: the column holds canonical protobuf Json, so
// the path is `howMany` and not the `how_many` the schema is written in.
//
// Which is the same cost stated twice. Renaming a field of a message stored
// this way does not break a build and does not break the wire -- it silently
// stops finding the old value, and this is the only place that would say so.
func TestAMessageFieldIsReachableFromSql(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant:  app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:   "ada",
		Profile: app.Profile_builder{DisplayName: "Ada Lovelace", HowMany: 3}.Build(),
	}.Build())
	x.NoError(err)

	at := func(path string, v any) int {
		n, err := b.Ent.Holder.Query().
			Where(func(s *entsql.Selector) {
				s.Where(sqljson.ValueEQ(holder.FieldProfile, v, sqljson.Path(path)))
			}).
			Count(ctx)
		x.NoError(err)

		return n
	}

	x.Equal(1, at("displayName", "Ada Lovelace"))
	x.Equal(1, at("howMany", 3))

	// And the spelling the schema is written in finds nothing, which is what
	// makes the two above a claim rather than a coincidence.
	x.Zero(at("how_many", 3), "the column is keyed by the proto field name, so the guide is wrong")
}
