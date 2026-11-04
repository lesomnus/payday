package cmd_test

import (
	"strings"
	"testing"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/pdcmd"
)

// The words a command answers to: its name, which is the schema's, the aliases
// an app gives it, and whether the help shows it at all.
//
// Most of this needs no server. A tree is built from the descriptors and asks
// its connector for nothing until a command runs, so `--help`, completion and
// every refusal here are the tree on its own; the two that run a call dial the
// app's real server, for the reason the rest of `pdcmd_test.go` gives.

// shaped is a tree of this app with nothing to call, for what is asked of its
// shape rather than of a deployment.
func shaped(t *testing.T) *pdcmd.Tree {
	t.Helper()

	tree, err := pdcmd.New(pdcmd.Static(nil))
	require.NoError(t, err)

	return tree
}

// helped is `args` run against the tree mounted on a root called `app`, with
// the help a person gets: an AI agent's `--help` prints more, and these tests
// are run by one as often as by a person.
func helped(t *testing.T, tree *pdcmd.Tree, args ...string) xlitest.Result {
	t.Helper()
	t.Setenv("XLI_AGENT", "0")

	root := &xli.Command{Name: "app"}
	require.NoError(t, tree.Mount(root))

	return xlitest.Harness{Cmd: root}.Run(t, args...)
}

// TestLsStillRunsTheList, because it is what every app on payday has typed
// since these commands were built.
//
// The verb is `list`, the RPC's name, and `ls` is its alias: the same command,
// answering the same, so a script that pages through with `ls` and a person
// who types it are both where they were.
func TestLsStillRunsTheList(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	list := xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.Run(t, "holder", "list", "-o", "name")
	x.NoError(list.Err)
	x.Contains(list.Stdout, b.Holder.String())

	ls := xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.Run(t, "holder", "ls", "-o", "name")
	x.NoError(ls.Err)
	x.Equal(list.Stdout, ls.Stdout)

	// And the help says both, beside each other, where the verbs are listed.
	got := helped(t, shaped(t), "robot", "--help")
	x.NoError(got.Err)
	x.Regexp(`(?m)^\s+list,ls\s+list Robot$`, got.Stdout)

	// Its own help is under its name, whichever word reached it.
	got = helped(t, shaped(t), "robot", "ls", "--help")
	x.NoError(got.Err)
	x.Contains(got.Stdout, "app robot list [options]")
	x.NotContains(got.Stdout, "robot ls")

	// And the documentation xli writes is of the name: a reference has one
	// word for each command, and it is the schema's.
	root := &xli.Command{Name: "app"}
	x.NoError(shaped(t).Mount(root))
	md := strings.Builder{}
	x.NoError(xli.WriteMarkdown(&md, root))
	x.Contains(md.String(), "## app robot list\n")
	x.NotContains(md.String(), "robot ls")
}

// TestAnAppGivesACommandWordsOfItsOwn, an entity's group and a verb it added
// itself alike, and the words run what the name runs.
func TestAnAppGivesACommandWordsOfItsOwn(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := b.dialed(t, ctx)

	// Fresh per run, for the reason on [rooted]; and a mounted tree is not
	// changed, so a fresh root is a fresh tree.
	worded := func() (*pdcmd.Tree, *xli.Command) {
		t.Helper()

		tree, err := pdcmd.New(pdcmd.Static(conn))
		x.NoError(err)

		// `Tenant` is payday's, declared in payday's protos and copied in
		// whole: the word an app calls it by is the app's, and said here.
		x.NoError(tree.Alias("tenant", "workspace", "ws"))

		// A verb the app added is aliased once it is there.
		move, err := tree.Unary("app.RobotService.Move")
		x.NoError(err)
		x.NoError(tree.Add("robot/move", move))
		x.NoError(tree.Alias("robot/move", "mv"))

		root := &xli.Command{Name: "app"}
		x.NoError(tree.Mount(root))

		return tree, root
	}

	// A path may be written with an alias, as the command line may.
	tree, root := worded()
	x.Same(tree.Command("robot/move"), tree.Command("robot/mv"))
	x.Same(tree.Command("tenant/list"), tree.Command("ws/ls"))

	got := xlitest.Harness{Cmd: root, Ctx: b.travels(ctx)}.Run(t, "ws", "get", "@acme")
	x.NoError(got.Err)
	x.Contains(got.Stdout, b.Tenant.String())

	t.Setenv("XLI_AGENT", "0")
	_, root = worded()
	help := xlitest.Harness{Cmd: root}.Run(t, "--help")
	x.NoError(help.Err)
	x.Regexp(`(?m)^\s+tenant,workspace,ws\s+manage Tenant$`, help.Stdout)
}

// TestAWordThatWouldRunTwoCommandsIsRefused, where it is said.
//
// xli runs the first command a word matches and says nothing about the
// second, so a word two commands answer to is a command that silently never
// runs. Each door a word comes in by refuses it -- an alias, a command added,
// a replacement's own -- and so does a path that is not there, which would be
// an alias of nothing.
func TestAWordThatWouldRunTwoCommandsIsRefused(t *testing.T) {
	t.Run("an alias that is another command's name", func(t *testing.T) {
		x := require.New(t)
		tree := shaped(t)

		x.ErrorContains(tree.Alias("tenant", "robot"), `"robot" already runs robot`)
		x.ErrorContains(tree.Alias("robot/get", "list"), `"list" already runs robot/list`)
	})

	t.Run("an alias that is another command's alias", func(t *testing.T) {
		x := require.New(t)
		tree := shaped(t)

		x.NoError(tree.Alias("tenant", "ws"))
		x.ErrorContains(tree.Alias("robot", "ws"), `"ws" already runs tenant, as an alias`)

		// `ls` is this package's own, and held to the same rule.
		x.ErrorContains(tree.Alias("robot/get", "ls"), `"ls" already runs robot/list, as an alias`)
	})

	t.Run("an alias of nothing", func(t *testing.T) {
		x := require.New(t)
		tree := shaped(t)

		x.ErrorContains(tree.Alias("tenat", "ws"), "nothing to alias")
		x.ErrorContains(tree.Alias("cell/list", "ls"), "nothing to alias",
			"cell declared no list:, so there is no list to give a word to")
		x.ErrorContains(tree.Hide("tenat"), "nothing to hide")
		x.ErrorContains(tree.Alias("robot"), "no alias to give")
	})

	t.Run("a word that cannot be typed as one", func(t *testing.T) {
		x := require.New(t)
		tree := shaped(t)

		for _, w := range []string{"", "-r", "a b", "a/b"} {
			x.Error(tree.Alias("robot", w), "%q", w)
		}
	})

	t.Run("all of the words or none of them", func(t *testing.T) {
		x := require.New(t)
		tree := shaped(t)

		x.Error(tree.Alias("robot", "bot", "tenant"))
		x.Nil(tree.Command("bot"), "the word before the refused one was not added either")
		x.Empty(tree.Command("robot").Aliases)
	})

	t.Run("a word it already answers to is nothing to add", func(t *testing.T) {
		x := require.New(t)
		tree := shaped(t)

		x.NoError(tree.Alias("robot/list", "ls", "list", "ls"))
		x.Equal([]string{"ls"}, tree.Command("robot/list").Aliases)
	})

	t.Run("a command added under a word that runs another", func(t *testing.T) {
		x := require.New(t)
		tree := shaped(t)
		x.NoError(tree.Alias("tenant", "ws"))

		x.ErrorContains(tree.Add("robot/ls", &xli.Command{}), `"ls" already runs robot/list, as an alias`,
			"an `ls` of the app's own would be one nothing ever reached")
		x.ErrorContains(tree.Add("ws", &xli.Command{}), `"ws" already runs tenant, as an alias`)
		x.ErrorContains(tree.Add("robot/count", &xli.Command{Aliases: []string{"get"}}), `"get" already runs robot/get`)
		x.ErrorContains(tree.Add("robot/get", &xli.Command{}), "already here")
	})

	t.Run("a replacement that brings a word of its own", func(t *testing.T) {
		x := require.New(t)
		tree := shaped(t)

		x.ErrorContains(tree.Replace("robot/erase", &xli.Command{Aliases: []string{"get"}}), `"get" already runs robot/get`)
	})
}

// TestAReplacementKeepsThePlacesWords: what a path answered to, it answers to
// after, for the reason the name is kept -- a replacement that also took a
// word away would be two changes in one call.
func TestAReplacementKeepsThePlacesWords(t *testing.T) {
	x := require.New(t)
	tree := shaped(t)

	mine := &xli.Command{Name: "whatever", Aliases: []string{"l"}}
	x.NoError(tree.Replace("robot/ls", mine))
	x.Equal("list", mine.Name, "put where list was, by the alias that reached it")
	x.Equal([]string{"ls", "l"}, mine.Aliases)
	x.Same(mine, tree.Command("robot/ls"))

	x.NoError(tree.Hide("fact"))
	group := &xli.Command{Commands: xli.Commands{{Name: "only"}}}
	x.NoError(tree.Replace("fact", group))
	x.True(group.Hidden, "and hidden, if it was")

	// The verbs are the replacement's, and the old group's are not reachable
	// by a path either: the tree is read off the commands, not an index of
	// them that kept the old group's paths.
	x.NotNil(tree.Command("fact/only"))
	x.Nil(tree.Command("fact/get"))
}

// TestAHiddenCommandRunsAndIsNotShown: left out of everything that lists
// commands, and there in full for whoever names it.
func TestAHiddenCommandRunsAndIsNotShown(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := b.dialed(t, ctx)

	hidden := func() *xli.Command {
		t.Helper()

		tree, err := pdcmd.New(pdcmd.Static(conn))
		x.NoError(err)
		x.NoError(tree.Hide("holder"))

		root := &xli.Command{Name: "app"}
		x.NoError(tree.Mount(root))

		return root
	}

	// It runs by name, and whether the call is allowed is the server's.
	got := xlitest.Harness{Cmd: hidden(), Ctx: b.travels(ctx)}.Run(t, "holder", "get", "@acme/admin")
	x.NoError(got.Err)
	x.Contains(got.Stdout, "admin")

	t.Setenv("XLI_AGENT", "0")

	got = xlitest.Harness{Cmd: hidden()}.Run(t, "--help")
	x.NoError(got.Err)
	x.Regexp(`(?m)^\s+robot\s`, got.Stdout)
	x.NotRegexp(`(?m)^\s+holder\s`, got.Stdout)

	got = xlitest.Harness{Cmd: hidden()}.Run(t, "--help-all")
	x.NoError(got.Err)
	x.Contains(got.Stdout, "app robot list [options]")
	x.NotContains(got.Stdout, "app holder")

	// Its own help lists its verbs: hidden is the group, from the list it is
	// in, and nothing below it.
	got = xlitest.Harness{Cmd: hidden()}.Run(t, "holder", "--help")
	x.NoError(got.Err)
	x.Regexp(`(?m)^\s+list,ls\s+list Holder$`, got.Stdout)

	// Completion offers it nowhere, and everything below it once it is typed.
	comp := xlitest.Harness{Cmd: hidden()}.Complete(t, "")
	x.True(comp.Has("robot"))
	x.False(comp.Has("holder"))
	comp = xlitest.Harness{Cmd: hidden()}.Complete(t, "holder ")
	x.True(comp.Has("get"))

	// The documentation xli writes leaves it out with the rest of the help.
	md := strings.Builder{}
	x.NoError(xli.WriteMarkdown(&md, hidden()))
	x.Contains(md.String(), "## app robot list")
	x.NotContains(md.String(), "app holder")

	// And the help an AI agent is given, which maps every command below when
	// they do not fit, maps it with the rest left out.
	t.Setenv("XLI_AGENT", "1")
	got = xlitest.Harness{Cmd: hidden()}.Run(t, "--help")
	x.NoError(got.Err)
	x.Contains(got.Stdout, "robot")
	x.NotContains(got.Stdout, "holder")
}

// TestCompletionOffersNamesAndReadsAliases: a candidate is a command's name --
// one for each command, which is what a list of them is for -- and a word
// typed before the cursor is read as the command line reads it, alias or not.
func TestCompletionOffersNamesAndReadsAliases(t *testing.T) {
	x := require.New(t)

	tree := shaped(t)
	x.NoError(tree.Alias("tenant", "ws"))

	root := &xli.Command{Name: "app"}
	x.NoError(tree.Mount(root))

	comp := xlitest.Harness{Cmd: root}.Complete(t, "robot ")
	x.True(comp.Has("list"))
	x.False(comp.Has("ls"))

	comp = xlitest.Harness{Cmd: root}.Complete(t, "robot ls --")
	x.NoError(comp.Err)
	x.True(comp.Has("--next"), "the flags of list, reached by its alias")
	x.True(comp.Has("--order"))

	comp = xlitest.Harness{Cmd: root}.Complete(t, "ws ")
	x.True(comp.Has("get"), "the verbs of tenant, reached by an alias of the app's")
}

// TestTheTreeMountsWhereverItIsPut: at the root, or under a command of the
// app's, with nothing in it that assumes which.
func TestTheTreeMountsWhereverItIsPut(t *testing.T) {
	t.Run("at the root", func(t *testing.T) {
		x := require.New(t)

		got := helped(t, shaped(t), "robot", "list", "--help")
		x.NoError(got.Err)
		x.Contains(got.Stdout, "app.robot.list")
		x.Contains(got.Stdout, "app robot list [options]")
	})

	t.Run("under a command of the app's", func(t *testing.T) {
		x := require.New(t)
		t.Setenv("XLI_AGENT", "0")

		api := &xli.Command{Name: "api", Handler: xli.RequireSubcommand()}
		root := &xli.Command{Name: "app", Commands: xli.Commands{api}}
		x.NoError(shaped(t).Mount(api))

		got := xlitest.Harness{Cmd: root}.Run(t, "api", "robot", "list", "--help")
		x.NoError(got.Err)
		x.Contains(got.Stdout, "app.api.robot.list")
		x.Contains(got.Stdout, "app api robot list [options]")
	})

	t.Run("refusing a word that already runs one of its commands", func(t *testing.T) {
		x := require.New(t)

		// The app's own `tenant` beside payday's: one of the two would never
		// run, and nothing would say which.
		mine := &xli.Command{Name: "tenant"}
		root := &xli.Command{Name: "app", Commands: xli.Commands{mine}}
		err := shaped(t).Mount(root)
		x.ErrorContains(err, `mount on app: "tenant" already runs tenant there, and is the tree's tenant`)
		x.Len(root.Commands, 1, "nothing was put there")

		// And an alias, which is a word like any other.
		tree := shaped(t)
		x.NoError(tree.Alias("tenant", "ws"))
		root = &xli.Command{Name: "app", Commands: xli.Commands{{Name: "ws"}}}
		x.ErrorContains(tree.Mount(root), `"ws" already runs ws there, and is the tree's tenant, as an alias`)
	})

	t.Run("refusing a word set on a command directly", func(t *testing.T) {
		x := require.New(t)

		// `Aliases` is a field anybody holding the command can set, past every
		// method of the tree; Mount reads every level once more.
		tree := shaped(t)
		tree.Command("robot/get").Aliases = []string{"list"}
		x.ErrorContains(tree.Mount(&xli.Command{Name: "app"}), `robot/list: "list" already runs robot/get, as an alias`)
	})

	t.Run("and not changed once it is", func(t *testing.T) {
		x := require.New(t)

		tree := shaped(t)
		root := &xli.Command{Name: "app"}
		x.NoError(tree.Mount(root))

		// A group added now would be in the tree and not on the root.
		x.ErrorContains(tree.Add("late", &xli.Command{}), "mounted on app")
		x.ErrorContains(tree.Alias("robot", "bot"), "mounted on app")
		x.ErrorContains(tree.Hide("robot"), "mounted on app")
		x.ErrorContains(tree.Drop("robot"), "mounted on app")
		x.ErrorContains(tree.Replace("robot/get", &xli.Command{}), "mounted on app")
		x.ErrorContains(tree.Mount(&xli.Command{Name: "elsewhere"}), "already mounted on app")
	})
}

// TestAVerbOfAnRpcIsTheRpcsName, in the case a command is typed in -- the
// rule an entity's name follows, and `AddMany` is `add-many`.
func TestAVerbOfAnRpcIsTheRpcsName(t *testing.T) {
	x := require.New(t)
	tree := shaped(t)

	c, err := tree.Unary("app.RobotService.AddMany")
	x.NoError(err)
	x.Equal("add-many", c.Name)

	c, err = tree.Unary("app.RobotService.Move")
	x.NoError(err)
	x.Equal("move", c.Name)

	// Added by a path, it is called what the path says, as it always was.
	x.NoError(tree.Add("robot/relocate", c))
	x.Equal("relocate", c.Name)
}
