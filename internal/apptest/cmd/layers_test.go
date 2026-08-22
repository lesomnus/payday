package cmd_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/audit"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/predicate"
	"github.com/lesomnus/payday/internal/apptest/internal/ent/robot"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// cmd0 is the configuration these tests build with; the database is named per
// test and everything else is what an app gets by saying nothing.
//
// Except the general writes, which are open here and closed everywhere else.
// They are how the servers write and not how a caller asks -- an app writes the
// RPC it means and implements it with one -- but this app has no such RPC yet,
// and a test that could not change a row could not say what a Watch does when
// one changes.
var cmd0 = cmd.Config{
	Server: config.ServerConfig{AllowGeneralWrites: true},
}

// built is an app on an empty database, with the first tenant and holder there.
type built struct {
	*cmd.Server

	Tenant pdid.Id
	Holder pdid.Id
}

func build(t *testing.T) (*built, context.Context) {
	t.Helper()

	// Named, because payday refuses to pick one. A test is one process, so the
	// answer here is the easy one -- and having to write it is the point: the
	// same line in a deployment of two replicas is a line somebody has to look
	// at.
	return buildWith(t, config.WatchConfig{Broker: config.BrokerMemory})
}

// buildWith is [build] for a test that is about the watch configuration itself.
func buildWith(t *testing.T, w config.WatchConfig) (*built, context.Context) {
	t.Helper()

	return buildOn(t, dbOf(t), w)
}

// buildOn is [buildWith] on a database somebody already has.
//
// Which is how a test gets **two replicas**: one process each, one database
// between them, and nothing else connecting the two. Everything a second server
// needs seeding is already there, so this skips it and answers with the tenant
// and holder the first one made.
func buildOn(t *testing.T, db config.DbConfig, w config.WatchConfig) (*built, context.Context) {
	t.Helper()
	x := require.New(t)
	ctx := t.Context()

	s, err := cmd.Build(ctx, cmd.Config{
		Db:    db,
		Watch: w,
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))

	// Through the ungated server, which is the point: there is nobody to be
	// inside a tenant before there is a tenant.
	//
	// The second server on one database finds it already there, which is what
	// `Get` answers with rather than a second `Add` failing on the alias.
	tenant, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	if err != nil {
		tenant, err = s.Ungated.Tenant().Get(ctx, app.TenantGetRequest_builder{
			Ref: app.TenantRef_builder{Alias: z.Ptr("acme")}.Build(),
		}.Build())
	}
	x.NoError(err)
	tk, err := pdid.From(tenant.GetId())
	x.NoError(err)

	holder, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tenant.GetId()}.Build(),
		Alias:  "admin",
	}.Build())
	if err != nil {
		// A second server on one database finds the seed already there. Listed
		// rather than named, because a `Holder` is unique within a tenant and
		// its reference says so in a shape a one-line lookup does not have.
		vs, e := s.Ungated.Holder().List(ctx, app.HolderListRequest_builder{}.Build())
		x.NoError(e)
		x.NotEmpty(vs.GetItems())

		holder, err = vs.GetItems()[0], nil
	}
	x.NoError(err)
	hk, err := pdid.From(holder.GetId())
	x.NoError(err)

	return &built{Server: s, Tenant: tk, Holder: hk}, ctx
}

// grpc is this app's server, built.
//
// A helper because [cmd.Server.Grpc] answers with an error now -- a certificate
// that cannot be read is a server that must not start -- and unpacking that at
// twenty call sites would say the same thing twenty times.
func (b *built) grpc(t *testing.T, opts ...grpc.ServerOption) *grpc.Server {
	t.Helper()

	g, err := b.Grpc(t.Context(), cmd0, opts...)
	require.NoError(t, err)

	return g
}

// as is a context carrying who a request is from, for a call made directly
// against a server rather than over a connection.
func (b *built) as(ctx context.Context) context.Context {
	f := frame.New(b.Holder, b.Tenant, frame.Whole()).WithScope(frame.Only(b.Tenant))
	return frame.Into(ctx, f)
}

// travels is [built.as] for a call that goes over a connection, where the frame
// cannot: it is a credential, and `auth` is what turns one into a frame.
//
// `Plain` believes what the caller writes, which is why a test can say who it is
// in one line -- and why it is not something to serve where anyone can reach it.
func (b *built) travels(ctx context.Context) context.Context {
	return auth.PlainProvider("@acme/admin").Provide(ctx)
}

// TestTenantIsNotPutUpFromInsideOne is the rule the wall cannot state, because
// the row does not exist yet and there is nothing to narrow.
//
// Refused to **everybody**: it is not about who is asking and no credential
// changes it. What does it is a server this layer is not in front of, which is
// what `Ungated` is -- an instance somebody was handed, which can be taken
// away, rather than a row somebody is, which cannot.
func TestTenantIsNotPutUpFromInsideOne(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Walled.Tenant().Add(b.as(ctx), app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.Equal(codes.Unimplemented, status.Code(err))

	_, err = b.Walled.Tenant().Erase(b.as(ctx), app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build())
	x.Equal(codes.Unimplemented, status.Code(err))

	// And the deployment's own path is not a privilege anybody satisfies; it
	// is a different server.
	_, err = b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)
}

// TestAHolderIsAddedToATenantYouCanSee is the other rule that is not a
// predicate.
//
// The check is a read of the tenant through the wall rather than a comparison
// against the scope, and the answer is NotFound rather than a refusal: that a
// tenant exists is itself something a caller who may not see it should not be
// told.
func TestAHolderIsAddedToATenantYouCanSee(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)

	_, err = b.Walled.Holder().Add(b.as(ctx), app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
		Alias:  "intruder",
	}.Build())
	x.Equal(codes.NotFound, status.Code(err))

	// And into their own, which is the same code path arriving at yes.
	_, err = b.Walled.Holder().Add(b.as(ctx), app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "second",
	}.Build())
	x.NoError(err)
}

// TestARowIsAddedToATenantYouCanSee is the same rule for the app's own
// entities, and it was not always true.
//
// The wall is a predicate, so it is on the query -- and an Add has no query.
// For a long time the check above existed for `Holder` alone, so a caller could
// put a Robot into a tenant it could not see: the identifier in the edge became
// a foreign key with nothing consulted. Not a read leak, since every read is
// narrowed and the row vanishes immediately afterwards -- which is exactly why
// nobody noticed. What it left behind was a row the victim reads as their own,
// usage they are billed for, and a trail row stamped with the **actor's**
// tenant, so the victim's audit log records nothing at all.
//
// Both ways of naming a tenant, because they take different paths: an
// identifier goes straight to the foreign key and an alias is resolved by a
// query that was not narrowed either.
func TestARowIsAddedToATenantYouCanSee(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)

	t.Run("by identifier", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
			Alias:  "planted",
		}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})

	t.Run("by alias", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Alias: z.Ptr("other")}.Build(),
			Alias:  "planted-2",
		}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})

	t.Run("and into their own, which is the same path arriving at yes", func(t *testing.T) {
		x := require.New(t)

		_, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  "mine",
		}.Build())
		x.NoError(err)
	})

	t.Run("and it reaches an entity that is walled through another row", func(t *testing.T) {
		x := require.New(t)

		// A Joint reaches the tenant through its Robot, so what is read is the
		// Robot -- and a Robot in a tenant this caller cannot see is NotFound
		// for the same reason.
		theirs, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
			Alias:  "theirs",
		}.Build())
		x.NoError(err)

		_, err = b.Walled.Joint().Add(b.as(ctx), app.JointAddRequest_builder{
			Robot: app.RobotRef_builder{Id: theirs.GetId()}.Build(),
			Alias: "planted-3",
		}.Build())
		x.Equal(codes.NotFound, status.Code(err))
	})
}

// TestTheTrailIsFiledUnderWhatChanged.
//
// The row is stamped with the tenant of the **thing that changed**, not of the
// actor -- so the tenant whose row it was can read what was done to it. Filed
// under the actor, a headquarters operator writing to a customer produced a
// record behind headquarters' wall, and the customer's trail said nothing
// happened. That is not repairable afterwards: nothing recorded which tenant it
// had been about.
func TestTheTrailIsFiledUnderWhatChanged(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)

	theirs, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
		Alias:  "theirs",
	}.Build())
	x.NoError(err)

	// Read it back through the server that sees everything, since the point is
	// which tenant it was filed under rather than who can read it here.
	vs, err := b.Ungated.Audit().List(ctx, app.AuditListRequest_builder{
		Filters: []*app.AuditFilter{
			app.AuditFilter_builder{ObjectId: theirs.GetId()}.Build(),
		},
	}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 1)

	row := vs.GetItems()[0]
	x.Equal(other.GetId(), row.GetTenantId(), "the trail was filed under the actor rather than the row")

	// And the actor's tenant is still recorded, so the write is attributable
	// from both ends. Here the actor is the deployment writing to itself, which
	// has no tenant at all -- what matters is that the column is its own.
	x.NotEqual(row.GetTenantId(), row.GetActorTenantId())

	// The state after the write, which is the only record of what an Add
	// created: `patch` is empty for one, and for an Erase the row is gone.
	x.NotEmpty(row.GetValue())

	var got app.Robot
	x.NoError(proto.Unmarshal(row.GetValue(), &got))
	x.Equal("theirs", got.GetAlias())
}

// TestTheTrailOfASoftEraseIsFiledUnderWhatWasErased.
//
// The record of an erasure is the one row the erased side cannot do without,
// and it used to be the one row that side could not read: the recorder read
// the row back through the bare server, whose every read narrows to the rows
// still here, so the just-stamped row answered NotFound and the record took
// the hard-erase path -- the actor's tenant, an empty value. The recorder
// reads past the erased filter now, so the trail says whose row it was and
// what it held, with the stamp on.
func TestTheTrailOfASoftEraseIsFiledUnderWhatWasErased(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// A headquarters shape: an operator held by one tenant, scoped to reach
	// another, erasing the other's row.
	hq, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "hq"}.Build())
	x.NoError(err)
	hk := must(pdid.From(hq.GetId()))

	mine, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	op := frame.New(pdid.New(pd.HolderDomain), hk, frame.Whole()).WithScope(frame.Only(hk, b.Tenant))
	_, err = b.Walled.Robot().Erase(frame.Into(ctx, op), app.RobotRef_builder{Id: mine.GetId()}.Build())
	x.NoError(err)

	vs, err := b.Ungated.Audit().List(ctx, app.AuditListRequest_builder{
		Filters: []*app.AuditFilter{
			app.AuditFilter_builder{ObjectId: mine.GetId()}.Build(),
		},
	}.Build())
	x.NoError(err)

	var row *app.Audit
	for _, r := range vs.GetItems() {
		if r.GetAction() == app.RobotService_Erase_FullMethodName {
			row = r
		}
	}
	x.NotNil(row, "the erase was not recorded")

	x.Equal(b.Tenant.Bytes(), row.GetTenantId(), "the erasure was filed under the actor rather than the erased")
	x.Equal(hk.Bytes(), row.GetActorTenantId())

	// The value is the row as the erase left it: everything it held, with the
	// stamp on -- the only account of what a row was at the moment it went.
	var got app.Robot
	x.NoError(proto.Unmarshal(row.GetValue(), &got))
	x.Equal("arm-01", got.GetAlias())
	x.True(got.HasDateErased(), "the value should be the row as the erase left it")
}

// TestTheTrailOfASoftEraseIsReadableByTheErased is the two-party property
// holding for the write it matters most for.
//
// The wall on the trail is the OR of the two tenant columns, so filing the
// erasure under the erased row's tenant is exactly what lets that tenant read
// it -- through its own scope, wide enough to see nothing of the actor's.
func TestTheTrailOfASoftEraseIsReadableByTheErased(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	hq, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "hq"}.Build())
	x.NoError(err)
	hk := must(pdid.From(hq.GetId()))

	mine, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	op := frame.New(pdid.New(pd.HolderDomain), hk, frame.Whole()).WithScope(frame.Only(hk, b.Tenant))
	_, err = b.Walled.Robot().Erase(frame.Into(ctx, op), app.RobotRef_builder{Id: mine.GetId()}.Build())
	x.NoError(err)

	// Read as the tenant whose row it was, through the wall.
	vs, err := b.Walled.Audit().List(b.as(ctx), app.AuditListRequest_builder{
		Filters: []*app.AuditFilter{
			app.AuditFilter_builder{ObjectId: mine.GetId()}.Build(),
		},
	}.Build())
	x.NoError(err)

	var found bool
	for _, r := range vs.GetItems() {
		found = found || r.GetAction() == app.RobotService_Erase_FullMethodName
	}
	x.True(found, "the tenant whose row was erased cannot read the record of the erasure")
}

// TestTheTrailOfAHardEraseIsFiledUnderTheActor pins the fallback the soft fix
// must not take with it.
//
// A hard erase leaves nothing to read -- the recorder runs inside the
// transaction, after the delete -- so the record is filed under the actor's
// tenant, the last thing known about it, and the value is empty. See
// `payday.Entity.Erase.hard` for why that is the accepted cost.
func TestTheTrailOfAHardEraseIsFiledUnderTheActor(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	hq, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "hq"}.Build())
	x.NoError(err)
	hk := must(pdid.From(hq.GetId()))

	mine, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	v, err := b.Walled.Reading().Add(b.as(ctx), app.ReadingAddRequest_builder{
		Robot: app.RobotRef_builder{Id: mine.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	op := frame.New(pdid.New(pd.HolderDomain), hk, frame.Whole()).WithScope(frame.Only(hk, b.Tenant))
	_, err = b.Walled.Reading().Erase(frame.Into(ctx, op), app.ReadingRef_builder{Id: v.GetId()}.Build())
	x.NoError(err)

	vs, err := b.Ungated.Audit().List(ctx, app.AuditListRequest_builder{
		Filters: []*app.AuditFilter{
			app.AuditFilter_builder{ObjectId: v.GetId()}.Build(),
		},
	}.Build())
	x.NoError(err)

	var row *app.Audit
	for _, r := range vs.GetItems() {
		if r.GetAction() == app.ReadingService_Erase_FullMethodName {
			row = r
		}
	}
	x.NotNil(row, "the erase was not recorded")

	x.Equal(hk.Bytes(), row.GetTenantId(), "a hard erase has nothing left to file under but the actor's own")
	x.Empty(row.GetValue())
}

// TestTheTrailOfAWriteThroughAnErasedParentIsFiledUnderThatParent is the half
// of the erased-inclusive read that no Erase reaches.
//
// The retry is keyed off the entity's shape rather than off the action, and
// that is the whole of why this case exists: a Joint reaches its tenant
// through a Robot, so an ordinary Patch of a live Joint reads a Robot that may
// already be softly erased. A stamped Robot is still the row that says whose
// the Joint is -- soft erasure makes a row unreadable, it does not make it
// somebody else's -- so the record belongs to that tenant. Falling back to the
// actor there loses the same thing the erase case loses, and loses it for a
// write nobody would think to look at: the owner's trail says nothing was done
// to their row.
func TestTheTrailOfAWriteThroughAnErasedParentIsFiledUnderThatParent(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	hq, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "hq"}.Build())
	x.NoError(err)
	hk := must(pdid.From(hq.GetId()))

	mine, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	j, err := b.Walled.Joint().Add(b.as(ctx), app.JointAddRequest_builder{
		Robot: app.RobotRef_builder{Id: mine.GetId()}.Build(),
		Alias: "elbow",
	}.Build())
	x.NoError(err)

	op := frame.New(pdid.New(pd.HolderDomain), hk, frame.Whole()).WithScope(frame.Only(hk, b.Tenant))
	_, err = b.Walled.Robot().Erase(frame.Into(ctx, op), app.RobotRef_builder{Id: mine.GetId()}.Build())
	x.NoError(err)

	// The Joint is untouched by that -- the wall reads the Robot's tenant and
	// not its stamp -- so what follows is an ordinary write, and the only
	// erased row anywhere near it is the one the tenant is read off.
	_, err = b.Walled.Joint().Patch(frame.Into(ctx, op), app.JointPatchRequest_builder{
		Ref:   app.JointRef_builder{Id: j.GetId()}.Build(),
		Alias: z.Ptr("shoulder"),
	}.Build())
	x.NoError(err)

	vs, err := b.Ungated.Audit().List(ctx, app.AuditListRequest_builder{
		Filters: []*app.AuditFilter{
			app.AuditFilter_builder{ObjectId: j.GetId()}.Build(),
		},
	}.Build())
	x.NoError(err)

	var row *app.Audit
	for _, r := range vs.GetItems() {
		if r.GetAction() == app.JointService_Patch_FullMethodName {
			row = r
		}
	}
	x.NotNil(row, "the patch was not recorded")

	x.Equal(b.Tenant.Bytes(), row.GetTenantId(), "a write through a softly erased parent was filed under the actor")
	x.Equal(hk.Bytes(), row.GetActorTenantId())

	// And it is still the Joint that was recorded, stamp-free: the parent's
	// erasure is not an event of the child's.
	var got app.Joint
	x.NoError(proto.Unmarshal(row.GetValue(), &got))
	x.Equal("shoulder", got.GetAlias())
	x.False(got.HasDateErased())
}

// TestTheTrailIsQueryable is what makes it a trail rather than a table.
func TestTheTrailIsQueryable(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	mine, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "mine",
	}.Build())
	x.NoError(err)

	for _, tc := range []struct {
		what string
		f    *app.AuditFilter
	}{
		{"what happened to this row", app.AuditFilter_builder{ObjectId: mine.GetId()}.Build()},
		{"what happened in this tenant", app.AuditFilter_builder{TenantId: b.Tenant.Bytes()}.Build()},
		{"what this tenant's people did", app.AuditFilter_builder{ActorTenantId: b.Tenant.Bytes()}.Build()},
	} {
		t.Run(tc.what, func(t *testing.T) {
			x := require.New(t)

			vs, err := b.Walled.Audit().List(b.as(ctx), app.AuditListRequest_builder{
				Filters: []*app.AuditFilter{tc.f},
			}.Build())
			x.NoError(err)
			x.NotEmpty(vs.GetItems())
		})
	}
}

// TestTheTrailIsNotWrittenByHand is what makes it evidence of anything.
func TestTheTrailIsNotWrittenByHand(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Walled.Audit().Add(b.as(ctx), app.AuditAddRequest_builder{
		Action: "/made.Up/Entirely",
	}.Build())
	x.Equal(codes.Unimplemented, status.Code(err))

	_, err = b.Walled.Audit().Erase(b.as(ctx), app.AuditRef_builder{Id: b.Holder.Bytes()}.Build())
	x.Equal(codes.Unimplemented, status.Code(err))
}

// TestEveryWriteIsOnTheTrail is the half the layer does not do.
//
// Nothing lists the RPCs. The generated servers call the recorder from inside
// the transaction that makes the write, so an RPC added to the schema is on the
// trail without anybody remembering to put it there.
func TestEveryWriteIsOnTheTrail(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	before, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	rows, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)
	x.Len(rows, before+1)

	row := rows[len(rows)-1]
	x.Equal(b.Holder.Uuid(), row.ActorID, "who did it")
	x.Equal(b.Tenant.Uuid(), row.TenantID, "on behalf of whom")
	x.Equal(v.GetId(), row.ObjectID[:], "and to which row")

	// The action is what the caller asked for, by the name gRPC knows it by.
	x.Equal(app.RobotService_Add_FullMethodName, row.Action)

	// An Add says everything it did in the action and the object, so there is
	// no document -- and the column holds bytes rather than a null, which is a
	// difference one driver keeps and another does not.
	x.NotNil(row.Patch)
	x.Empty(row.Patch)
}

// TestTheTrailTakesNoNullFromPayday is the column's contract rather than the
// row's meaning, and it is about the three the app never supplies:
// `trace_id`, `patch` and `value` are NOT NULL, and payday computes all three.
//
// Every one of them is **empty** for some ordinary write -- a call nobody
// traced, an Add that was not compiled from a document, a row that is not
// there to be read back -- and empty is the one value the two databases
// disagree about: pgx sends a nil `[]byte` as SQL NULL, and the SQLite driver
// makes it an empty blob. So the assertion below is really the write
// **succeeding**, and it can only fail on PostgreSQL: without `PDTEST_POSTGRES`
// naming one it asserts a nil the driver has already taken away, and passes on
// a tree where every one of these is nil.
//
// The writes are the ones that reach a path with no row behind it, which is
// where every nil of this kind has come from so far: an entity declared
// `global: {}` is not in `subject`'s switch at all, and an entity erased hard
// has no row left by the time the recorder reads for one.
func TestTheTrailTakesNoNullFromPayday(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	arm, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	for _, tc := range []struct {
		what  string
		write func(x *require.Assertions) []byte
	}{
		{
			"an entity with no tenant to file it under",
			func(x *require.Assertions) []byte {
				v, err := b.Ungated.Fleet().Add(ctx, app.FleetAddRequest_builder{Alias: "east"}.Build())
				x.NoError(err)

				return v.GetId()
			},
		},
		{
			"an erase that took the row with it",
			func(x *require.Assertions) []byte {
				v, err := b.Ungated.Reading().Add(ctx, app.ReadingAddRequest_builder{
					Robot:   app.RobotRef_builder{Id: arm.GetId()}.Build(),
					Celsius: 21.5,
				}.Build())
				x.NoError(err)

				_, err = b.Ungated.Reading().Erase(ctx, app.ReadingRef_builder{Id: v.GetId()}.Build())
				x.NoError(err)

				return v.GetId()
			},
		},
	} {
		t.Run(tc.what, func(t *testing.T) {
			x := require.New(t)
			k, err := uuid.FromBytes(tc.write(x))
			x.NoError(err)

			// Off the database rather than through a server: what is under
			// test is what the column holds, and a read through the trail's
			// own service would answer with a message that has no way to say
			// the difference.
			rows, err := b.Ent.Audit.Query().Where(entaudit.ObjectIDEQ(k)).All(ctx)
			x.NoError(err)
			x.NotEmpty(rows)

			for _, row := range rows {
				x.NotNil(row.TraceID, "trace_id")
				x.NotNil(row.Patch, "patch")
				x.NotNil(row.Value, "value")
			}
		})
	}
}

// TestAWriteNothingCouldRecordIsUndone is [TestEveryWriteIsOnTheTrail] from the
// side that makes it a fact about the database rather than about the order two
// statements were issued in.
//
// A recorder is called from inside the transaction that makes the write, so a
// refusal is not a write that happened beside a record that did not: it is no
// write at all. That is the promise on `bare.Recorder`, and it is what
// docs/guide/permissions.md is saying with "the trail and the data hold or
// fall together".
//
// The trail recorder is kept ahead of the refusing one, in the order
// `cmd.Build` writes them, because the stronger thing to show is the row it had
// already written going too. A trail appended after the commit could not do
// that: whichever of the two failed, what is left is a record of a write that
// was undone or a write nothing accounts for.
func TestAWriteNothingCouldRecordIsUndone(t *testing.T) {
	b, ctx := build(t)

	// A sink of its own rather than `b.Walled`, because a recorder is
	// something a server is handed when it is built and this test is about
	// which one. No wall on it: what is under test is a write nobody argued
	// with, and narrowing reads would only make the counts below harder to
	// read.
	sink := func(t *testing.T, rec bare.Recorder) pd.Sink {
		t.Helper()

		s, err := pd.NewSink(b.Ent,
			bare.WithMinter(pd.Minter()),
			bare.WithRecorder(bare.Recorders{pd.Recorder(), rec}),
		)
		require.NoError(t, err)

		return s
	}

	add := func(s pd.Sink, alias string) error {
		_, err := s.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  alias,
		}.Build())

		return err
	}

	// Read off the database rather than through a server, so that a row hidden
	// from a read and a row that is not there cannot be confused -- a soft
	// erase is the shape that makes those two look alike, and a rollback is
	// the claim that must not.
	counts := func(x *require.Assertions, alias string) (int, int) {
		rows, err := b.Ent.Robot.Query().Where(robot.AliasEQ(alias)).Count(ctx)
		x.NoError(err)

		trail, err := b.Ent.Audit.Query().Count(ctx)
		x.NoError(err)

		return rows, trail
	}

	t.Run("refused", func(t *testing.T) {
		x := require.New(t)

		_, before := counts(x, "arm-undone")

		err := add(sink(t, answers{err: errors.New("the queue is down")}), "arm-undone")

		// Internal whatever the recorder answered with, since keeping the
		// trail is this server's job rather than anything the caller asked
		// for; the words are kept so a log says which recorder gave up on
		// what. See `record`.
		x.Equal(codes.Internal, status.Code(err))
		x.ErrorContains(err, "the queue is down")

		rows, trail := counts(x, "arm-undone")
		x.Zero(rows, "the write a recorder refused is in the database")
		x.Equal(before, trail, "the trail row written before the refusal outlived the write it was about")
	})

	t.Run("and the same write, with a recorder that agrees", func(t *testing.T) {
		x := require.New(t)

		_, before := counts(x, "arm-01")

		x.NoError(add(sink(t, answers{}), "arm-01"))

		// Which is what says the subtest above is a rollback: the same sink,
		// the same call, one recorder answering differently. A write that was
		// never attempted -- refused by the wall, refused by the gate, dropped
		// before the statement -- leaves the same empty table behind, and only
		// this half tells the two apart.
		rows, trail := counts(x, "arm-01")
		x.Equal(1, rows)
		x.Equal(before+1, trail)
	})
}

// answers is a recorder that does nothing but answer, which is all a test of
// the transaction wants from one: what it says when it is asked is the whole
// of its behaviour, and the zero value is the recorder that agrees.
type answers struct{ err error }

func (r answers) Record(context.Context, bare.Server, bare.Change) error { return r.err }

// TestTheTrailNamesWhatChangedByKind is what the domain byte bought.
//
// A trail names what changed by identifier and not by table, because an
// identifier is unique across all of them. The standing cost of that was a row
// erased later leaving an identifier nothing answers to and no way to say what
// it used to be. It carries its own domain now, so the answer outlives the row.
//
// The entity here is the one that says `erase: {hard: {}}`, because it is the
// only one where the row really does go. Everything else is erased softly, and
// then the identifier answers to something the whole time -- which is a weaker
// version of the same test and not the one worth having.
func TestTheTrailNamesWhatChangedByKind(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	robot, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	v, err := b.Walled.Reading().Add(b.as(ctx), app.ReadingAddRequest_builder{
		Robot: app.RobotRef_builder{Id: robot.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	_, err = b.Walled.Reading().Erase(b.as(ctx), app.ReadingRef_builder{Id: v.GetId()}.Build())
	x.NoError(err)

	// The row is gone -- really gone, not stamped -- and the trail still says
	// what it was.
	_, err = b.Ent.Reading.Get(ctx, must(pdid.From(v.GetId())).Uuid())
	x.Error(err)

	rows, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)

	var erased bool
	for _, row := range rows {
		if row.Action != app.ReadingService_Erase_FullMethodName {
			continue
		}

		erased = true
		x.Equal(pd.ReadingDomain, pdid.Id(row.ObjectID).Domain(),
			"the identifier still says what it named")
	}
	x.True(erased, "the erase was not recorded")
}

// TestASoftErasedRowIsGoneToEveryReadAndStillThere.
//
// Saying nothing about erasure means softly, which is a different thing from
// nothing happening: the row cannot be read, cannot be changed, and its alias
// comes free -- and it is still there for the trail to have read.
func TestASoftErasedRowIsGoneToEveryReadAndStillThere(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	_, err = b.Walled.Robot().Erase(b.as(ctx), app.RobotRef_builder{Id: v.GetId()}.Build())
	x.NoError(err)

	_, err = b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
		Ref: app.RobotRef_builder{Id: v.GetId()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err), "a soft erase left the row readable")

	// The alias comes free, which is what the partial unique index is for.
	_, err = b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err, "the erased row is still holding its name")

	// And the row is there, which is what makes the trail able to say what it
	// held -- see `Audit.value`.
	got, err := b.Ent.Robot.Get(ctx, must(pdid.From(v.GetId())).Uuid())
	x.NoError(err)
	x.False(got.DateErased.IsZero())
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}

	return v
}

// TestTheWallReadsTheKeyRatherThanWalkingToIt is the wall's shape, and it is
// tested by what it hides rather than by what it renders.
//
// `HasTenantWith(tenant.IDIn(vs))` and `<foreign key> IN vs` answer the same
// question, and they answer it the same way **because the key is a foreign
// key**: a row cannot hold the identifier of a tenant that is not there. The
// integrity constraint is not the cost of the join, it is what makes the join
// skippable -- measured on SQLite, the walked form plans as a correlated
// subquery probing the tenant table once per row and this plans as a filter on
// the row's own column.
//
// Every hop but the last is still a walk. Two hops go from four plan steps with
// a CORRELATED SCALAR SUBQUERY to two without one.
func TestTheWallReadsTheKeyRatherThanWalkingToIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)

	// One of each, in each tenant, at every depth the schema has.
	mine, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "mine",
	}.Build())
	x.NoError(err)

	theirs, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
		Alias:  "theirs",
	}.Build())
	x.NoError(err)

	myJoint, err := b.Ungated.Joint().Add(ctx, app.JointAddRequest_builder{
		Robot: app.RobotRef_builder{Id: mine.GetId()}.Build(),
		Alias: "my-elbow",
	}.Build())
	x.NoError(err)

	theirJoint, err := b.Ungated.Joint().Add(ctx, app.JointAddRequest_builder{
		Robot: app.RobotRef_builder{Id: theirs.GetId()}.Build(),
		Alias: "their-elbow",
	}.Build())
	x.NoError(err)

	// One hop, collapsed onto the key.
	got, err := b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
		Ref: app.RobotRef_builder{Id: mine.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("mine", got.GetAlias())

	_, err = b.Walled.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
		Ref: app.RobotRef_builder{Id: theirs.GetId()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err), "the wall stopped narrowing when the walk was collapsed")

	// Two hops: the outer one is still a walk and the inner one is the key.
	gotJoint, err := b.Walled.Joint().Get(b.as(ctx), app.JointGetRequest_builder{
		Ref: app.JointRef_builder{Id: myJoint.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("my-elbow", gotJoint.GetAlias())

	_, err = b.Walled.Joint().Get(b.as(ctx), app.JointGetRequest_builder{
		Ref: app.JointRef_builder{Id: theirJoint.GetId()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err), "two hops stopped narrowing")

	// And a list, which is the read the collapse was for: it runs the predicate
	// against every row rather than one.
	page, err := b.Walled.Robot().List(b.as(ctx), app.RobotListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(page.GetItems(), 1)
	x.Equal("mine", page.GetItems()[0].GetAlias())
}

// TestAHookGivenTwiceIsRefused is the rule an app meets the first time it wires
// two of anything.
//
// Neither answer to "you gave it twice, what did you mean" is safe to pick.
// Replacing loses one -- the recorder that was going to write the trail, the
// scope that was the tenant wall -- and says nothing at the time; appending
// invents a rule nobody wrote, in an order nobody chose. So the option refuses,
// and `bare.Recorders` / `bare.Scopes` are where an app says which it meant.
func TestAHookGivenTwiceIsRefused(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	_, err := pd.NewSink(b.Ent,
		bare.WithScope(pd.Wall()),
		bare.WithScope(bare.Unscoped{}),
	)
	x.ErrorIs(err, bare.ErrTwice)
	x.ErrorContains(err, "Scopes{...}", "the refusal does not say where to say it")
}

// TestTheWallAndSomethingElse is what `Scopes` is for, and it is the shape an
// app reaches for the moment it has a rule of its own -- a region, a
// published flag, a soft archive.
//
// The wall is one of the list rather than something wrapped, so nothing has to
// re-implement what "narrows nothing" means: a scope says that with a nil
// predicate, and `Scopes` is where the And-of-whichever-are-not-nil is written
// once instead of once per app per entity.
func TestTheWallAndSomethingElse(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	mine, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "keep",
	}.Build())
	x.NoError(err)

	_, err = b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "drop",
	}.Build())
	x.NoError(err)

	s, err := pd.NewSink(b.Ent,
		bare.WithMinter(pd.Minter()),
		bare.WithScope(bare.Scopes{pd.Wall(), keeps{}}),
	)
	x.NoError(err)

	// Inside the wall and what the second scope keeps.
	got, err := s.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
		Ref: app.RobotRef_builder{Id: mine.GetId()}.Build(),
	}.Build())
	x.NoError(err)
	x.Equal("keep", got.GetAlias())

	// Inside the wall and not what it keeps: the same NotFound the wall gives,
	// because narrowing is narrowing whoever did it.
	_, err = s.Robot().Get(b.as(ctx), app.RobotGetRequest_builder{
		Ref: app.RobotRef_builder{Slug: app.RobotRefBySlug_builder{
			Alias:  z.Ptr("drop"),
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		}.Build()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err))
}

// keeps is an app's own narrowing: one rule about one entity, and nothing to
// say about the rest.
type keeps struct{ bare.Unscoped }

func (keeps) RobotScope(context.Context) (predicate.Robot, error) {
	return robot.AliasEQ("keep"), nil
}

// TestBothSidesOfAWriteAboutTwoTenantsReadIt.
//
// A write can be about two tenants, and the record of it is filed under one:
// `tenant_id` is read off the row after the write, so a row that moved from one
// tenant to another leaves the tenant it left with no record of the event that
// took it away. The row it most needs is the one it is not a party to.
//
// `audit.Concerning` is how the operation that knows says so, and the wall on
// the trail counts that column. It is a context and not a request field for the
// reason in that function: naming a tenant here grants that tenant a read.
func TestBothSidesOfAWriteAboutTwoTenantsReadIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)
	ok, err := pdid.From(other.GetId())
	x.NoError(err)

	mine, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "shared",
	}.Build())
	x.NoError(err)

	// A write on acme's row, said to be about `other` as well.
	_, err = b.Ungated.Robot().Patch(audit.Concerning(ctx, ok), app.RobotPatchRequest_builder{
		Ref:              app.RobotRef_builder{Id: mine.GetId()}.Build(),
		Alias:            z.Ptr("moved"),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)

	// The tenant the row is in reads it, as it always did.
	seen := func(who pdid.Id) []*app.Audit {
		f := frame.New(b.Holder, who, frame.Whole()).WithScope(frame.Only(who))

		vs, err := b.Walled.Audit().List(frame.Into(ctx, f), app.AuditListRequest_builder{
			Filters: []*app.AuditFilter{
				app.AuditFilter_builder{ObjectId: mine.GetId()}.Build(),
			},
		}.Build())
		x.NoError(err)

		return vs.GetItems()
	}

	x.NotEmpty(seen(b.Tenant), "the tenant the row is in cannot read its own trail")

	// And so does the other one -- only this row. The Add before it was about
	// nobody else and stays where it was.
	got := seen(ok)
	x.Len(got, 1, "the other tenant reads the write it was a party to, and nothing else")
	x.Equal(other.GetId(), got[0].GetCounterpartTenantId())
	x.Equal(b.Tenant.Bytes(), got[0].GetTenantId())

	// And a write about nobody else leaves the column empty. It is nullable for
	// this: a default on a uuid column means *the server picks one*, so for one
	// commit every ordinary row of the trail carried a **random tenant
	// identifier** in the column that decides who may read it. It matched
	// nobody and was still the wrong thing to have written.
	for _, row := range seen(b.Tenant) {
		if row.GetAction() == app.RobotService_Add_FullMethodName {
			x.Empty(row.GetCounterpartTenantId(), "a write about one tenant named a second")
		}
	}
}
