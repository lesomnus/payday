package cmd_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
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
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/lesomnus/payday/access"
	"github.com/lesomnus/payday/auth"
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

		rename := app.HolderPatchRequest_builder{
			Ref:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
			Name: z.Ptr("Alice"),

			DateUpdatedForce: z.Ptr(true),
		}.Build()
		c := access.Config{Subjects: pd.Subjects, Refs: pd.SubjectRefs, Writer: b.Access}
		_, err := c.Unary()(b.as(ctx), rename, &grpc.UnaryServerInfo{FullMethod: "/test.Writes/Rename"},
			func(ctx context.Context, _ any) (any, error) {
				// In no transaction of anybody's: the write's row is written
				// in the one the write makes for itself.
				if _, err := b.Walled.Holder().Patch(ctx, rename); err != nil {
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
			x.Empty(detail(t, r).GetRequest(), "what a call that wrote asked for is the write, and on the trail")
		}
		x.Equal(map[string][][]byte{"write": subjects(alice), "read": subjects(bob)}, kinds)
	})

	// Where the transaction keeps nothing for a commit, the row is written as
	// the write is made, in it: a write whose row cannot be written is not
	// made either, rather than made and recorded nowhere.
	t.Run("and one whose row cannot be written is not made", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		alice := b.person(t, ctx, "alice")

		_, err := b.Db.ExecContext(ctx, "ALTER TABLE access RENAME TO access_gone")
		x.NoError(err)

		conn := pdtest.Serve(t, b.grpc(t))
		_, err = app.NewHolderServiceClient(conn).Patch(b.travels(ctx), app.HolderPatchRequest_builder{
			Ref:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
			Name: z.Ptr("Alice"),

			DateUpdatedForce: z.Ptr(true),
		}.Build())
		x.Error(err)

		v, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build(),
		}.Build())
		x.NoError(err)
		x.NotEqual("Alice", v.GetName(), "the write was made, and its record was not")
	})

	// A commit can still refuse after the rows ahead of it are written -- a
	// check deferred to it -- and takes them along. The call that ends in that
	// refusal is then the one to say what its writes wrote.
	t.Run("and one refused at its commit is the refusal it ended in", func(t *testing.T) {
		x := require.New(t)
		b, ctx := build(t)
		alice := b.person(t, ctx, "alice")

		c := access.Config{Subjects: pd.Subjects, Refs: pd.SubjectRefs, Writer: b.Access}
		_, err := c.Unary()(b.as(ctx), nil, &grpc.UnaryServerInfo{FullMethod: "/test.Writes/Rename"},
			func(ctx context.Context, _ any) (any, error) {
				drv, tx, err := dialect.BeginTx(ctx, b.Drv)
				if err != nil {
					return nil, err
				}
				s, err := enttx.Rebind(b.Walled, drv)
				if err != nil {
					tx.Rollback()
					return nil, err
				}
				if _, err := s.Holder().Patch(ctx, app.HolderPatchRequest_builder{
					Ref:  app.HolderRef_builder{Id: alice.Bytes()}.Build(),
					Name: z.Ptr("Alice"),

					DateUpdatedForce: z.Ptr(true),
				}.Build()); err != nil {
					tx.Rollback()
					return nil, err
				}

				k, _ := dialect.KeepOf(drv)
				k.BeforeCommit(func(context.Context) error {
					return status.Error(codes.Aborted, "refused at the commit")
				})

				return nil, tx.Commit()
			})
		x.Equal(codes.Aborted, status.Code(err))

		rs := b.accessed(t, ctx)
		x.Len(rs, 1, "the row the refused commit took along is not written again as its refusal")
		x.Equal("write", rs[0].Kind)
		x.Equal(codes.Aborted.String(), rs[0].Result)
		x.Equal(subjects(alice), rs[0].Subjects)
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

	// And names them, and keeps nothing of what it tried: the name a refused
	// write tried to write is somebody's, and no row of it is the place for
	// that.
	for _, r := range rs {
		x.Empty(detail(t, r).GetRequest(), "what a refused call asked for was kept")
	}
}

// TestARowKeepsWhatAReadAskedForAndNoMore is what a row holds of a request: a
// read's, as its message declares it, and none of one larger than a row
// keeps.
func TestARowKeepsWhatAReadAskedForAndNoMore(t *testing.T) {
	b, ctx := build(t)
	alice := b.person(t, ctx, "alice")
	shown, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
		Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build(),
	}.Build())
	require.NoError(t, err)

	c := access.Config{Subjects: pd.Subjects, Refs: pd.SubjectRefs, Writer: b.Access}
	read := func(t *testing.T, req proto.Message) *pdpb.AccessDetail {
		t.Helper()

		_, err := c.Unary()(b.as(ctx), req, &grpc.UnaryServerInfo{FullMethod: app.HolderService_List_FullMethodName},
			func(context.Context, any) (any, error) {
				return app.HolderListResponse_builder{Items: []*app.Holder{shown}}.Build(), nil
			})
		require.NoError(t, err)

		rs := b.accessed(t, ctx)
		require.NotEmpty(t, rs)
		return detail(t, rs[len(rs)-1])
	}

	t.Run("as its message declares it", func(t *testing.T) {
		x := require.New(t)

		req := app.HolderListRequest_builder{Size: 10}.Build()
		req.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, 99, protowire.BytesType), []byte("what this build does not know")))

		asked := &app.HolderListRequest{}
		x.NoError(proto.Unmarshal(read(t, req).GetRequest(), asked))
		x.EqualValues(10, asked.GetSize())
		x.Empty(asked.ProtoReflect().GetUnknown(), "what the request's message does not declare was kept")
	})

	t.Run("and none of one larger than a row keeps", func(t *testing.T) {
		x := require.New(t)

		req := app.HolderListRequest_builder{After: strings.Repeat("x", access.MaxRequest)}.Build()
		x.Empty(read(t, req).GetRequest())
	})
}

// TestAnAnyIsReadOnlyWherePaydayPackedOne is what the access record reads a
// message for: a batch's operations and answers, unpacked once, and no `Any`
// of the app's own. A message packed in one packed in another is work that
// grows with the square of its size, for whoever sent it, and a tenant named
// inside an app's `Any` is whatever its writer wrote.
func TestAnAnyIsReadOnlyWherePaydayPackedOne(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	alice := b.person(t, ctx, "alice")

	get := app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: alice.Bytes()}.Build()}.Build()
	shown, err := b.Ungated.Holder().Get(ctx, get)
	x.NoError(err)

	batched := func(v *anypb.Any) *pdpb.BatchRequest {
		return pdpb.BatchRequest_builder{
			Ops: []*pdpb.Op{pdpb.Op_builder{Method: app.HolderService_Get_FullMethodName, Request: v}.Build()},
		}.Build()
	}
	packed := must(anypb.New(get))
	x.Equal([]access.Subject{{Id: alice}}, pd.SubjectRefs(batched(packed)), "an operation's request is read unpacked")
	x.Empty(pd.SubjectRefs(batched(must(anypb.New(packed)))), "an Any packed in it is unpacked")
	x.Empty(pd.SubjectRefs(batched(must(anypb.New(batched(packed))))), "a batch packed in it is unpacked")

	answered := func(v *anypb.Any) *pdpb.BatchResponse {
		return pdpb.BatchResponse_builder{Results: []*anypb.Any{v}}.Build()
	}
	held := must(anypb.New(shown))
	x.Equal([]access.Subject{{Id: alice, Tenant: b.Tenant}}, pd.Subjects(answered(held)), "a batch's answer is read unpacked")
	x.Empty(pd.Subjects(answered(must(anypb.New(held)))), "an Any packed in it is unpacked")
	x.Empty(pd.Subjects(answered(must(anypb.New(answered(held))))), "an answer packed in it is unpacked")
	x.Empty(pd.Subjects(held), "an Any of the app's own is read")
}

// TestADownloadIsNotAnOperationOfABatch: a download says what it is for, and
// its row is written before its answer leaves -- neither of which an
// operation of a batch can do -- so a batch refuses one rather than let it out
// as the batch's read.
func TestADownloadIsNotAnOperationOfABatch(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	b.person(t, ctx, "alice")

	b.Downloads = []string{app.HolderService_List_FullMethodName}
	conn := pdtest.Serve(t, b.grpc(t))
	why := metadata.AppendToOutgoingContext(b.travels(ctx), access.ReasonKey, "October's payroll")
	req := pdpb.BatchRequest_builder{
		Ops: []*pdpb.Op{op(t, app.HolderService_List_FullMethodName, app.HolderListRequest_builder{}.Build())},
	}.Build()

	_, err := pdpb.NewBatchServiceClient(conn).Do(why, req)
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(err.Error(), "by itself")

	_, err = pdpb.NewResourceServiceClient(conn).Batch(why, req)
	x.Equal(codes.InvalidArgument, status.Code(err))
	x.Contains(err.Error(), "by itself")

	x.Empty(b.accessed(t, ctx), "a refused batch carried somebody")
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

	// A header is ASCII, and a browser sends no other: a reason is a `-bin`
	// key, base64 on the wire and the text again by the time it arrives.
	t.Run("in any words, from a page", func(t *testing.T) {
		x := require.New(t)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			b.serving(t).URL+app.HolderService_List_FullMethodName, strings.NewReader(`{}`))
		x.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")
		req.Header.Set(auth.Header, auth.PlainScheme+" @acme/admin")
		req.Header.Set(access.ReasonKey, base64.RawStdEncoding.EncodeToString([]byte("10월 급여 정산")))

		res, err := http.DefaultClient.Do(req)
		x.NoError(err)
		res.Body.Close()
		x.Equal(http.StatusOK, res.StatusCode)

		rs := b.accessed(t, ctx)
		x.Equal("10월 급여 정산", detail(t, rs[len(rs)-1]).GetReason())
	})

	t.Run("and refused when what it says is not a sentence of text", func(t *testing.T) {
		x := require.New(t)

		for _, v := range []string{"\xff\xfe", strings.Repeat("가", access.MaxReason/3+1)} {
			_, err := c.List(metadata.AppendToOutgoingContext(as, access.ReasonKey, v), app.HolderListRequest_builder{}.Build())
			x.Equal(codes.InvalidArgument, status.Code(err))
		}
	})

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
