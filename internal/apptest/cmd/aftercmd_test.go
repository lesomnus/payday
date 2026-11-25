package cmd_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/pdcmd"

	app "github.com/lesomnus/payday/internal/apptest"
)

// What an app says after one of the tree's commands has made its call.
//
// roster's `role add` is the tree's own and it wants one more line once a role
// is written; replacing the command to say it would be writing the command
// again. [pdcmd.Tree.After] is the line and nothing else: the call, its answer
// printed, and then the app's function with what was sent and answered.

// told is every call `f` was handed, and what the command had printed when it
// was handed it -- which is how the order is asserted.
type told struct {
	calls   []pdcmd.Call
	printed []string
}

func (c *told) f(root *xli.Command, also func(context.Context, pdcmd.Call)) func(context.Context, pdcmd.Call) {
	return func(ctx context.Context, call pdcmd.Call) {
		c.calls = append(c.calls, call)
		c.printed = append(c.printed, root.Writer.(*bytes.Buffer).String())
		if also != nil {
			also(ctx, call)
		}
	}
}

func TestAnAppIsToldAfterAGeneratedCall(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	tree, err := pdcmd.New(pdcmd.Static(conn))
	x.NoError(err)
	root := &xli.Command{Name: "app"}

	// The connection answers a second call, as the caller the first was made
	// as: the credential is on the context `f` is handed, as it was on the one
	// the command ran in.
	var again *app.Holder
	var bare error
	c := &told{}
	x.NoError(tree.After("holder/add", c.f(root, func(ctx context.Context, call pdcmd.Call) {
		ref := app.HolderRef_builder{Id: call.Answered.(*app.Holder).GetId()}.Build()
		req := app.HolderGetRequest_builder{Ref: ref}.Build()

		again, err = app.NewHolderServiceClient(call.Conn).Get(ctx, req)
		x.NoError(err)
		_, bare = app.NewHolderServiceClient(call.Conn).Get(context.Background(), req)
	})))
	x.NoError(tree.Mount(root))

	got := xlitest.Harness{Cmd: root, Ctx: as}.Run(t, "holder", "add", "@acme/erin", `{"name":"Erin"}`)
	x.NoError(got.Err)

	x.Len(c.calls, 1)
	call := c.calls[0]
	x.Equal(got.Stdout, c.printed[0], "f was called before the answer was printed")
	x.Contains(got.Stdout, "erin")

	x.Equal("/app.HolderService/Add", call.Method)
	x.Equal("erin", call.Sent.(*app.HolderAddRequest).GetAlias())
	x.Equal("Erin", call.Sent.(*app.HolderAddRequest).GetName(), "what was sent is the request with the JSON merged in")
	x.Equal("erin", call.Answered.(*app.Holder).GetAlias())

	x.NotNil(again)
	x.Equal(call.Answered.(*app.Holder).GetId(), again.GetId())
	x.Equal(codes.Unauthenticated, status.Code(bare), "the second call was answered for a reason other than its caller")
}

func TestAnAppIsToldAfterAUnaryCall(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := b.dialed(t, ctx)

	tree, err := pdcmd.New(pdcmd.Static(conn))
	x.NoError(err)
	root := &xli.Command{Name: "app"}

	u, err := tree.Unary("app.HolderService.Get")
	x.NoError(err)
	x.NoError(tree.Add("holder/fetch", u))

	var again *app.Holder
	c := &told{}
	x.NoError(tree.After("holder/fetch", c.f(root, func(ctx context.Context, call pdcmd.Call) {
		again, err = app.NewHolderServiceClient(call.Conn).Get(ctx, call.Sent.(*app.HolderGetRequest))
		x.NoError(err)
	})))
	x.NoError(tree.Mount(root))

	got := xlitest.Harness{Cmd: root, Ctx: b.travels(ctx)}.Run(t, "holder", "fetch", "@acme/admin")
	x.NoError(got.Err)

	x.Len(c.calls, 1)
	call := c.calls[0]
	x.Equal(got.Stdout, c.printed[0])
	x.Contains(got.Stdout, "admin")

	x.Equal("/app.HolderService/Get", call.Method)
	x.Equal("admin", call.Sent.(*app.HolderGetRequest).GetRef().GetSlug().GetAlias())
	x.Equal(b.Holder.Bytes(), call.Answered.(*app.Holder).GetId())
	x.Equal(b.Holder.Bytes(), again.GetId())
}

func TestNothingIsToldOfACallThatWasNotMade(t *testing.T) {
	b, ctx := build(t)
	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	run := func(t *testing.T, ctx context.Context, args ...string) (*told, xlitest.Result) {
		tree, err := pdcmd.New(pdcmd.Static(conn))
		require.NoError(t, err)

		root := &xli.Command{Name: "app"}
		c := &told{}
		require.NoError(t, tree.After("holder/add", c.f(root, nil)))
		require.NoError(t, tree.Mount(root))

		return c, xlitest.Harness{Cmd: root, Ctx: ctx}.Run(t, args...)
	}

	t.Run("the server refused it", func(t *testing.T) {
		x := require.New(t)

		c, got := run(t, ctx, "holder", "add", "@acme/frank")
		x.ErrorContains(got.Err, "Unauthenticated")
		x.Empty(c.calls)
	})

	t.Run("-o is not a format", func(t *testing.T) {
		x := require.New(t)

		c, got := run(t, as, "holder", "add", "-o", "nope", "@acme/grace")
		x.ErrorContains(got.Err, "not a format")
		x.Empty(c.calls)

		// And nothing was sent: a write made and then reported as failed is a
		// write somebody makes twice.
		_, err := b.Ungated.Holder().Get(ctx, app.HolderGetRequest_builder{
			Ref: app.HolderRef_builder{Slug: app.HolderRefBySlug_builder{
				Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
				Alias:  proto.String("grace"),
			}.Build()}.Build(),
		}.Build())
		x.Equal(codes.NotFound, status.Code(err), "the add was made before -o was read")
	})

	t.Run("a flag that is none", func(t *testing.T) {
		x := require.New(t)

		c, got := run(t, as, "holder", "add", "--nope", "@acme/heidi")
		x.Error(got.Err)
		x.Empty(c.calls)
	})

	t.Run("help", func(t *testing.T) {
		x := require.New(t)

		c, got := run(t, as, "holder", "add", "--help")
		x.NoError(got.Err)
		x.Empty(c.calls)
	})
}

func TestAfterIsRefusedWhereThereIsNoOneCall(t *testing.T) {
	x := require.New(t)
	f := func(context.Context, pdcmd.Call) {}

	tree := shaped(t)
	x.ErrorContains(tree.After("holder/addd", f), "pdcmd: holder/addd: nothing to run after")
	x.ErrorContains(tree.After("cell/list", f), "pdcmd: cell/list: nothing to run after")
	x.ErrorContains(tree.After("robot/watch", f), "pdcmd: robot/watch: is a stream")
	x.ErrorContains(tree.After("robot", f), "pdcmd: robot: is a group")

	x.NoError(tree.Add("holder/count", &xli.Command{}))
	x.ErrorContains(tree.After("holder/count", f), "pdcmd: holder/count: is the app's own command")
	x.NoError(tree.Replace("holder/erase", &xli.Command{}))
	x.ErrorContains(tree.After("holder/erase", f), "pdcmd: holder/erase: is the app's own command")

	x.ErrorContains(tree.After("holder/get", nil), "pdcmd: holder/get: nothing to call")

	x.NoError(tree.After("holder/get", f))
	x.ErrorContains(tree.After("holder/get", f), "pdcmd: holder/get: has a function to call after it already")
	x.ErrorContains(tree.Replace("holder/get", &xli.Command{}), "pdcmd: holder/get: has a function to call after it, which the replacement would never call")

	x.NoError(tree.Mount(&xli.Command{Name: "app"}))
	x.ErrorContains(tree.After("holder/patch", f), "pdcmd: holder/patch: the tree is mounted on app")
}

func TestAfterReadsAPathAsTheCommandLineDoes(t *testing.T) {
	b, ctx := build(t)
	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	t.Run("said by an alias", func(t *testing.T) {
		x := require.New(t)

		tree, err := pdcmd.New(pdcmd.Static(conn))
		x.NoError(err)
		root := &xli.Command{Name: "app"}

		c := &told{}
		x.NoError(tree.After("holder/ls", c.f(root, nil)))
		x.ErrorContains(tree.After("holder/list", c.f(root, nil)), "already", "ls and list are one command")
		x.NoError(tree.Mount(root))

		got := xlitest.Harness{Cmd: root, Ctx: as}.Run(t, "holder", "list")
		x.NoError(got.Err)
		x.Len(c.calls, 1)
		x.Equal("/app.HolderService/List", c.calls[0].Method)
	})

	t.Run("kept by the words given after it", func(t *testing.T) {
		x := require.New(t)

		tree, err := pdcmd.New(pdcmd.Static(conn))
		x.NoError(err)
		root := &xli.Command{Name: "app"}

		c := &told{}
		x.NoError(tree.After("holder/get", c.f(root, nil)))
		x.NoError(tree.Alias("holder", "who"))
		x.NoError(tree.Alias("holder/get", "show"))
		x.NoError(tree.Hide("holder"))
		x.NoError(tree.Mount(root))

		got := xlitest.Harness{Cmd: root, Ctx: as}.Run(t, "who", "show", "@acme/admin")
		x.NoError(got.Err)
		x.Len(c.calls, 1)
		x.Equal("/app.HolderService/Get", c.calls[0].Method)
	})
}
