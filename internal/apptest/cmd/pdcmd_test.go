package cmd_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/google/uuid"

	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// The commands payday generates nothing for and builds anyway.
//
// Everything here runs against the app's real server over a real connection:
// the same chain, the same interceptors, the same wall. A test that called the
// server directly would be testing the request builder and not the command --
// and the request builder is the part that is obviously right.

// dialed is the app, reachable, with a connection a command can be handed.
func (b *built) dialed(t *testing.T, ctx context.Context) *grpc.ClientConn {
	t.Helper()
	return pdtest.Serve(t, b.grpc(t, pdtest.Logging(t)))
}

// rooted is a fresh command tree on `conn`.
//
// Fresh per run because a command holds the values parsed into it, so running
// one twice would run it with what the first run left behind. `xlitest` says as
// much; this is that.
func rooted(t *testing.T, conn *grpc.ClientConn, opts ...pdcmd.Options) *xli.Command {
	t.Helper()

	tree, err := pdcmd.New(pdcmd.Static(conn), opts...)
	require.NoError(t, err)

	return &xli.Command{Name: "app", Commands: tree.Commands()}
}

// TestTheTreeIsWhatTheSchemaDeclared is the claim that makes this worth
// generating nothing for.
//
// A tree of "every entity gets every verb" would be wrong on this app: seven of
// its eleven entities have no `List`, because `list:` is a thing an entity
// declares and most did not. So `robot ls` exists and `cell ls` does not, and
// neither was written down anywhere.
func TestTheTreeIsWhatTheSchemaDeclared(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	tree, err := pdcmd.New(pdcmd.Static(b.dialed(t, ctx)))
	x.NoError(err)

	// Spelled out rather than asserted one `Nil` at a time, because a `Nil` on
	// a path is also what a typo answers -- `tree.Command("cell/lsx")` is nil
	// too, and a test made of those asserts nothing at all.
	verbs := func(entity string) []string {
		c := tree.Command(entity)
		x.NotNil(c, entity)

		vs := []string{}
		for _, v := range c.Commands {
			vs = append(vs, v.Name)
		}

		return vs
	}

	// `Apply` is one of payday's two general writes and is closed unless an app
	// opts in, so it is not built and does not appear here even though `Robot`
	// has one.
	x.Equal([]string{"get", "ls", "watch", "add", "patch", "erase"}, verbs("robot"),
		"robot declared list: and watch:, so it has both")
	x.Equal([]string{"get", "add", "patch", "erase"}, verbs("cell"),
		"cell declared no list:, so there is no ls to call")

	// `Tenant` is the one that says `watch` is read off the schema rather than
	// added to everything that can be listed: it has a `List` and no `Watch`,
	// because `watch:` is declared separately and it did not.
	x.Equal([]string{"get", "ls", "add", "patch", "erase"}, verbs("tenant"),
		"tenant declared list: and no watch:")

	x.Nil(tree.Command("cell/ls"))
	x.NotNil(tree.Command("robot/ls"))
	x.Nil(tree.Command("tenant/watch"))
	x.NotNil(tree.Command("robot/watch"))
}

// TestACommandTravelsTheWholeStack is the command doing the only thing it does.
func TestACommandTravelsTheWholeStack(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := b.dialed(t, ctx)

	// The credential rides on the context the app passes to `Run`, which is the
	// whole of how these commands are authenticated: `pdcmd` holds none and
	// dials nothing, so what this call may do is what the app decided here.
	as := b.travels(ctx)

	got := xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.
		Run(t, "holder", "get", "@acme/admin")

	x.NoError(got.Err)
	x.Contains(got.Stdout, "alias")
	x.Contains(got.Stdout, "admin")
}

// TestTheDefaultFormatIsReadable is the one that was got wrong first, so it is
// the one worth a test.
//
// prototext is what a protobuf library hands you, and taking it made the format
// a person sees most the format they could use least: every payday identifier
// is a `bytes` field, so `id` printed as an escaped byte string that nothing
// can be done with, and an unset timestamp printed as year one.
func TestTheDefaultFormatIsReadable(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	got := xlitest.Harness{Cmd: rooted(t, b.dialed(t, ctx)), Ctx: b.travels(ctx)}.
		Run(t, "holder", "get", "@acme/admin")
	x.NoError(got.Err)

	// The identifier is the uuid a person types back, not the bytes on the wire.
	x.Contains(got.Stdout, b.Holder.String())
	x.NotContains(got.Stdout, `\x`)

	// The nested tenant is a line, not a block -- and its unread timestamps,
	// which prototext prints as year one, are not there at all.
	x.Contains(got.Stdout, b.Tenant.String())
	x.NotContains(got.Stdout, "0001-01-01")
	x.NotContains(got.Stdout, "-62135596800")

	// And `-o prototext` is still exactly prototext, for when the question is
	// what is on the wire. Named after the encoding rather than `-o raw`,
	// because `raw` already names the *input* here -- the trailing protojson of
	// a request -- and one word for both halves of a call is a word that has to
	// be explained every time.
	raw := xlitest.Harness{Cmd: rooted(t, b.dialed(t, ctx)), Ctx: b.travels(ctx)}.
		Run(t, "holder", "get", "-o", "prototext", "@acme/admin")
	x.NoError(raw.Err)
	x.Contains(raw.Stdout, `\x`)
	x.NotContains(raw.Stdout, b.Holder.String())
}

// TestAppCanRenderOneMessageItsOwnWay is the typed half of output: a format for
// one message type, used where a person did not ask for a format.
func TestAppCanRenderOneMessageItsOwnWay(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	opts := pdcmd.Options{Renderers: map[protoreflect.FullName]pdcmd.Printer{
		"app.Holder": pdcmd.PrinterFunc(func(w io.Writer, m proto.Message) error {
			_, err := fmt.Fprintln(w, "a person")
			return err
		}),
	}}

	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	got := xlitest.Harness{Cmd: rooted(t, conn, opts), Ctx: as}.
		Run(t, "holder", "get", "@acme/admin")
	x.NoError(got.Err)
	x.Equal("a person", strings.TrimSpace(got.Stdout))

	// And it does not touch the serialisations. A renderer that changed what
	// `-o json` produced would make a script's output depend on which app it
	// ran against.
	got = xlitest.Harness{Cmd: rooted(t, conn, opts), Ctx: as}.
		Run(t, "holder", "get", "-o", "json", "@acme/admin")
	x.NoError(got.Err)
	x.NotContains(got.Stdout, "a person")
	x.Contains(got.Stdout, "admin")
}

// TestTheWallIsStillTheWall: a command is a caller like any other.
//
// Without the credential the same call is refused, and it is refused by the
// server rather than by anything here. This is the test that says `pdcmd` is
// not a way around the wall -- which is the first question to ask of a package
// that builds admin commands for every entity.
func TestTheWallIsStillTheWall(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	got := xlitest.Harness{Cmd: rooted(t, b.dialed(t, ctx)), Ctx: ctx}.
		Run(t, "holder", "get", "@acme/admin")

	x.Error(got.Err)
	x.Contains(got.Err.Error(), "Unauthenticated")
}

// TestTheRawRequestIsMergedOverTheArguments is the flexible half.
//
// The typed argument says what it is called; everything else the request can
// hold is protojson. A command with a flag per field would be a second copy of
// the schema, and the copy is what goes stale.
func TestTheRawRequestIsMergedOverTheArguments(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	got := xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.
		Run(t, "holder", "add", "@acme/bob", `{"name":"Bob"}`)
	x.NoError(got.Err)
	x.Contains(got.Stdout, "bob")
	x.Contains(got.Stdout, "Bob", "the field no argument names came from the Json")

	// And it wins where the two overlap, which is what makes it an escape
	// hatch rather than a second way to say the same thing.
	got = xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.
		Run(t, "holder", "add", "@acme/carol", `{"alias":"carolyn"}`)
	x.NoError(got.Err)
	x.Contains(got.Stdout, "carolyn")
	x.Regexp(`alias +carolyn`, got.Stdout)
}

// TestTheRawRequestCanBeStdin, because a request composed by a script is not
// one somebody wants to quote for a shell.
func TestTheRawRequestCanBeStdin(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	got := xlitest.Harness{
		Cmd:   rooted(t, b.dialed(t, ctx)),
		Ctx:   b.travels(ctx),
		Stdin: `{"tenant":{"alias":"acme"},"alias":"dave","name":"Dave"}`,
	}.Run(t, "holder", "add", "-")

	x.NoError(got.Err)
	x.Contains(got.Stdout, "dave")
	x.Contains(got.Stdout, "Dave")
}

func TestTheFormats(t *testing.T) {
	b, ctx := build(t)
	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	run := func(t *testing.T, args ...string) xlitest.Result {
		t.Helper()
		return xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.Run(t, args...)
	}

	t.Run("json is Json", func(t *testing.T) {
		x := require.New(t)

		got := run(t, "holder", "get", "-o", "json", "@acme/admin")
		x.NoError(got.Err)
		x.Contains(got.Stdout, `"alias"`)
		x.Contains(got.Stdout, `"admin"`)

		// Which of the two Jsons this is, and how they differ, is
		// TestThereAreTwoJsons.
	})

	t.Run("name is the identifier and nothing else", func(t *testing.T) {
		x := require.New(t)

		got := run(t, "holder", "get", "-o", "name", "@acme/admin")
		x.NoError(got.Err)
		x.Equal(b.Holder.String(), strings.TrimSpace(got.Stdout))
	})

	t.Run("table has a header and a row", func(t *testing.T) {
		x := require.New(t)

		got := run(t, "holder", "ls", "-o", "table")
		x.NoError(got.Err)

		lines := strings.Split(strings.TrimSpace(got.Stdout), "\n")
		x.GreaterOrEqual(len(lines), 2)
		x.Contains(lines[0], "ALIAS")
		x.Contains(lines[0], "AGE")
		x.NotContains(lines[0], "Id", "the identifier is wide, so a terminal shows the readable columns")
		x.Contains(got.Stdout, "admin")
	})

	t.Run("wide is the same table with the identifiers", func(t *testing.T) {
		x := require.New(t)

		got := run(t, "holder", "ls", "-o", "wide")
		x.NoError(got.Err)
		x.Contains(got.Stdout, "Id")
		x.Contains(got.Stdout, b.Holder.String())
	})

	t.Run("a template reads the names -o json prints", func(t *testing.T) {
		x := require.New(t)

		got := run(t, "holder", "get", "-o", "template={{.alias}}", "@acme/admin")
		x.NoError(got.Err)
		x.Equal("admin", strings.TrimSpace(got.Stdout))
	})

	t.Run("and one that is not a format is refused by name", func(t *testing.T) {
		x := require.New(t)

		got := run(t, "holder", "get", "-o", "yaml", "@acme/admin")
		x.Error(got.Err)
		x.Contains(got.Err.Error(), "not a format")
	})
}

// TestLsPagesWithNext is the round trip the two paging flags exist for: the
// "next" of one answer, handed back, is the page after it.
//
// It is worth a test of its own because the halves have different names. The
// answer calls the cursor "next" and the generated request calls it "after",
// and a command that wrote the flag onto a field the request does not have
// would drop it without a word: every page would be the first page, and the
// first page again is exactly what an honest one-page list looks like.
func TestLsPagesWithNext(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	b.sow(ctx, x, b.Tenant, 3, "arm-")

	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	// `-o json` rather than the default, because the cursor is part of the
	// answer and this reads it back the way a script paging through would.
	page := func(next string) (string, string) {
		t.Helper()

		args := []string{"robot", "ls", "-o", "json", "--size", "1"}
		if next != "" {
			args = append(args, "--next", next)
		}

		got := xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.Run(t, args...)
		x.NoError(got.Err)

		var res struct {
			Items []struct {
				Alias string `json:"alias"`
			} `json:"items"`
			Next string `json:"next"`
		}
		x.NoError(json.Unmarshal([]byte(got.Stdout), &res))
		x.Len(res.Items, 1, "one at a time is what --size asked for")

		return res.Items[0].Alias, res.Next
	}

	first, next := page("")
	x.NotEmpty(next, "three rows and a page of one, so there is more")

	seen := []string{first}
	for next != "" {
		alias, n := page(next)
		x.NotContains(seen, alias, "a row repeated, so the cursor went nowhere")

		seen = append(seen, alias)
		next = n

		x.LessOrEqual(len(seen), 3, "the cursor never ended")
	}

	x.Len(seen, 3, "read through, every row arrived once")
}

// TestAnAppCanAddAFormat: the escape hatch for output is the same interface the
// built-in formats implement.
func TestAnAppCanAddAFormat(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	opts := pdcmd.Options{Printers: map[string]pdcmd.Printer{
		"shout": pdcmd.PrinterFunc(func(w io.Writer, m proto.Message) error {
			for _, row := range pdcmd.Rows(m) {
				fd := row.Descriptor().Fields().ByName("alias")
				fmt.Fprintln(w, strings.ToUpper(row.Get(fd).String()))
			}

			return nil
		}),
	}}

	got := xlitest.Harness{Cmd: rooted(t, b.dialed(t, ctx), opts), Ctx: b.travels(ctx)}.
		Run(t, "holder", "get", "-o", "shout", "@acme/admin")

	x.NoError(got.Err)
	x.Equal("ADMIN", strings.TrimSpace(got.Stdout))
}

// TestOneCommandCanBeReplaced is the reason this is a library rather than a
// generator.
//
// An app that means something particular by a verb swaps that one command and
// keeps the rest. What goes in is an `*xli.Command`, which is what came out --
// so a hand-written command is not a lesser kind of command here.
func TestOneCommandCanBeReplaced(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	tree, err := pdcmd.New(pdcmd.Static(b.dialed(t, ctx)))
	x.NoError(err)

	x.NoError(tree.Replace("holder/erase", &xli.Command{
		Brief: "refuse, because this deployment does not erase people",
		Args:  arg.Args{&pdcmd.ArgRef{Name: "REF"}},
		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			return errors.New("people are erased in the identity store, not here")
		}),
	}))

	root := &xli.Command{Name: "app", Commands: tree.Commands()}
	got := xlitest.Harness{Cmd: root, Ctx: b.travels(ctx)}.Run(t, "holder", "erase", "@acme/admin")

	x.Error(got.Err)
	x.Contains(got.Err.Error(), "people are erased in the identity store")

	// And the rest of the tree is untouched.
	x.NotNil(tree.Command("holder/get"))
}

// TestReplacingWhatIsNotThereIsRefused, because the alternative is a command
// that silently never runs -- a typo and a deliberate addition look identical
// at the call site.
func TestReplacingWhatIsNotThereIsRefused(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	tree, err := pdcmd.New(pdcmd.Static(b.dialed(t, ctx)))
	x.NoError(err)

	x.Error(tree.Replace("holder/gett", &xli.Command{}))
	x.Error(tree.Replace("cell/ls", &xli.Command{}), "cell has no list to replace")
}

// TestATreeCanBeNarrowed: a deployment that serves a subset says so.
func TestATreeCanBeNarrowed(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	tree, err := pdcmd.New(pdcmd.Static(b.dialed(t, ctx)))
	x.NoError(err)

	x.NoError(tree.Drop("holder/erase"))
	x.Nil(tree.Command("holder/erase"))
	x.NotNil(tree.Command("holder/get"))

	x.NoError(tree.Drop("audit"))
	x.Nil(tree.Command("audit"))
	x.Nil(tree.Command("audit/get"), "the whole group went")

	for _, c := range tree.Commands() {
		x.NotEqual("audit", c.Name)
	}
}

// TestAnIdentifierIsWhatTheSchemaSaysItIs, and not what its name looks like.
//
// The first version of the pretty format matched `id` and anything ending in
// `_id`, which is payday's field-number rule written in names and is wrong on
// this app: `Audit.trace_id` is a `bytes` field ending in `_id` that carries no
// `type: TYPE_UUID`, because it is a trace identifier from somewhere else. A
// rule about names prints it as a uuid that names nothing here.
func TestAnIdentifierIsWhatTheSchemaSaysItIs(t *testing.T) {
	x := require.New(t)

	u, err := pdid.Mint(pd.AuditDomain, uuid.Nil, false)
	x.NoError(err)
	id := pdid.Id(u)

	trace := []byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}

	var b strings.Builder
	x.NoError(pdcmd.Pretty.Print(&b, app.Audit_builder{
		Id:      id.Bytes(),
		TraceID: trace,
		Action:  "Add",
	}.Build()))

	got := b.String()
	x.Contains(got, id.String(), "the key carries TYPE_UUID")
	x.NotContains(got, "dead-beef", "and the trace identifier does not, so it is not drawn as one")
	x.Contains(got, "(16 bytes)", "what a bytes field payday did not put there is worth saying")
}

// TestThereAreTwoJsons, and which one somebody gets is which one they asked for.
//
// protojson is right about `bytes` and useless to a person: every payday
// identifier is one, so `-o protojson` answers base64 that has to be decoded
// before it means anything. Rewriting it in place would break the property that
// makes `-o protojson` worth having -- that a script can feed it back.
func TestThereAreTwoJsons(t *testing.T) {
	b, ctx := build(t)
	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	run := func(t *testing.T, args ...string) xlitest.Result {
		t.Helper()
		return xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.Run(t, args...)
	}

	t.Run("protojson is exactly protojson", func(t *testing.T) {
		x := require.New(t)

		got := run(t, "holder", "get", "-o", "protojson", "@acme/admin")
		x.NoError(got.Err)
		x.NotContains(got.Stdout, b.Holder.String())

		var v map[string]any
		x.NoError(json.Unmarshal([]byte(got.Stdout), &v))
		x.Equal(base64.StdEncoding.EncodeToString(b.Holder.Bytes()), v["id"])
	})

	t.Run("json writes the identifiers a person writes", func(t *testing.T) {
		x := require.New(t)

		got := run(t, "holder", "get", "-o", "json", "@acme/admin")
		x.NoError(got.Err)

		var v map[string]any
		x.NoError(json.Unmarshal([]byte(got.Stdout), &v), "still Json")
		x.Equal(b.Holder.String(), v["id"])
		x.Equal(b.Tenant.String(), v["tenant"].(map[string]any)["id"], "and inside a nested entity")

		// In the order the schema declares them, not alphabetically: `id` is
		// written first and read first, and an encoder that sorted would put
		// `alias` above it.
		x.Less(strings.Index(got.Stdout, `"id"`), strings.Index(got.Stdout, `"alias"`))
	})

	t.Run("and a bytes field payday did not put there stays base64", func(t *testing.T) {
		x := require.New(t)

		var s strings.Builder
		x.NoError(pdcmd.Json.Print(&s, app.Audit_builder{
			Id:      b.Holder.Bytes(),
			TraceID: []byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12},
		}.Build()))

		var v map[string]any
		x.NoError(json.Unmarshal([]byte(s.String()), &v))
		x.Equal("3q2+7wECAwQFBgcICQoLDA==", v["traceId"], "no TYPE_UUID, so it is what it is")
	})
}

// TestAnIdentifierCanBeWrittenAsAUuid is the other half of the round trip.
//
// Without it this app prints an identifier one way and refuses to be told it
// that way: protojson reads `bytes` as base64, so the uuid somebody just read
// off the screen cannot be typed back into a request.
func TestAnIdentifierCanBeWrittenAsAUuid(t *testing.T) {
	b, ctx := build(t)
	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	run := func(t *testing.T, args ...string) xlitest.Result {
		t.Helper()
		return xlitest.Harness{Cmd: rooted(t, conn), Ctx: as}.Run(t, args...)
	}

	t.Run("in a request", func(t *testing.T) {
		x := require.New(t)

		got := run(t, "holder", "add",
			fmt.Sprintf(`{"tenant":{"id":"%s"},"alias":"erin"}`, b.Tenant))
		x.NoError(got.Err)
		x.Contains(got.Stdout, "erin")
		x.Contains(got.Stdout, b.Tenant.String())
	})

	t.Run("and base64 still works, because only an exact uuid is converted", func(t *testing.T) {
		x := require.New(t)

		got := run(t, "holder", "add",
			fmt.Sprintf(`{"tenant":{"id":"%s"},"alias":"frank"}`,
				base64.StdEncoding.EncodeToString(b.Tenant.Bytes())))
		x.NoError(got.Err)
		x.Contains(got.Stdout, "frank")
	})

	// This is what the lenient reading is actually for, and it is not what it
	// looked like. protojson does not refuse a uuid: it accepts URL-safe base64
	// as well as standard, `-` is in that alphabet, and a uuid string decodes
	// to 27 bytes of nothing. The refusal comes later, from the server, about a
	// value nobody wrote.
	t.Run("and --in protojson is the stricter contract, which reads it as base64", func(t *testing.T) {
		x := require.New(t)

		got := run(t, "holder", "add", "--in", "protojson",
			fmt.Sprintf(`{"tenant":{"id":"%s"},"alias":"grace"}`, b.Tenant))
		x.Error(got.Err)
		x.Contains(got.Err.Error(), "27 bytes")
	})

	t.Run("and an input format that is not one is refused by name", func(t *testing.T) {
		x := require.New(t)

		got := run(t, "holder", "add", "--in", "yaml", `{"alias":"heidi"}`)
		x.Error(got.Err)
		x.Contains(got.Err.Error(), "not an input format")
	})
}

// TestAnRpcThisAppWroteGetsACommandToo is the case the six verbs do not cover.
//
// payday closes the general writes on purpose, so an operation that means
// something -- moving a row to another tenant, say -- is an RPC the app
// declares in an overlay and implements in a layer. Nothing can generate a
// command for it, because nothing knows what it means. What can be shared is
// everything around it, which is what [pdcmd.Tree.Unary] is.
//
// It reaches a method by name and does not care where the method came from: an
// overlay and `pd gen` write into the same `ServiceDescriptor`, merged before
// generation. This app has one -- `RobotService.Move`, declared in
// `proto/ext/app/robot_svc.ext.proto` and answered in `server/core` -- so that
// is what is used here rather than something standing in for it.
func TestAnRpcThisAppWroteGetsACommandToo(t *testing.T) {
	b, ctx := build(t)
	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	t.Run("the Rpc this app declared", func(t *testing.T) {
		x := require.New(t)

		tenant := app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build()
		cell, err := b.Ungated.Cell().Add(ctx, app.CellAddRequest_builder{
			Tenant: tenant, Alias: "floor-7",
		}.Build())
		x.NoError(err)
		_, err = b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
			Tenant: tenant, Alias: "arm-07",
		}.Build())
		x.NoError(err)

		tree, err := pdcmd.New(pdcmd.Static(conn))
		x.NoError(err)
		x.Nil(tree.Command("robot/move"), "the six verbs do not include it")

		c, err := tree.Unary("app.RobotService.Move")
		x.NoError(err)
		x.NoError(tree.Add("robot/move", c))

		root := &xli.Command{Name: "app", Commands: tree.Commands()}
		got := xlitest.Harness{Cmd: root, Ctx: as}.Run(t,
			"robot", "move", "@acme/arm-07",
			fmt.Sprintf(`{"to":{"id":"%s"}}`, mustId(x, cell.GetId())))

		x.NoError(got.Err)
		x.Contains(got.Stdout, "arm-07")
		x.Contains(got.Stdout, mustId(x, cell.GetId()).String(),
			"and it is in the cell it was moved to")
	})

	t.Run("and one payday generated but does not build", func(t *testing.T) {
		x := require.New(t)

		tree, err := pdcmd.New(pdcmd.Static(conn))
		x.NoError(err)
		x.Nil(tree.Command("robot/apply"), "Apply is a general write and is not built")

		c, err := tree.Unary("app.RobotService.Apply")
		x.NoError(err)
		x.NoError(tree.Add("robot/apply", c))

		root := &xli.Command{Name: "app", Commands: tree.Commands()}
		got := xlitest.Harness{Cmd: root, Ctx: as}.Run(t,
			"robot", "apply", "@acme/nothing", `{"patch":{}}`)

		// The refusal is the server's, about the patch -- so the request was
		// built, sent, and understood. That is what is being checked; a valid
		// patch would be checking `patch.Patch`.
		x.Error(got.Err)
		x.Contains(got.Err.Error(), "delta")
	})

	t.Run("the reference argument is worked out from the request", func(t *testing.T) {
		x := require.New(t)

		tree, err := pdcmd.New(pdcmd.Static(conn))
		x.NoError(err)

		// `HolderGetRequest` has a `ref`, so the command takes one.
		c, err := tree.Unary("app.HolderService.Get")
		x.NoError(err)
		x.Equal("REF", c.Args[0].Info().Name)

		// `HolderAddRequest` does not, so it takes only the request.
		c, err = tree.Unary("app.HolderService.Add")
		x.NoError(err)
		x.Equal("REQ", c.Args[0].Info().Name)
	})

	t.Run("and what cannot be a command is refused while it is being wired", func(t *testing.T) {
		x := require.New(t)

		tree, err := pdcmd.New(pdcmd.Static(conn))
		x.NoError(err)

		_, err = tree.Unary("app.RobotService.Watch")
		x.ErrorContains(err, "is a stream")

		_, err = tree.Unary("app.RobotService.Nope")
		x.ErrorContains(err, "no such method")

		_, err = tree.Unary("app.NoSuchService.X")
		x.ErrorContains(err, "app.NoSuchService")
	})
}

// TestWhichAppThisConnectionSpeaksTo covers the case an embedded server creates.
//
// Two payday apps can share a process whenever their proto packages differ, and
// an app that embeds another is such a process: two `Holder` entities are in
// the registry at once and a connection cannot say which of them it reaches.
//
// **A package is not an app**, which is what this app is here to show: it holds
// entities in two -- `app`, and `shared` for a name meant to be the same word in
// more than one service -- and it is still one app, because `app/robot.proto`
// says `option (payday.app)`. So `Packages` finds two and `New` is not stuck:
// what it needs is the app's package, and one of them said so.
func TestWhichAppThisConnectionSpeaksTo(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)
	conn := b.dialed(t, ctx)

	x.Equal([]protoreflect.FullName{"app", "shared"}, pdcmd.Packages(),
		"both hold entities, which is what this lists")

	// And New answers, rather than refusing for finding two.
	guessed, err := pdcmd.New(pdcmd.Static(conn))
	x.NoError(err)
	x.NotNil(guessed.Command("holder/get"))

	tree := pdcmd.NewIn(pdcmd.Static(conn), "app")
	x.NotNil(tree.Command("holder/get"))

	got := xlitest.Harness{
		Cmd: &xli.Command{Name: "app", Commands: tree.Commands()},
		Ctx: b.travels(ctx),
	}.Run(t, "holder", "get", "@acme/admin")
	x.NoError(got.Err)
	x.Contains(got.Stdout, "admin")

	// A package with no entities builds an empty tree rather than failing, so
	// that a caller naming the wrong one finds out from the tree it got.
	x.Empty(pdcmd.NewIn(pdcmd.Static(conn), "payday").Commands())
}

// TestSharedThingIsServedLikeAnyOtherEntity is the "serves" of the claim in
// docs/guide/packages.md: `shared.Thing` generates, serves and reaches
// TypeScript like any other entity.
//
// Generation is proven by the tree above holding a `shared` package at all;
// this is the row actually going in and coming back out, through the same
// server, the same chain and the same credential everything in `app` travels.
// Driven through `NewIn` because that is how an embedded deployment would
// reach the shared package by name rather than by guess.
func TestSharedThingIsServedLikeAnyOtherEntity(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	conn := b.dialed(t, ctx)
	as := b.travels(ctx)

	// Fresh per run, for the reason on [rooted]: a command holds the values
	// parsed into it.
	sharedRoot := func() *xli.Command {
		return &xli.Command{Name: "app", Commands: pdcmd.NewIn(pdcmd.Static(conn), "shared").Commands()}
	}

	// `-o name` because the answer this test needs is the identifier: `Thing`
	// is named by identifier alone -- global, so no tenant to hang a slug on --
	// and the uuid printed here is what the `get` below types back.
	made := xlitest.Harness{Cmd: sharedRoot(), Ctx: as}.Run(t, "thing", "add", "-o", "name", "@gizmo")
	x.NoError(made.Err)

	id, err := pdid.Parse(strings.TrimSpace(made.Stdout))
	x.NoError(err)
	x.Equal(pd.ThingDomain, id.Domain(), "minted with the domain the shared schema declared")

	got := xlitest.Harness{Cmd: sharedRoot(), Ctx: as}.Run(t, "thing", "get", id.String())
	x.NoError(got.Err)
	x.Contains(got.Stdout, "gizmo", "the row round-trips")
	x.Contains(got.Stdout, id.String())
}

// mustId is a `bytes` identifier as the uuid a command line carries.
func mustId(x *require.Assertions, b []byte) pdid.Id {
	v, err := pdid.From(b)
	x.NoError(err)

	return v
}

// TestTheConnectionIsOpenedWhenACommandRuns is the whole reason [pdcmd.Connector]
// is a function.
//
// A tree is built while an app is assembling its commands, which is before a
// flag has been parsed and before a configuration file has been read -- and the
// address to dial is in that file. So an app that had to hand over a connection
// would have to open one before it knew where to.
func TestTheConnectionIsOpenedWhenACommandRuns(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	conn := b.dialed(t, ctx)

	opened, closed := 0, 0
	to := connectorFunc(func(context.Context) (pdcmd.Conn, func(), error) {
		opened++
		return conn, func() { closed++ }, nil
	})

	tree, err := pdcmd.New(to)
	x.NoError(err)
	x.Zero(opened, "building the tree opened a connection")

	root := &xli.Command{Name: "app", Commands: tree.Commands()}
	got := xlitest.Harness{Cmd: root, Ctx: b.travels(ctx)}.Run(t, "holder", "get", "@acme/admin")
	x.NoError(got.Err)

	x.Equal(1, opened)
	x.Equal(1, closed, "what was opened for the command was not let go of")
}

// TestPrintingHelpOpensNothing.
//
// `--help` and a completion are what somebody types while working out what to
// type next, and neither should be able to hang on a server that is not there.
// It is `mode.Run` that says so; a tree holding a socket could not.
func TestPrintingHelpOpensNothing(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	opened := 0
	to := connectorFunc(func(context.Context) (pdcmd.Conn, func(), error) {
		opened++
		return nil, nil, errors.New("this deployment cannot reach the server")
	})

	tree, err := pdcmd.New(to)
	x.NoError(err)

	root := &xli.Command{Name: "app", Commands: tree.Commands()}
	got := xlitest.Harness{Cmd: root, Ctx: b.travels(ctx)}.Run(t, "holder", "get", "--help")

	// The usage message, and no refusal -- which is the assertion: a connector
	// that cannot connect to anything is never asked.
	x.NoError(got.Err)
	x.Zero(opened)
	x.Contains(got.Stdout, "get")
}

// TestAConnectorThatCannotConnectIsTheCommandsAnswer, so that a bad address is
// reported by the command somebody ran rather than by the app starting up.
func TestAConnectorThatCannotConnectIsTheCommandsAnswer(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	tree, err := pdcmd.New(connectorFunc(func(context.Context) (pdcmd.Conn, func(), error) {
		return nil, nil, errors.New("server.addr names nothing")
	}))
	x.NoError(err)

	root := &xli.Command{Name: "app", Commands: tree.Commands()}
	got := xlitest.Harness{Cmd: root, Ctx: b.travels(ctx)}.Run(t, "holder", "get", "@acme/admin")

	x.Error(got.Err)
	x.Contains(got.Err.Error(), "server.addr names nothing")
}

// TestACommandOfTheAppsOwnGetsTheSameConnection.
//
// A command an app adds to the tree has to reach the same server the built ones
// do. Opening its own would be a second socket, a second credential to get
// right, and -- for a connector that hands out an in-process server -- a second
// server, which is a different database.
func TestACommandOfTheAppsOwnGetsTheSameConnection(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	conn := b.dialed(t, ctx)

	opened := 0
	tree, err := pdcmd.New(connectorFunc(func(context.Context) (pdcmd.Conn, func(), error) {
		opened++
		return conn, nil, nil
	}))
	x.NoError(err)

	var saw pdcmd.Conn
	x.NoError(tree.Add("holder/count", &xli.Command{
		Name:  "count",
		Brief: "how many there are",
		Handler: xli.Chain(tree.WithConn(), xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			saw = pdcmd.MustConn(ctx)

			c := app.NewHolderServiceClient(saw)
			vs, err := c.List(ctx, app.HolderListRequest_builder{}.Build())
			if err != nil {
				return err
			}

			cmd.Printf("%d\n", len(vs.GetItems()))

			return next(ctx)
		})),
	}))

	root := &xli.Command{Name: "app", Commands: tree.Commands()}
	got := xlitest.Harness{Cmd: root, Ctx: b.travels(ctx)}.Run(t, "holder", "count")
	x.NoError(got.Err)
	x.Equal("1\n", got.Stdout)

	x.Same(conn, saw, "the app's own command reached a different connection")
	x.Equal(1, opened, "it opened a second one")
}

// connectorFunc is a [pdcmd.Connector] written inline, which is a thing a test
// wants and an app does not.
//
// payday ships no such adapter on purpose. What an app writes is not a callback
// -- it is which address, which credential, and whether the server is in this
// process at all -- and those want a name and a comment saying why. A test's
// connector is none of that; it counts calls.
type connectorFunc func(ctx context.Context) (pdcmd.Conn, func(), error)

func (f connectorFunc) Connect(ctx context.Context) (pdcmd.Conn, func(), error) { return f(ctx) }
