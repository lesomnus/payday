package cmd_test

import (
	"testing"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdcmd"
)

// TestAReferenceSaysWhatKindOfThingItNames.
//
// `@acme/admin#holder` carries the same assertion a slug carries in a header
// or a certificate, and the command is where it gets checked -- the server
// cannot: it narrows to rows of its own entity, finds nothing, and answers a
// NotFound that is true and says nothing about the mistake. Before this, the
// "#" was not even parsed: `@acme#tenant` -- the slug package's own example --
// was the alias "acme#tenant", and the typo this exists to catch was a silent
// empty answer.
func TestAReferenceSaysWhatKindOfThingItNames(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	// The right word is the row, same as no word.
	got := xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.
		Run(t, "holder", "get", "@acme/admin#holder")
	x.NoError(got.Err)
	x.Contains(got.Stdout, "admin")

	// And the slug package's own example of naming a tenant.
	got = xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.
		Run(t, "tenant", "get", "@acme#tenant")
	x.NoError(got.Err)
	x.Contains(got.Stdout, "acme")

	// The wrong word is refused with both kinds in it, before anything is
	// asked of the server.
	got = xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.
		Run(t, "holder", "get", "@acme/admin#robot")
	x.Error(got.Err)
	x.Contains(got.Err.Error(), "robot")
	x.Contains(got.Err.Error(), "holder")

	// An identifier claims its kind in its ninth byte, and the claim is
	// checked the same way: the tenant's identifier on `holder get` used to be
	// a NotFound, which was true and useless.
	got = xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.
		Run(t, "holder", "get", b.Tenant.String())
	x.Error(got.Err)
	x.Contains(got.Err.Error(), "tenant")
	x.Contains(got.Err.Error(), "holder")

	// A word this app has no kind for is a typo, refused with the list of
	// words it does have.
	got = xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.
		Run(t, "holder", "get", "@acme/admin#hodler")
	x.Error(got.Err)
	x.Contains(got.Err.Error(), "holder")
	x.Contains(got.Err.Error(), "robot")
}

// TestAReferenceFoldsTheWayTheDatabaseFolded.
//
// "  Acme " and "acme" are the same alias on the way in -- [slug.ParseAlias]
// folds before the row is written -- so they have to be the same alias on the
// command line, or a name copied out of an email finds nothing that is there.
func TestAReferenceFoldsTheWayTheDatabaseFolded(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	got := xlitest.Harness{Cmd: rooted(t, b.dialed(t, ctx)), Ctx: b.travels(ctx)}.
		Run(t, "holder", "get", "@ACME/Admin")
	x.NoError(got.Err)
	x.Contains(got.Stdout, "admin")
}

// TestTheKindIsCheckedOnEveryVerbThatTakesOne.
//
// The check lives in one method, but each verb wires it separately -- `add`
// through `setNew`, `watch` through a filter -- so each is one forgotten line
// away from the old silence. `get` is pinned above; these are the others.
func TestTheKindIsCheckedOnEveryVerbThatTakesOne(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	for _, args := range [][]string{
		{"holder", "add", "@acme/bob#robot"},
		{"holder", "patch", "@acme/admin#robot", `{"name":"Ada"}`},
		{"holder", "erase", "@acme/admin#robot"},
		{"robot", "watch", "@acme/arm-01#holder"},
	} {
		got := xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.Run(t, args...)
		x.Error(got.Err, args)
		x.Contains(got.Err.Error(), "robot", args)
		x.Contains(got.Err.Error(), "holder", args)
	}
}

// TestTheKindReachesACommandTheAppWiredItself.
//
// [pdcmd.Tree.Unary] has no Entity to read a domain from, so it resolves one
// from the `ref` field's own type -- `app.RobotRef` is the generated name of a
// reference to `app.Robot`, and the registry answers for that. This is the
// test that says an RPC an app declared in an overlay refuses a wrong-kind
// reference the same way the five verbs do.
func TestTheKindReachesACommandTheAppWiredItself(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := b.dialed(t, ctx)

	tree, err := pdcmd.New(pdcmd.Static(conn))
	x.NoError(err)

	c, err := tree.Unary("app.RobotService.Move")
	x.NoError(err)
	x.NoError(tree.Add("robot/move", c))

	root := &xli.Command{Name: "app", Commands: tree.Commands()}
	got := xlitest.Harness{Cmd: root, Ctx: b.travels(ctx)}.Run(t,
		"robot", "move", "@acme/arm-07#holder", `{"to":{"alias":"floor-2"}}`)

	x.Error(got.Err)
	x.Contains(got.Err.Error(), "robot")
	x.Contains(got.Err.Error(), "holder")
}
