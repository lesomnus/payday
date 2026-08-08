package cmd_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/ncruces/go-sqlite3/vfs/memdb"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
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
