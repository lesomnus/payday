package cmd_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/lesomnus/z"
	"github.com/ncruces/go-sqlite3/vfs/memdb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
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
	x := require.New(t)
	ctx := t.Context()

	s, err := cmd.Build(ctx, cmd.Config{
		Db: config.DbConfig{
			Driver: "sqlite3",
			Dsn:    memdb.TestDB(t, url.Values{"_pragma": {"foreign_keys(1)"}}),
		},
		// Named, because payday refuses to pick one. A test is one process, so
		// the answer here is the easy one -- and having to write it is the
		// point: the same line in a deployment of two replicas is a line
		// somebody has to look at.
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	})
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(s.Ent.Schema.Create(ctx))

	// Through the ungated server, which is the point: there is nobody to be
	// inside a tenant before there is a tenant.
	tenant, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)
	tk, err := pdid.From(tenant.GetId())
	x.NoError(err)

	holder, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tenant.GetId()}.Build(),
		Alias:  "admin",
	}.Build())
	x.NoError(err)
	hk, err := pdid.From(holder.GetId())
	x.NoError(err)

	return &built{Server: s, Tenant: tk, Holder: hk}, ctx
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

// TestTheTrailNamesWhatChangedByKind is what the domain byte bought.
//
// A trail names what changed by identifier and not by table, because an
// identifier is unique across all of them. The standing cost of that was a row
// erased later leaving an identifier nothing answers to and no way to say what
// it used to be. It carries its own domain now, so the answer outlives the row.
func TestTheTrailNamesWhatChangedByKind(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Walled.Robot().Add(b.as(ctx), app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	_, err = b.Walled.Robot().Erase(b.as(ctx), app.RobotRef_builder{Id: v.GetId()}.Build())
	x.NoError(err)

	// The row is gone and the trail still says what it was.
	_, err = b.Ent.Robot.Get(ctx, must(pdid.From(v.GetId())).Uuid())
	x.Error(err)

	rows, err := b.Ent.Audit.Query().All(ctx)
	x.NoError(err)

	var erased bool
	for _, row := range rows {
		if row.Action != app.RobotService_Erase_FullMethodName {
			continue
		}

		erased = true
		x.Equal(pd.RobotDomain, pdid.Id(row.ObjectID).Domain(),
			"the identifier still says what it named")
	}
	x.True(erased, "the erase was not recorded")
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
