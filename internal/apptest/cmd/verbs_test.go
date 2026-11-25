package cmd_test

import (
	"context"
	"testing"

	"github.com/lesomnus/protobuf-patch/patch"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/lesomnus/payday/admission"
	app "github.com/lesomnus/payday/internal/apptest"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
)

// TestAVerbIsThereWhenItIsAskedFor: `orm` names each verb on its own now, and
// no verb brings another (protobuf-orm/protobuf-orm#4). This app's Joint asks
// for a Patch and no Apply, and its Cell for an Apply and no Patch. Each has
// the one it asked for and not the other -- in the contract, and so in every
// client and on every port -- and the one it has goes through every layer the
// app is built of, as it would beside the other: the wall narrows it, the
// trail records it by the method that wrote, admission is asked of it, and a
// caller over the wire reaches it through the same chain.
func TestAVerbIsThereWhenItIsAskedFor(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	methods := func(svc protoreflect.FullName) protoreflect.MethodDescriptors {
		d, err := protoregistry.GlobalFiles.FindDescriptorByName(svc)
		x.NoError(err)
		return d.(protoreflect.ServiceDescriptor).Methods()
	}
	x.NotNil(methods("app.JointService").ByName("Patch"))
	x.Nil(methods("app.JointService").ByName("Apply"), "an Apply that nothing asked for")
	x.NotNil(methods("app.CellService").ByName("Apply"))
	x.Nil(methods("app.CellService").ByName("Patch"), "a Patch that nothing asked for")

	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "elsewhere"}.Build())
	x.NoError(err)

	cell := func(tenant []byte, alias string) []byte {
		v, err := b.Ungated.Cell().Add(ctx, app.CellAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tenant}.Build(),
			Alias:  alias,
		}.Build())
		x.NoError(err)
		return v.GetId()
	}
	joint := func(tenant []byte, alias string) []byte {
		r, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tenant}.Build(),
			Alias:  alias,
		}.Build())
		x.NoError(err)
		v, err := b.Ungated.Joint().Add(ctx, app.JointAddRequest_builder{
			Robot: app.RobotRef_builder{Id: r.GetId()}.Build(),
			Alias: alias,
		}.Build())
		x.NoError(err)
		return v.GetId()
	}
	applying := func(id []byte, alias string) *app.CellApplyRequest {
		return app.CellApplyRequest_builder{
			Ref:   app.CellRef_builder{Id: id}.Build(),
			Patch: patch.MustNewUntyped(patch.Target(patch.Name("alias")).Assign(patch.Str(alias))),
		}.Build()
	}
	patching := func(id []byte, alias string) *app.JointPatchRequest {
		return app.JointPatchRequest_builder{
			Ref:   app.JointRef_builder{Id: id}.Build(),
			Alias: &alias,
		}.Build()
	}
	recorded := func(id []byte, method string) bool {
		n, err := b.Ent.Audit.Query().Where(entaudit.ObjectIdEQ(mustId(x, id).Uuid()), entaudit.ActionEQ(method)).Count(ctx)
		x.NoError(err)
		return n == 1
	}

	t.Run("through the wall", func(t *testing.T) {
		x := require.New(t)

		mine := cell(b.Tenant.Bytes(), "near")
		v, err := b.Walled.Cell().Apply(b.as(ctx), applying(mine, "nearer"))
		x.NoError(err)
		x.Equal("nearer", v.GetAlias())
		x.True(recorded(mine, app.CellService_Apply_FullMethodName), "the trail names the write by its method")

		_, err = b.Walled.Cell().Apply(b.as(ctx), applying(cell(other.GetId(), "far"), "mine-now"))
		x.Equal(codes.NotFound, status.Code(err), "a document wrote a row in a tenant this caller cannot see")

		ours := joint(b.Tenant.Bytes(), "elbow")
		w, err := b.Walled.Joint().Patch(b.as(ctx), patching(ours, "knee"))
		x.NoError(err)
		x.Equal("knee", w.GetAlias())
		x.True(recorded(ours, app.JointService_Patch_FullMethodName))

		_, err = b.Walled.Joint().Patch(b.as(ctx), patching(joint(other.GetId(), "wrist"), "mine-now"))
		x.Equal(codes.NotFound, status.Code(err), "a patch wrote a row in a tenant this caller cannot see")
	})

	t.Run("over the wire", func(t *testing.T) {
		x := require.New(t)

		conn := b.dialed(t, ctx)
		as := b.travels(ctx)

		c := cell(b.Tenant.Bytes(), "wired")
		v, err := app.NewCellServiceClient(conn).Apply(as, applying(c, "wired-2"))
		x.NoError(err)
		x.Equal("wired-2", v.GetAlias())

		j := joint(b.Tenant.Bytes(), "wired")
		w, err := app.NewJointServiceClient(conn).Patch(as, patching(j, "wired-2"))
		x.NoError(err)
		x.Equal("wired-2", w.GetAlias())

		// And what neither asked for is no method of the server's.
		for _, m := range []string{"/app.CellService/Patch", "/app.JointService/Apply"} {
			err := conn.Invoke(as, m, &emptypb.Empty{}, &emptypb.Empty{})
			x.Equal(codes.Unimplemented, status.Code(err), m)
		}
	})

	t.Run("past admission", func(t *testing.T) {
		x := require.New(t)

		// Made first: admission is installed on the store the app shares,
		// and it refuses a write that is none of the generated servers'.
		c, j := cell(b.Tenant.Bytes(), "admitted"), joint(b.Tenant.Bytes(), "admitted")

		by := []string{}
		s := admitted(t, b, admission.Func(func(_ context.Context, call admission.Call) error {
			by = append(by, call.By)
			return nil
		}), admission.Options{Entities: []string{"app.Cell", "app.Joint"}})

		_, err := s.Cell().Apply(b.as(ctx), applying(c, "admitted-2"))
		x.NoError(err)
		_, err = s.Joint().Patch(b.as(ctx), patching(j, "admitted-2"))
		x.NoError(err)
		x.Contains(by, app.CellService_Apply_FullMethodName)
		x.Contains(by, app.JointService_Patch_FullMethodName)
	})
}
