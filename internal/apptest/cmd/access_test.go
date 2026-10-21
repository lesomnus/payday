package cmd_test

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/flob"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/payday/trail"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/internal/ent"
	entaccess "github.com/lesomnus/payday/internal/apptest/internal/ent/access"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// person adds a holder to the tenant, which is somebody: what it is called is
// declared `personal`, so an answer that carries it carries their data.
func (b *built) person(t *testing.T, ctx context.Context, alias string) pdid.Id {
	t.Helper()

	v, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  alias,
	}.Build())
	require.NoError(t, err)

	return must(pdid.From(v.GetId()))
}

// accessed is the access record once what waits has been written, oldest
// first.
func (b *built) accessed(t *testing.T, ctx context.Context) []*ent.Access {
	t.Helper()
	require.NoError(t, b.Access.Flush(ctx))

	vs, err := b.Ent.Access.Query().Order(ent.Asc(entaccess.FieldDateCreated, entaccess.FieldId)).All(ctx)
	require.NoError(t, err)

	return vs
}

// subjects is the identifiers as a row of the access record holds them.
func subjects(ks ...pdid.Id) [][]byte {
	out := make([][]byte, len(ks))
	for i, k := range ks {
		out[i] = k.Bytes()
	}

	return out
}

// detail is what a row says beyond its columns: where the call came from, what
// it asked, and why.
func detail(t *testing.T, v *ent.Access) *pdpb.AccessDetail {
	t.Helper()

	d := &pdpb.AccessDetail{}
	require.NoError(t, proto.Unmarshal(v.Value, d))

	return d
}

// TestAReadIsRecordedOfWhoseDataItCarried is payday#111 in one call: a list
// of people is a row of the access record naming each of them -- who asked,
// from where, what they asked, and how it ended -- read off what the schema
// declared `personal` rather than written by the app.
func TestAReadIsRecordedOfWhoseDataItCarried(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	alice := b.person(t, ctx, "alice")
	bob := b.person(t, ctx, "bob")

	conn := pdtest.Serve(t, b.grpc(t))
	as := b.travels(ctx)

	vs, err := app.NewHolderServiceClient(conn).List(as, app.HolderListRequest_builder{Size: 10}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 3)

	// Queued, to be written with the others of its second.
	x.Equal(1, b.Access.Pending())
	n, err := b.Ent.Access.Query().Count(ctx)
	x.NoError(err)
	x.Zero(n, "a read was written before its second was up")

	rs := b.accessed(t, ctx)
	x.Len(rs, 1)
	r := rs[0]
	x.Equal("read", r.Kind)
	x.Equal(app.HolderService_List_FullMethodName, r.Action)
	x.Equal("ok", r.Result)
	x.Equal(b.Tenant.Uuid(), r.TenantId, "filed under the tenant whose data it was")
	x.Equal(b.Holder.Uuid(), r.ActorId)
	x.Equal(b.Tenant.Uuid(), r.ActorTenantId)
	x.EqualValues(pd.AccessDomain, r.Domain)
	x.ElementsMatch(subjects(b.Holder, alice, bob), r.Subjects)
	x.EqualValues(3, r.SubjectCount)

	d := detail(t, r)
	x.NotEmpty(d.GetAddress(), "where the call came from")
	x.Empty(d.GetReason())

	asked := &app.HolderListRequest{}
	x.NoError(proto.Unmarshal(d.GetRequest(), asked))
	x.EqualValues(10, asked.GetSize(), "what it asked for")

	t.Run("and one that showed nobody's data leaves nothing", func(t *testing.T) {
		x := require.New(t)

		// A robot is nobody, and its alias is declared so.
		b.robot(t, ctx, b.Tenant, "arm-01")
		_, err := app.NewRobotServiceClient(conn).List(as, app.RobotListRequest_builder{}.Build())
		x.NoError(err)

		// And a person, asked for none of what makes them one.
		_, err = app.NewHolderServiceClient(conn).Get(as, app.HolderGetRequest_builder{
			Ref:    app.HolderRef_builder{Id: alice.Bytes()}.Build(),
			Select: app.HolderSelect_builder{DateCreated: z.Ptr(true)}.Build(),
		}.Build())
		x.NoError(err)

		x.Len(b.accessed(t, ctx), 1)
	})

	t.Run("until somebody is its keeper", func(t *testing.T) {
		x := require.New(t)

		v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
			Alias:  "arm-02",
			Keeper: "carol",
		}.Build())
		x.NoError(err)

		_, err = app.NewRobotServiceClient(conn).List(as, app.RobotListRequest_builder{}.Build())
		x.NoError(err)

		rs := b.accessed(t, ctx)
		x.Len(rs, 2)
		x.Equal(app.RobotService_List_FullMethodName, rs[1].Action)
		x.Equal([][]byte{v.GetId()}, rs[1].Subjects)
	})
}

// TestAWriteIsRecordedInItsOwnTransaction is what holds the write holding its
// record: the row is written by the commit, as the trail's are, and a
// transaction rolled back takes it along.
func TestAWriteIsRecordedInItsOwnTransaction(t *testing.T) {
	t.Run("over the wire, at once", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		alice := b.person(t, ctx, "alice")

		conn := pdtest.Serve(t, b.grpc(t))
		_, err := app.NewHolderServiceClient(conn).Patch(b.travels(ctx), app.HolderPatchRequest_builder{
			Ref:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
			Name: z.Ptr("Alice"),

			DateUpdatedForce: z.Ptr(true),
		}.Build())
		x.NoError(err)

		// Not queued, and one row: its answer carried her too, and that is
		// said already.
		x.Zero(b.Access.Pending())
		rs := b.accessed(t, ctx)
		x.Len(rs, 1)
		x.Equal("write", rs[0].Kind)
		x.Equal(app.HolderService_Patch_FullMethodName, rs[0].Action)
		x.Equal("ok", rs[0].Result)
		x.Equal(subjects(alice), rs[0].Subjects)
		x.Empty(detail(t, rs[0]).GetRequest(), "a write's is on the trail")
	})

	written := func(t *testing.T, b *built, ctx context.Context) (context.Context, *access.Call, dialect.Tx, pdid.Id) {
		t.Helper()
		x := require.New(t)
		alice := b.person(t, ctx, "alice")

		ctx, call := access.Into(b.as(ctx), "/test.Writes/Rename")
		drv, tx, err := dialect.BeginTx(ctx, b.Drv)
		x.NoError(err)
		t.Cleanup(func() { tx.Rollback() })
		s, err := enttx.Rebind(b.Walled, drv)
		x.NoError(err)

		for _, name := range []string{"Alice", "Alice B."} {
			_, err = s.Holder().Patch(ctx, app.HolderPatchRequest_builder{
				Ref:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
				Name: z.Ptr(name),

				DateUpdatedForce: z.Ptr(true),
			}.Build())
			x.NoError(err)
		}

		n, err := b.Ent.Access.Query().Count(ctx)
		x.NoError(err)
		x.Zero(n, "the record was written before the transaction was done")

		return ctx, call, tx, alice
	}

	t.Run("by the commit, once for the call", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		_, call, tx, alice := written(t, b, ctx)

		x.NoError(tx.Commit())
		rs := b.accessed(t, ctx)
		x.Len(rs, 1, "two writes of one call in one transaction")
		x.Equal("/test.Writes/Rename", rs[0].Action)
		x.Equal(subjects(alice), rs[0].Subjects)

		wrote, others := call.Unsaid()
		x.Empty(wrote)
		x.Empty(others, "what the writes wrote is not also what the call showed")
	})

	t.Run("and none of it when the transaction is rolled back", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		_, call, tx, alice := written(t, b, ctx)

		x.NoError(tx.Rollback())
		x.Empty(b.accessed(t, ctx))

		// Still the call's, for the interceptor to record as the refusal it
		// ended in.
		wrote, _ := call.Unsaid()
		x.Equal([]access.Subject{{Id: alice, Tenant: b.Tenant}}, wrote)
	})

	// A write's answer, or a history beside it, shows people whose data it did
	// not write: they are a read, beside the write's row.
	t.Run("and somebody it only showed is a read beside it", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		alice := b.person(t, ctx, "alice")
		bob := b.person(t, ctx, "bob")

		c := access.Config{Subjects: pd.Subjects, Refs: pd.SubjectRefs, Writer: b.Access}
		_, err := c.Unary()(b.as(ctx), nil, &grpc.UnaryServerInfo{FullMethod: "/test.Writes/Rename"},
			func(ctx context.Context, _ any) (any, error) {
				// In no transaction of anybody's: the write's row is the
				// interceptor's, once the call has answered.
				_, err := b.Walled.Holder().Patch(ctx, app.HolderPatchRequest_builder{
					Ref:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
					Name: z.Ptr("Alice"),

					DateUpdatedForce: z.Ptr(true),
				}.Build())
				if err != nil {
					return nil, err
				}

				access.Saw(ctx, access.Subject{Id: bob})
				return nil, nil
			})
		x.NoError(err)

		rs := b.accessed(t, ctx)
		x.Len(rs, 2)
		kinds := map[string][][]byte{}
		for _, r := range rs {
			kinds[r.Kind] = r.Subjects
		}
		x.Equal(map[string][][]byte{"write": subjects(alice), "read": subjects(bob)}, kinds)
	})

	t.Run("and an erase is a write of what it erased", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		alice := b.person(t, ctx, "alice")

		conn := pdtest.Serve(t, b.grpc(t))
		_, err := app.NewHolderServiceClient(conn).Erase(b.travels(ctx), app.HolderRef_builder{Id: alice.Bytes()}.Build())
		x.NoError(err)

		rs := b.accessed(t, ctx)
		x.Len(rs, 1)
		x.Equal("write", rs[0].Kind)
		x.Equal(app.HolderService_Erase_FullMethodName, rs[0].Action)
		x.Equal(subjects(alice), rs[0].Subjects)
	})
}

// TestARefusedCallIsRecordedWithWhatItNamed: a call refused carried nobody's
// data, and is what a review most wants to see. Its row names the rows its
// request named by key, and says how it was refused.
func TestARefusedCallIsRecordedWithWhatItNamed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	alice := b.person(t, ctx, "alice")

	// Behind the wall: somebody of another tenant.
	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)
	theirs, err := b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
		Alias:  "dave",
	}.Build())
	x.NoError(err)

	conn := pdtest.Serve(t, b.grpc(t))
	as := b.travels(ctx)
	_, err = app.NewHolderServiceClient(conn).Get(as, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: theirs.GetId()}.Build(),
	}.Build())
	x.Equal(codes.NotFound, status.Code(err))

	// And by a policy that reads and does not write.
	b.Policy = console{}
	conn = pdtest.Serve(t, b.grpc(t))
	_, err = app.NewHolderServiceClient(conn).Patch(as, app.HolderPatchRequest_builder{
		Ref:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
		Name: z.Ptr("Alice"),

		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.Equal(codes.PermissionDenied, status.Code(err))

	rs := b.accessed(t, ctx)
	x.Len(rs, 2)

	x.Equal(app.HolderService_Get_FullMethodName, rs[0].Action)
	x.Equal(codes.NotFound.String(), rs[0].Result)
	x.Equal([][]byte{theirs.GetId()}, rs[0].Subjects)
	// Whose it was, the call never read: it is filed under the caller's.
	x.Equal(b.Tenant.Uuid(), rs[0].TenantId)

	// A write refused before it wrote is a read of what it named.
	x.Equal(app.HolderService_Patch_FullMethodName, rs[1].Action)
	x.Equal("read", rs[1].Kind)
	x.Equal(codes.PermissionDenied.String(), rs[1].Result)
	x.Equal(subjects(alice), rs[1].Subjects)
}

// TestADownloadSaysWhatItIsFor is the rule a download is under (payday#111):
// it is refused without a reason, recorded before its answer leaves, and
// refused when it cannot be recorded.
func TestADownloadSaysWhatItIsFor(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	b.person(t, ctx, "alice")

	// This app has no download of its own; a list of everybody is one here.
	b.Downloads = []string{app.HolderService_List_FullMethodName}
	c := app.NewHolderServiceClient(pdtest.Serve(t, b.grpc(t)))
	as := b.travels(ctx)

	_, err := c.List(as, app.HolderListRequest_builder{}.Build())
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(err.Error(), access.ReasonKey)

	why := metadata.AppendToOutgoingContext(as, access.ReasonKey, "October's payroll")
	vs, err := c.List(why, app.HolderListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(vs.GetItems(), 2)

	// Written before it answered, and not queued.
	x.Zero(b.Access.Pending())
	n, err := b.Ent.Access.Query().Count(ctx)
	x.NoError(err)
	x.Equal(1, n)

	rs := b.accessed(t, ctx)
	x.Equal("download", rs[0].Kind)
	x.Equal("October's payroll", detail(t, rs[0]).GetReason())
	x.EqualValues(2, rs[0].SubjectCount)

	t.Run("and refused when it cannot be recorded", func(t *testing.T) {
		x := require.New(t)

		sink := b.Access.Sink
		t.Cleanup(func() { b.Access.Sink = sink })
		b.Access.Sink = func(context.Context, []access.Record) error {
			return errors.New("the database is not there")
		}

		vs, err := c.List(why, app.HolderListRequest_builder{}.Build())
		x.Equal(codes.Unavailable, status.Code(err))
		x.Nil(vs, "what could not be recorded left anyway")
	})
}

// TestAStreamIsRecordedAsItSends: a watch open for an hour is a record of what
// it showed in that hour, rather than of the moment it closed.
func TestAStreamIsRecordedAsItSends(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-01",
		Keeper: "carol",
	}.Build())
	x.NoError(err)

	_, events, cancel := b.watching(t, app.RobotWatchRequest_builder{
		Filters: []*app.RobotFilter{
			app.RobotFilter_builder{Ref: app.RobotRef_builder{Id: v.GetId()}.Build()}.Build(),
		},
	}.Build())
	first := next(t, events)
	x.NotEmpty(first.GetItems())

	// Still open: the row is the stream's second.
	x.Eventually(func() bool {
		if err := b.Access.Flush(ctx); err != nil {
			return false
		}
		n, err := b.Ent.Access.Query().Where(entaccess.ActionEQ(app.RobotService_Watch_FullMethodName)).Count(ctx)
		return err == nil && n == 1
	}, 5*time.Second, 50*time.Millisecond)

	cancel()

	rs := b.accessed(t, ctx)
	x.Len(rs, 1, "what was said while it was open is not said again as it closes")
	x.Equal("read", rs[0].Kind)
	x.Equal([][]byte{v.GetId()}, rs[0].Subjects)
}

// TestTheAccessRecordIsKeptAsAKindOfItsOwn: its rows leave the database, are
// archived, and are destroyed by the trail's own passes -- as the kind
// `access`, on a window of its own.
func TestTheAccessRecordIsKeptAsAKindOfItsOwn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	b.person(t, ctx, "alice")

	conn := pdtest.Serve(t, b.grpc(t))
	_, err := app.NewHolderServiceClient(conn).List(b.travels(ctx), app.HolderListRequest_builder{}.Build())
	x.NoError(err)
	x.Len(b.accessed(t, ctx), 1)

	trails, err := b.Ent.Audit.Query().Count(ctx)
	x.NoError(err)
	x.NotZero(trails)

	a := flob.NewMemStores()
	s := pd.TrailStore(b.Ent)
	n, err := trail.Archive(ctx, s, trail.Policy{Archive: a}, trail.Kinds{}, time.Now().Add(time.Hour))
	x.NoError(err)
	x.Equal(trails+1, n)

	cs, err := trail.Chunks(ctx, a)
	x.NoError(err)
	kinds := map[string]int{}
	for _, c := range cs {
		kinds[c.Kind] += c.Rows
	}
	x.Equal(1, kinds["access"], "the access record is not a kind of its own in the archive")

	// Read back as what each is: the trail's readers pass over the access
	// record's rows, and the other way round.
	seen := 0
	x.NoError(trail.Read(ctx, a, pd.AccessOf(func(v *app.Access) error {
		x.Equal("read", v.GetKind())
		x.EqualValues(2, v.GetSubjectCount())
		seen++

		return nil
	})))
	x.Equal(1, seen)

	audited := 0
	x.NoError(trail.Read(ctx, a, pd.TrailOf(func(v *app.Audit) error {
		audited++
		return nil
	})))
	x.Equal(trails, audited)

	t.Run("on a window of its own", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		b.person(t, ctx, "alice")

		conn := pdtest.Serve(t, b.grpc(t))
		_, err := app.NewHolderServiceClient(conn).List(b.travels(ctx), app.HolderListRequest_builder{}.Build())
		x.NoError(err)
		x.Len(b.accessed(t, ctx), 1)

		p, err := config.AuditConfig{
			Archive: t.TempDir(),
			Retain:  90 * 24 * time.Hour,
			By: map[string]config.AuditKeepConfig{
				"access": {Retain: time.Nanosecond},
			},
		}.Policy()
		x.NoError(err)

		trails, err := b.Ent.Audit.Query().Count(ctx)
		x.NoError(err)

		p.Pass(ctx, pd.TrailStore(b.Ent))

		x.Empty(b.accessed(t, ctx), "the access record outlived its window")
		n, err := b.Ent.Audit.Query().Where(entaudit.DomainNEQ(uint32(pd.AccessDomain))).Count(ctx)
		x.NoError(err)
		x.Equal(trails, n, "the trail left on the access record's window")

		cs, err := trail.Chunks(ctx, p.Archive)
		x.NoError(err)
		held := 0
		for _, c := range cs {
			if c.Kind == "access" {
				held += c.Rows
			}
		}
		x.Equal(1, held, "what left the database is not in the archive")
	})

	t.Run("and a tenant that leaves takes its own with it", func(t *testing.T) {
		x := require.New(t)

		leaving := must(pdid.From(must(b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "leaving"}.Build())).GetId()))
		row := func(tenant, actor pdid.Id) uuid.UUID {
			r := access.Record{
				Id:          pdid.New(pd.AccessDomain),
				Tenant:      tenant,
				ActorTenant: actor,
				Action:      "/test.Reads/Somebody",
				Kind:        access.Read,
				Result:      "ok",
				At:          time.Now().UTC(),
				Subjects:    []pdid.Id{pdid.New(pd.HolderDomain)},
				Count:       1,
				Detail:      pdpb.AccessDetail_builder{Address: "192.0.2.1"}.Build(),
			}
			require.NoError(t, pd.AccessSink(b.Ent)(ctx, []access.Record{r}))

			return r.Id.Uuid()
		}
		alone := row(leaving, leaving)
		sharing := row(leaving, b.Tenant)
		theirs := row(b.Tenant, leaving)

		_, err := trail.Policy{}.PurgeTenant(ctx, s, leaving)
		x.NoError(err)

		_, err = b.Ent.Access.Get(ctx, alone)
		x.True(ent.IsNotFound(err), "its own record outlived it")
		x.Empty(must(b.Ent.Access.Get(ctx, sharing)).Value, "what another tenant did in it still says where from")
		x.NotEmpty(must(b.Ent.Access.Get(ctx, theirs)).Value, "the other side's record was touched")
	})
}
