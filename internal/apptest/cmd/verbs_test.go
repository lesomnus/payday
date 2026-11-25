package cmd_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/lesomnus/protobuf-patch/patch"
	"github.com/lesomnus/protobuf-patch/patchpb"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/lesomnus/payday/admission"
	app "github.com/lesomnus/payday/internal/apptest"
	entaudit "github.com/lesomnus/payday/internal/apptest/internal/ent/audit"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pderr"
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

// TestAVerbAnOverlaySwitchedIsWhatEveryoneSees: this app's overlays switch
// payday's Tenant's Erase off and its Holder's Apply on, and the merged entity
// is what every generator reads. So there is no `TenantService/Erase` in the
// contract, on the server's interface, in the client or in the command tree,
// and a caller over the wire is told there is no such method; and there is a
// `HolderService/Apply` everywhere, through the same layers payday's own verbs
// go through.
func TestAVerbAnOverlaySwitchedIsWhatEveryoneSees(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	d, err := protoregistry.GlobalFiles.FindDescriptorByName("app.TenantService")
	x.NoError(err)
	x.Nil(d.(protoreflect.ServiceDescriptor).Methods().ByName("Erase"), "in the contract")
	d, err = protoregistry.GlobalFiles.FindDescriptorByName("app.HolderService")
	x.NoError(err)
	x.NotNil(d.(protoreflect.ServiceDescriptor).Methods().ByName("Apply"), "in the contract")

	_, ok := reflect.TypeFor[app.TenantServiceServer]().MethodByName("Erase")
	x.False(ok, "on the server")
	_, ok = reflect.TypeFor[app.TenantServiceClient]().MethodByName("Erase")
	x.False(ok, "in the client")
	_, ok = reflect.TypeFor[app.HolderServiceServer]().MethodByName("Apply")
	x.True(ok, "on the server")
	_, ok = reflect.TypeFor[app.HolderServiceClient]().MethodByName("Apply")
	x.True(ok, "in the client")

	conn := b.dialed(t, ctx)
	tree, err := pdcmd.New(pdcmd.Static(conn))
	x.NoError(err)
	x.NotNil(tree.Command("tenant/patch"))
	x.Nil(tree.Command("tenant/erase"), "in the command tree")

	as := b.travels(ctx)
	err = conn.Invoke(as, "/app.TenantService/Erase", &emptypb.Empty{}, &emptypb.Empty{})
	x.Equal(codes.Unimplemented, status.Code(err), "over the wire")

	v, err := app.NewHolderServiceClient(conn).Apply(as, app.HolderApplyRequest_builder{
		Ref:   app.HolderRef_builder{Id: b.Holder.Bytes()}.Build(),
		Patch: patch.MustNewUntyped(patch.Target(patch.Name("name")).Assign(patch.Str("Ada"))),
	}.Build())
	x.NoError(err)
	x.Equal("Ada", v.GetName())
	x.True(recordedAs(t, b, b.Holder.Bytes(), app.HolderService_Apply_FullMethodName), "on the trail by its method")
}

// recordedAs is whether the trail has a line of `method` about the row `id`.
func recordedAs(t *testing.T, b *built, id []byte, method string) bool {
	t.Helper()
	n, err := b.Ent.Audit.Query().Where(entaudit.ObjectIdEQ(mustId(require.New(t), id).Uuid()), entaudit.ActionEQ(method)).Count(t.Context())
	require.NoError(t, err)
	return n >= 1
}

// TestAnAliasADocumentWritesIsKeptAsAPatchKeepsIt: the Sink folds the alias
// a Patch is given -- "  Floor-2 " is kept as "floor-2" -- so that aliases
// compare with "=" in Go and in the database. An Apply writes the same column
// from a document, and it was let through as it was: it was hidden while
// every Apply came with a Patch, and an entity may have an Apply alone now
// (Cell). So the same alias is kept whichever wrote it: a document that writes
// an alias not already folded, or not an alias at all, is refused, naming the
// field and the form it must have, and one written as it is kept goes
// through. Of Cell, which has an Apply and no Patch, and of Robot, which has
// both, in process and over the wire.
func TestAnAliasADocumentWritesIsKeptAsAPatchKeepsIt(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := b.dialed(t, ctx)

	doc := func(alias string) *patchpb.Patch {
		return patch.MustNewUntyped(patch.Target(patch.Name("alias")).Assign(patch.Str(alias)))
	}
	type apply func(ctx context.Context, id []byte, d *patchpb.Patch) (string, error)
	cases := map[string]struct {
		as   func(context.Context) context.Context
		add  func(alias string) []byte
		send apply
	}{}
	cell := func(alias string) []byte {
		v, err := b.Ungated.Cell().Add(ctx, app.CellAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: alias}.Build())
		x.NoError(err)
		return v.GetId()
	}
	robot := func(alias string) []byte {
		v, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(), Alias: alias}.Build())
		x.NoError(err)
		return v.GetId()
	}
	cases["a cell, in process"] = struct {
		as   func(context.Context) context.Context
		add  func(alias string) []byte
		send apply
	}{b.as, cell, func(ctx context.Context, id []byte, d *patchpb.Patch) (string, error) {
		v, err := b.Walled.Cell().Apply(ctx, app.CellApplyRequest_builder{Ref: app.CellRef_builder{Id: id}.Build(), Patch: d}.Build())
		return v.GetAlias(), err
	}}
	cases["a cell, over the wire"] = struct {
		as   func(context.Context) context.Context
		add  func(alias string) []byte
		send apply
	}{b.travels, cell, func(ctx context.Context, id []byte, d *patchpb.Patch) (string, error) {
		v, err := app.NewCellServiceClient(conn).Apply(ctx, app.CellApplyRequest_builder{Ref: app.CellRef_builder{Id: id}.Build(), Patch: d}.Build())
		return v.GetAlias(), err
	}}
	cases["a robot, in process"] = struct {
		as   func(context.Context) context.Context
		add  func(alias string) []byte
		send apply
	}{b.as, robot, func(ctx context.Context, id []byte, d *patchpb.Patch) (string, error) {
		v, err := b.Walled.Robot().Apply(ctx, app.RobotApplyRequest_builder{Ref: app.RobotRef_builder{Id: id}.Build(), Patch: d}.Build())
		return v.GetAlias(), err
	}}
	cases["a robot, over the wire"] = struct {
		as   func(context.Context) context.Context
		add  func(alias string) []byte
		send apply
	}{b.travels, robot, func(ctx context.Context, id []byte, d *patchpb.Patch) (string, error) {
		v, err := app.NewRobotServiceClient(conn).Apply(ctx, app.RobotApplyRequest_builder{Ref: app.RobotRef_builder{Id: id}.Build(), Patch: d}.Build())
		return v.GetAlias(), err
	}}

	n := 0
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)
			n++
			id := c.add(fmt.Sprintf("before-%d", n))

			for _, v := range []string{fmt.Sprintf("  Floor-%d ", n), fmt.Sprintf("FLOOR-%d", n), "", "no spaces inside"} {
				_, err := c.send(c.as(ctx), id, doc(v))
				x.Equal(codes.InvalidArgument, status.Code(err), "%q was let through", v)
				vs := pderr.Violations(err)
				x.Len(vs, 1, "%q", v)
				x.Equal("alias", vs[0].Field)
			}
			_, err := c.send(c.as(ctx), id, doc(fmt.Sprintf("Floor-%d", n)))
			x.ErrorContains(err, fmt.Sprintf(`"floor-%d"`, n), "the form it must have")

			got, err := c.send(c.as(ctx), id, doc(fmt.Sprintf("floor-%d", n)))
			x.NoError(err)
			x.Equal(fmt.Sprintf("floor-%d", n), got)
		})
	}

	// And a Patch of the robot keeps "  Floor-9 " as "floor-9", which is what
	// the document is held to.
	id := robot("patched")
	v, err := b.Walled.Robot().Patch(b.as(ctx), app.RobotPatchRequest_builder{
		Ref:              app.RobotRef_builder{Id: id}.Build(),
		Alias:            z.Ptr("  Floor-9 "),
		DateUpdatedForce: z.Ptr(true),
	}.Build())
	x.NoError(err)
	x.Equal("floor-9", v.GetAlias())
}
