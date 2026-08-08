package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/header"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// TestTheTrailCanSayWhatTheThingWasCalled is the half that was missing.
//
// A trail row names what changed by identifier, and the domain byte says what
// **kind** of thing it was -- so "somebody erased a Robot" could always be
// written and "somebody erased arm-01" could not. Reading the row back needs a
// type nothing generic has.
//
// This is that read, over any entity, with no switch anybody has to remember to
// extend when the schema grows.
func TestTheTrailCanSayWhatTheThingWasCalled(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	rows, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)

	row := rows[len(rows)-1]
	k := pdid.Id(row.ObjectID)
	x.Equal(pd.RobotDomain, k.Domain(), "the identifier says which kind")

	// And which one, by reading it back through whichever service that domain
	// belongs to. The domain is what turns an identifier into the question
	// "read this from over there".
	got, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
		Ref: app.RobotRef_builder{Id: k.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	h := header.Of(got)
	x.Equal("arm-01", h.Title())
	x.Equal(v.GetId(), h.Id.Bytes())
	x.Equal(b.Tenant, h.Tenant, "read from the edge")
}

// TestOneReadWorksOnEveryEntity, which is the whole of why it is reflective:
// there is nothing to extend when the schema grows.
func TestOneReadWorksOnEveryEntity(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	tenant, err := b.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
		Ref: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	h := header.Of(tenant)
	x.Equal("acme", h.Alias)
	x.True(h.Has(header.Labels), "Tenant declares labels")
	x.False(h.Has(header.Tenant), "a tenant is not held by one")

	holder, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: b.Holder.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	h = header.Of(holder)
	x.Equal("admin", h.Alias)
	x.True(h.Has(header.Tenant))
	x.True(h.Has(header.Erased), "a Holder is erased softly")
}

// TestAnEntityThatHasNoneOfItSaysSo is why presence is not required.
//
// payday's own Audit has none of it: its early fields are the trace, the
// action, the object and the patch -- structural rather than descriptive. A
// rule that made every entity carry a name would be a rule payday exempts
// itself from on the first day.
func TestAnEntityThatHasNoneOfItSaysSo(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	rows, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	x.NotEmpty(rows)

	got, err := b.Walled.Audit().Get(b.as(ctx), app.AuditGetRequest_builder{
		Ref: app.AuditRef_builder{Id: rows[len(rows)-1].ID[:]}.Build(),
	}.Build())
	x.NoError(err)

	h := header.Of(got)
	x.True(h.Has(header.Id))
	x.False(h.Has(header.Alias), "an Audit row has no name and should not pretend to")
	x.False(h.Has(header.Name))

	// The tenant is still found: a trail row names it with a **column** rather
	// than an edge, so that it can outlive the tenant it points at.
	x.True(h.Has(header.Tenant))
	x.Equal(b.Tenant, h.Tenant)

	// And the title falls back to the one thing it does have.
	x.Equal(h.Id.String(), h.Title())
}

// TestAbsentIsNotEmpty, which is the only reason Has exists.
//
// An entity with no `name` and an entity whose name is "" are different things
// to a page: the first is a reason to fall back to something else and the
// second is a person who cleared the field.
func TestAbsentIsNotEmpty(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "empty"}.Build())
	x.NoError(err)

	h := header.Of(v)
	x.True(h.Has(header.Name), "Tenant declares a name")
	x.Empty(h.Name, "and nobody wrote one")
	x.Equal("empty", h.Title(), "so the title is the alias")

	// Written, and now it is the title.
	got, err := b.Ungated.Tenant().Patch(ctx, app.TenantPatchRequest_builder{
		Ref:         app.TenantRef_builder{Id: v.GetId()}.Build(),
		Name:        z.Ptr("Acme Corporation"),
		DateUpdated: v.GetDateUpdated(),
	}.Build())
	x.NoError(err)
	x.Equal("Acme Corporation", header.Of(got).Title())
}

// TestNothingIsAZeroHeader, so that every caller holding a reference that
// resolved to nothing is saved a check.
func TestNothingIsAZeroHeader(t *testing.T) {
	x := require.New(t)

	var v *app.Robot
	h := header.Of(v)
	x.False(h.Has(header.Id))
	x.Empty(h.Title())
}
