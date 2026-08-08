package cmd_test

import (
	"testing"

	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/pderr"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// TestANameIsFoldedOnTheWayIn is the half that is not a refusal.
//
// "  Arm-01 " and "arm-01" are one row to a person and two to a unique index,
// and the only place that difference can be closed once is here. Closed
// anywhere else and it is closed differently in each place that remembered.
func TestANameIsFoldedOnTheWayIn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	req := app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "  Arm-01 ",
	}.Build()

	v, err := b.Walled.Robot().Add(b.as(ctx), req)
	x.NoError(err)
	x.Equal("arm-01", v.GetAlias())

	// And the row holds the folded name rather than the server having answered
	// with one and stored the other.
	row, err := b.Ent.Robot.Get(ctx, must(pdid.From(v.GetId())).Uuid())
	x.NoError(err)
	x.Equal("arm-01", row.Alias)

	// The caller's message is untouched. For a call made in this process it is
	// one they may still be holding, and a server that folded a field they can
	// read back would be changing a value out from under them.
	x.Equal("  Arm-01 ", req.GetAlias())
}

// TestANameThatIsNotOneIsRefused is what keeps `@acme/arm-01` able to name
// anything at all.
//
// Reachable by identifier is not the same as reachable. A row called
// "Not An Alias!!" cannot be written in a config file, said over a phone, or
// put in a certificate -- and nothing about writing it fails, which is why the
// rule has to be here rather than in whatever reads it later.
func TestANameThatIsNotOneIsRefused(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	for _, v := range []string{"Not An Alias!!", "-leading", "trailing-", "9front", "a--b"} {
		_, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  v,
		}.Build())
		x.Equal(codes.InvalidArgument, status.Code(err), "%q was taken", v)

		// And it says which box, not just that something was wrong. This is the
		// whole of what a form needs and the whole of what a message cannot give
		// it without becoming a wire format nobody declared.
		vs := pderr.Violations(err)
		x.Len(vs, 1, "%q", v)
		x.Equal("alias", vs[0].Field, "%q", v)
	}
}

// TestTheDeploymentPathIsNotAWayAround is why this is on the Sink and not in a
// layer.
//
// `Ungated` is the server the deployment does its own work through -- no wall,
// no gate. A layer in front of the served stack would have left it able to
// write a name the served stack refuses, and a row written that way is found
// months later by somebody asking why `@acme/Ops Team` does not resolve.
func TestTheDeploymentPathIsNotAWayAround(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "Ops Team"}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Equal("alias", pderr.Violations(err)[0].Field)

	v, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: " OPS "}.Build())
	x.NoError(err)
	x.Equal("ops", v.GetAlias())
}

// TestAPatchSaysNothingAboutANameItDidNotMention is the difference between a
// field that is absent and one that is empty.
//
// Refusing a patch that does not carry the alias would make every patch of any
// other field carry the name along, which is a request that overwrites what it
// did not mean to.
func TestAPatchSaysNothingAboutANameItDidNotMention(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	// Nothing said about the name. The version comes along because this entity
	// declares one -- a patch of a versioned row is a compare-and-set, and what
	// a client compares with is the copy it is holding.
	got, err := b.Walled.Robot().Patch(b.as(ctx), app.RobotPatchRequest_builder{
		Ref:         app.RobotRef_builder{Id: v.GetId()}.Build(),
		DateUpdated: v.GetDateUpdated(),
	}.Build())
	x.NoError(err)
	x.Equal("arm-01", got.GetAlias())

	// Said, and folded.
	got, err = b.Walled.Robot().Patch(b.as(ctx), app.RobotPatchRequest_builder{
		Ref:         app.RobotRef_builder{Id: v.GetId()}.Build(),
		Alias:       z.Ptr(" Arm-02 "),
		DateUpdated: got.GetDateUpdated(),
	}.Build())
	x.NoError(err)
	x.Equal("arm-02", got.GetAlias())

	// Said, and not a name.
	_, err = b.Walled.Robot().Patch(b.as(ctx), app.RobotPatchRequest_builder{
		Ref:         app.RobotRef_builder{Id: v.GetId()}.Build(),
		Alias:       z.Ptr("Arm 03"),
		DateUpdated: got.GetDateUpdated(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Equal("alias", pderr.Violations(err)[0].Field)
}

// TestAFoldedNameIsFoundByTheNameThatWasWritten is what the folding was for.
func TestAFoldedNameIsFoundByTheNameThatWasWritten(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "ARM-01",
	}.Build())
	x.NoError(err)

	v, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
		Ref: app.RobotRef_builder{
			Slug: app.RobotRefBySlug_builder{
				Alias:  z.Ptr("arm-01"),
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("arm-01", v.GetAlias())
}

// sink is the innermost server with a namer on it, which is where the hook
// lives: in front of the database and behind everything else, so both the
// served stack and the deployment's own path go through it.
func (b *built) sink(t *testing.T, n slug.Namer) app.Server {
	t.Helper()

	s, err := pd.NewSink(b.Ent, bare.WithMinter(pd.Minter()))
	require.NoError(t, err)

	return s.WithNamer(n)
}

// TestARowNobodyNamedIsGivenOne is the default, and it is the default for the
// reason `bare.Minter` makes a key up.
//
// A row needs an identity the caller may not have an opinion about, and payday
// already supplies one of the two -- an Add with no `id` gets a fresh uuid and
// nobody calls that dangerous. A name is that decision one field over.
func TestARowNobodyNamedIsGivenOne(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
	}.Build())
	x.NoError(err)

	// A name, and one `@acme/...` can reach -- which is the whole of what an
	// alias is for.
	x.NotEmpty(v.GetAlias())
	x.NoError(slug.Validate(v.GetAlias()))

	got, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
		Ref: app.RobotRef_builder{
			Slug: app.RobotRefBySlug_builder{
				Alias:  z.Ptr(v.GetAlias()),
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			}.Build(),
		}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal(v.GetId(), got.GetId())
}

// TestTwoRowsNobodyNamedDoNotCollide, which is what makes the default usable at
// all: an entity nobody names is one where every Add would otherwise be an
// AlreadyExists on the empty string.
func TestTwoRowsNobodyNamedDoNotCollide(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	seen := map[string]bool{}
	for range 8 {
		v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		x.False(seen[v.GetAlias()], "two rows were named %q", v.GetAlias())
		seen[v.GetAlias()] = true
	}
}

// TestANameThatWasGivenIsStillJudged. Making one up is what happens when
// nothing was said, and not a repair of what was.
func TestANameThatWasGivenIsStillJudged(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "Not An Alias",
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Equal("alias", pderr.Violations(err)[0].Field)
}

// TestAnAppCanSayARowHasToBeNamed is the other way round, per entity.
//
// What it buys is not safety -- a made-up name is visible on the first screen
// that draws one -- it is **feedback**: a client that dropped the field is told
// so, rather than writing a row somebody has to find and rename.
func TestAnAppCanSayARowHasToBeNamed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	s := b.sink(t, slug.RequiredFor(nil, "app.Robot"))

	_, err := s.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Equal("alias", pderr.Violations(err)[0].Field)

	// And the entity that was not in the list still gets one made up, which is
	// the point of it being per entity rather than a switch on the server.
	v, err := s.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	joint, err := s.Joint().Add(ctx, app.JointAddRequest_builder{
		Robot: app.RobotRef_builder{Id: v.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.NotEmpty(joint.GetAlias())
}

// TestRequiredEverywhereIsOneLine, for a deployment that wants it.
func TestRequiredEverywhereIsOneLine(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.sink(t, slug.Required()).Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
}

// TestANamerIsNotAskedToRenameSomethingThatExists is the difference from a
// minter that is worth having a test for.
//
// A namer decides the name of a row being made. A patch that cleared the alias
// is a caller asking for something invalid, and answering it with an invented
// name would hand them a row they did not ask for.
func TestANamerIsNotAskedToRenameSomethingThatExists(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	s := b.sink(t, nil)

	v, err := s.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	_, err = s.Robot().Patch(ctx, app.RobotPatchRequest_builder{
		Ref:         app.RobotRef_builder{Id: v.GetId()}.Build(),
		Alias:       z.Ptr(""),
		DateUpdated: v.GetDateUpdated(),
	}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err), "the row was renamed to something nobody asked for")
}
