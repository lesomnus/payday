package pdcmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Options is what an app changes about a whole [Tree].
//
// Every field is optional, and the zero value is the tree payday would build on
// its own.
type Options struct {
	// Default is the format when `-o` is not given. Empty means "pretty".
	Default string

	// Printers adds a format, or replaces one of the built-in names.
	//
	// This is the general escape hatch for output: a name here is a name `-o`
	// accepts, and what it does is entirely the app's. It is the same interface
	// the built-in formats implement, so an app's format is not a lesser kind
	// of format.
	Printers map[string]Printer

	// Renderers is a format for one message type, used only when the person did
	// not ask for a format.
	//
	// Only then, and this is worth being exact about: `-o json` means JSON, and
	// a renderer that changed what `-o json` produced would make a script's
	// output depend on which app it ran against. kubectl draws the same line --
	// its typed print handlers shape the human-readable table and never the
	// serialisations.
	Renderers map[protoreflect.FullName]Printer
}

func (o Options) def() string {
	if o.Default == "" {
		return "pretty"
	}

	return o.Default
}

// Tree is the entity commands built for one connection.
//
// # What it does not decide
//
// Where to connect, who to connect as, and what that credential may do. All
// three are the deployment's, and getting them wrong is how an admin command
// becomes a way around the wall. So a command here is handed a [Conn] and does
// nothing but turn the arguments into the request the schema declared, send it,
// and print what came back. It reads no configuration file and holds no
// credential.
//
// That is what lets one app have several trees: an app may put an operator port
// and a data port behind different policies, and an app that embeds another
// reaches the embedded one over an in-process pipe while reaching itself over a
// socket. Each is a connection, so each is a tree.
//
// # Three ways in, and they are the same commands
//
// The whole tree, which is the whole of what an app has to write:
//
//	t, err := pdcmd.New(to)
//	root.Commands = append(root.Commands, t.Commands()...)
//
// One command of it, to mount somewhere else or to wrap:
//
//	t.Command("robot/ls")
//
// Or replaced, where this app means something particular by a verb:
//
//	t.Replace("robot/erase", myOwnErase)
//	t.Drop("audit/add")
//
// A replacement is an `*xli.Command`, and so is everything built here, so there
// is no tier a hand-written command drops into. That is deliberate, and it is
// the lesson of `kubectl describe`: its typed describers are in-tree and a
// custom resource falls back to a generic one whose output is noticeably worse,
// so the fallback exists and nobody wants it. `kubectl get` avoided that by
// letting the resource declare its own columns, which is the same shape as
// [Options.Renderers] here.
//
// # What is not built
//
// `Apply`. It is one of payday's two general writes and is closed unless an app
// opted in, so a command for it would fail on every app that took the default,
// and an app that did opt in means something particular by it -- which is a
// command it writes and mounts beside these.
//
// `Watch` **is** built, on the entities that declared one, the same way `ls` is
// built only where there is a `List`. It was left out for a while as "a stream,
// and a stream is not this shape"; what was actually missing was an opinion
// about what a stream ending means, and [cmdWatch] is that opinion.
type Tree struct {
	run  *runner
	opts Options
	pkg  protoreflect.FullName

	order []string
	cmds  map[string]*xli.Command
}

// New builds the tree for the one payday app in this process.
//
// It refuses when there is more than one, naming them, because the connection
// cannot say which it speaks to -- see [Entities]. An app in that position uses
// [NewIn], which is not a workaround: a process with two apps has two servers
// and wants two trees.
//
// `to` is asked when a command runs rather than now; see [Connector]. An app
// that already holds a connection -- a test, an embedded server over `bufconn`
// -- passes [Static].
func New(to Connector, opts ...Options) (*Tree, error) {
	pkg, err := solePackage()
	if err != nil {
		return nil, err
	}

	return NewIn(to, pkg, opts...), nil
}

// NewIn builds the tree for the app whose proto package is `pkg`.
func NewIn(to Connector, pkg protoreflect.FullName, opts ...Options) *Tree {
	o := Options{}
	if len(opts) > 0 {
		o = opts[0]
	}

	r := &runner{conn: to, opts: o}

	t := &Tree{
		run:  r,
		opts: o,
		pkg:  pkg,
		cmds: map[string]*xli.Command{},
	}

	for _, e := range Entities(pkg) {
		vs := xli.Commands{}
		for _, v := range verbs {
			md := e.Method(v.method)
			if md == nil {
				// The verb this entity does not have. Seven of payday's own
				// eleven test entities have no `List`, because `list:` is a
				// thing an entity declares and most did not -- so this is the
				// ordinary case and not an error.
				continue
			}
			if md.IsStreamingClient() || md.IsStreamingServer() != v.stream {
				// Not the shape this verb is; see `stream` on [verbs]. Nothing
				// here takes a client stream at all -- there is no verb whose
				// argument is a sequence.
				continue
			}

			c := v.build(e, md, r)
			vs = append(vs, c)
			t.cmds[e.Name+"/"+v.name] = c
		}

		if len(vs) == 0 {
			// An entity with no served contract at all. It is stored and it is
			// real; there is simply nothing to call about it.
			continue
		}

		t.cmds[e.Name] = &xli.Command{
			Name:     e.Name,
			Brief:    fmt.Sprintf("manage %s", e.Message.Name()),
			Commands: vs,
			Handler:  xli.RequireSubcommand(),
		}
		t.order = append(t.order, e.Name)
	}

	return t
}

// WithConn opens the connection, puts it in the context and closes it when the
// command is done.
//
// Every command this package builds already has it. It is exported for one an
// app wrote itself: chain it in front, and [ConnFrom] answers with the same
// connection the generated commands are using rather than a second one.
//
//	c := &xli.Command{
//		Name:    "resend",
//		Handler: xli.Chain(t.WithConn(), xli.OnRun(func(ctx context.Context, ...) error {
//			cl := app.NewRobotServiceClient(pdcmd.MustConn(ctx))
//			...
//		})),
//	}
//	t.Add("robot/resend", c)
//
// It is idempotent, so chaining it on a command mounted under another that has
// it costs nothing and opens nothing.
func (t *Tree) WithConn() xli.Handler { return t.run.withConn() }

// Commands is the tree, ready to be appended to an app's root.
func (t *Tree) Commands() xli.Commands {
	vs := make(xli.Commands, 0, len(t.order))
	for _, name := range t.order {
		vs = append(vs, t.cmds[name])
	}

	return vs
}

// Command answers with one command by path -- "robot" or "robot/ls" -- or nil.
//
// The nil is usable: asking for "cell/ls" on an entity that declared no `list:`
// answers nothing, which is the same answer the tree gives.
func (t *Tree) Command(path string) *xli.Command {
	return t.cmds[path]
}

// Replace swaps one command for the app's own.
//
// It refuses a path that is not there rather than adding it, because a typo in
// a path would otherwise be a command that silently never runs -- and a typo
// and a deliberate addition look identical at the call site. [Tree.Add] is how
// a new one goes in.
func (t *Tree) Replace(path string, c *xli.Command) error {
	old, ok := t.cmds[path]
	if !ok {
		return fmt.Errorf("pdcmd: %s: nothing to replace", path)
	}

	entity, verb, isVerb := strings.Cut(path, "/")
	if !isVerb {
		c.Name = entity
		t.cmds[path] = c

		return nil
	}

	parent, ok := t.cmds[entity]
	if !ok {
		return fmt.Errorf("pdcmd: %s: %s is not here", path, entity)
	}

	for i, v := range parent.Commands {
		if v != old {
			continue
		}

		// The name is kept rather than taken from the replacement, so that
		// `Replace("robot/ls", c)` puts `c` where `ls` was even when `c` is
		// called something else. A replacement that also renamed would be two
		// changes in one call.
		c.Name = verb
		parent.Commands[i] = c
		t.cmds[path] = c

		return nil
	}

	return fmt.Errorf("pdcmd: %s: not in %s", path, entity)
}

// Add puts a command into an entity's group, or a new group at the top.
func (t *Tree) Add(path string, c *xli.Command) error {
	if _, ok := t.cmds[path]; ok {
		return fmt.Errorf("pdcmd: %s: already here", path)
	}

	entity, verb, isVerb := strings.Cut(path, "/")
	if !isVerb {
		c.Name = entity
		t.cmds[path] = c
		t.order = append(t.order, path)

		return nil
	}

	parent, ok := t.cmds[entity]
	if !ok {
		return fmt.Errorf("pdcmd: %s: %s is not here", path, entity)
	}

	c.Name = verb
	parent.Commands = append(parent.Commands, c)
	t.cmds[path] = c

	return nil
}

// Drop removes a command, which is how a tree is narrowed to a subset.
//
// Dropping an entity drops its verbs with it: what is being said is "this
// deployment does not serve robots here", and leaving `robot/get` reachable
// from somewhere else would not be that.
func (t *Tree) Drop(path string) error {
	old, ok := t.cmds[path]
	if !ok {
		return fmt.Errorf("pdcmd: %s: nothing to drop", path)
	}

	delete(t.cmds, path)

	entity, _, isVerb := strings.Cut(path, "/")
	if !isVerb {
		if i := indexOf(t.order, entity); i >= 0 {
			t.order = append(t.order[:i], t.order[i+1:]...)
		}
		for k := range t.cmds {
			if strings.HasPrefix(k, entity+"/") {
				delete(t.cmds, k)
			}
		}

		return nil
	}

	parent, ok := t.cmds[entity]
	if !ok {
		return nil
	}

	for i, v := range parent.Commands {
		if v == old {
			parent.Commands = append(parent.Commands[:i], parent.Commands[i+1:]...)
			break
		}
	}

	return nil
}

func indexOf(vs []string, v string) int {
	for i, x := range vs {
		if x == v {
			return i
		}
	}

	return -1
}

// printer is what `-o` asked for.
func (r *runner) printer(cmd *xli.Command) (Printer, error) {
	return r.printerAs(cmd, true)
}

// printerAs is [runner.printer] for one answer of several.
//
// `header` is false for every answer after the first of a stream, and the only
// thing that reads it is a built-in table: a header between every event is a
// table nobody can read down a column of, and one printed once is what `-o
// table` means for a watch.
//
// A format an app supplied is handed back the same either way. What a stream
// means for it is the app's, and a [Printer] that had to be told would be an
// interface with two methods for what is one thing -- the reason it has one.
func (r *runner) printerAs(cmd *xli.Command, header bool) (Printer, error) {
	name, ok := flg.Find[string](cmd, "output")
	if !ok || name == "" {
		return r.byDefault(header), nil
	}

	if p, ok := r.opts.Printers[name]; ok {
		return p, nil
	}

	return builtinAs(name, header)
}

// byDefault is the format when nothing was asked for, and the only place a
// per-message renderer is consulted. See [Options.Renderers].
func (r *runner) byDefault(header bool) Printer {
	p, err := builtinAs(r.opts.def(), header)
	if err != nil {
		if v, ok := r.opts.Printers[r.opts.def()]; ok {
			p = v
		} else {
			p = ProtoText
		}
	}

	if len(r.opts.Renderers) == 0 {
		return p
	}

	return PrinterFunc(func(w io.Writer, m proto.Message) error {
		if v, ok := r.opts.Renderers[m.ProtoReflect().Descriptor().FullName()]; ok {
			return v.Print(w, m)
		}

		return p.Print(w, m)
	})
}

func builtin(name string) (Printer, error) { return builtinAs(name, true) }

// builtinAs is [builtin] told whether this is the first answer of several; only
// the tables have anything to do with it.
func builtinAs(name string, header bool) (Printer, error) {
	switch {
	case name == "pretty":
		return Pretty, nil
	case name == "prototext":
		return ProtoText, nil
	case name == "protojson":
		return ProtoJson, nil
	case name == "json":
		return Json, nil
	case name == "name":
		return Name, nil
	case name == "table":
		return table(false, header), nil
	case name == "wide":
		return table(true, header), nil
	case strings.HasPrefix(name, "template="):
		return Template(strings.TrimPrefix(name, "template="))
	}

	return nil, fmt.Errorf("-o %s: not a format; one of pretty, json, prototext, protojson, name, table, wide, template=...", name)
}
