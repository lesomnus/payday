package pdcmd

import (
	"errors"
	"fmt"
	"io"
	"slices"
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
// The whole tree, put where the app wants it -- its root, or a command of its
// own -- which is the whole of what an app has to write:
//
//	t, err := pdcmd.New(to)
//	err = t.Mount(root)
//
// One command of it, to mount somewhere else or to wrap:
//
//	t.Command("robot/list")
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
// # The words a command answers to
//
// A command is called what the schema calls it: an entity's group is the
// entity's name and a verb is the RPC's, `list` for `List`. A word that runs
// one as well is an alias -- `ls` for `list` is this package's own -- and an
// app gives one more with [Tree.Alias], and keeps a command out of the help
// with [Tree.Hide]:
//
//	t.Alias("tenant/configure", "conf", "config")
//	t.Hide("fact")
//
// A path is read the way the command line is, by name or by alias at each
// step, so `t.Command("robot/ls")` is the `list` that `app robot ls` runs.
//
// No two commands at one level answer to the same word. xli runs the first
// command a word matches and says nothing about the second, so a word that
// meant two would be a command that silently never runs -- which is the
// mistake [Tree.Replace] refuses a typo for. So it is refused at the call that
// would bring it in: [Tree.Alias], [Tree.Add], [Tree.Replace], and
// [Tree.Mount] for the commands the app has beside the tree.
//
// # What is not built
//
// `Apply`. It is one of payday's two general writes and is closed unless an app
// opted in, so a command for it would fail on every app that took the default,
// and an app that did opt in means something particular by it -- which is a
// command it writes and mounts beside these.
//
// `Watch` **is** built, on the entities that declared one, the same way `list`
// is built only where there is a `List`. It was left out for a while as "a
// stream, and a stream is not this shape"; what was actually missing was an
// opinion about what a stream ending means, and [cmdWatch] is that opinion.
type Tree struct {
	run  *runner
	opts Options
	pkg  protoreflect.FullName

	// top is the tree itself, in the order it is shown: a group for each
	// entity, and whatever the app added beside them.
	//
	// A path is found by walking it rather than by looking it up in an index
	// beside it. There was one, keyed by the path's spelling, and it could not
	// hold an alias without holding every spelling of every path -- and it
	// drifted anyway: replacing an entity's group left the old group's verbs
	// in it, answering to paths the command line no longer reached.
	top xli.Commands

	// mounted is where [Tree.Mount] put the tree. Nothing changes it after
	// that; see Mount.
	mounted *xli.Command
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

			// Named here, from the table, rather than by each builder: what a
			// verb is called is one list to read, and the aliases are in it.
			c.Name = v.name
			c.Aliases = slices.Clone(v.aliases)

			vs = append(vs, c)
		}

		if len(vs) == 0 {
			// An entity with no served contract at all. It is stored and it is
			// real; there is simply nothing to call about it.
			continue
		}

		// Unique without checking: `pd gen` refuses two entities written the
		// same, since that is also the `#word` of a slug.
		t.top = append(t.top, &xli.Command{
			Name:     e.Name,
			Brief:    fmt.Sprintf("manage %s", e.Message.Name()),
			Commands: vs,
			Handler:  xli.RequireSubcommand(),
		})
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
//
// [Tree.Mount] is that append and the check it cannot make: whether a word of
// the tree is already one of the commands it lands beside. This is for an app
// that puts the commands somewhere Mount does not reach, and it checks
// nothing.
func (t *Tree) Commands() xli.Commands {
	return slices.Clone(t.top)
}

// Command answers with one command by path -- "robot" or "robot/list" -- or nil.
//
// Each step of the path is a name or an alias, as it is on the command line:
// "robot/ls" is the same command.
//
// The nil is usable: asking for "cell/list" on an entity that declared no
// `list:` answers nothing, which is the same answer the tree gives.
func (t *Tree) Command(path string) *xli.Command {
	_, c := t.find(path)
	return c
}

// Replace swaps one command for the app's own.
//
// It refuses a path that is not there rather than adding it, because a typo in
// a path would otherwise be a command that silently never runs -- and a typo
// and a deliberate addition look identical at the call site. [Tree.Add] is how
// a new one goes in.
//
// The replacement takes the place as it was: its name, its aliases, and
// whether it is hidden.
func (t *Tree) Replace(path string, c *xli.Command) error {
	if err := t.mayChange(path); err != nil {
		return err
	}

	group, old := t.find(path)
	if old == nil {
		return fmt.Errorf("pdcmd: %s: nothing to replace", path)
	}

	level := t.level(group)

	// What the place had is kept, rather than taken from the replacement: the
	// name, so that `Replace("robot/ls", c)` puts `c` where `list` was even
	// when `c` is called something else, and the aliases and whether it is
	// hidden, for the same reason. A replacement that also renamed it, or
	// took a word away, would be two changes in one call. Whatever words `c`
	// brings of its own are added, and held to the rule every word is.
	for _, w := range c.Aliases {
		if err := word(w); err != nil {
			return fmt.Errorf("pdcmd: %s: %w", path, err)
		}
		if u := answers(level, w, old); u != nil {
			return refused(path, w, group, u)
		}
	}

	i := slices.Index(level, old)
	if i < 0 {
		return fmt.Errorf("pdcmd: %s: not where it was found", path)
	}

	c.Name = old.Name
	c.Aliases = union(old.Aliases, c.Aliases)
	c.Hidden = c.Hidden || old.Hidden
	level[i] = c

	return nil
}

// Add puts a command into an entity's group, or a new group at the top.
//
// The command is called what the path says, and keeps the aliases it was
// built with. Refused, as [Tree.Alias] refuses one, when the name or an alias
// is already a word at that level: `Add("robot/ls", c)` is refused because
// `ls` runs `list`, and `c` would be a command nothing ever reached.
func (t *Tree) Add(path string, c *xli.Command) error {
	if err := t.mayChange(path); err != nil {
		return err
	}

	entity, verb, isVerb := strings.Cut(path, "/")

	var group *xli.Command
	name := entity
	if isVerb {
		group = t.top.Get(entity)
		if group == nil {
			return fmt.Errorf("pdcmd: %s: %s is not here", path, entity)
		}

		name = verb
	}

	level := t.level(group)
	for _, w := range append([]string{name}, c.Aliases...) {
		if err := word(w); err != nil {
			return fmt.Errorf("pdcmd: %s: %w", path, err)
		}
		if u := answers(level, w, c); u != nil {
			if w == name && u.Name == name {
				return fmt.Errorf("pdcmd: %s: already here", path)
			}

			return refused(path, w, group, u)
		}
	}

	c.Name = name
	if group == nil {
		t.top = append(t.top, c)
	} else {
		group.Commands = append(group.Commands, c)
	}

	return nil
}

// Drop removes a command, which is how a tree is narrowed to a subset.
//
// Dropping an entity drops its verbs with it: what is being said is "this
// deployment does not serve robots here", and leaving `robot/get` reachable
// from somewhere else would not be that.
//
// It is not [Tree.Hide]. A dropped command is gone; a hidden one is there and
// not shown.
func (t *Tree) Drop(path string) error {
	if err := t.mayChange(path); err != nil {
		return err
	}

	group, old := t.find(path)
	if old == nil {
		return fmt.Errorf("pdcmd: %s: nothing to drop", path)
	}

	level := slices.DeleteFunc(t.level(group), func(c *xli.Command) bool { return c == old })
	if group == nil {
		t.top = level
	} else {
		group.Commands = level
	}

	return nil
}

// Alias gives the command at path more words that run it, beside its name.
//
//	t.Alias("tenant", "workspace", "ws")
//	t.Alias("tenant/configure", "conf", "config")
//
// The path may be a command this package built or one the app added, so an
// alias of a method's verb is said after the [Tree.Add] that put it there. A
// word the command already answers to is nothing to add, and is not an error.
//
// # What is refused
//
// A path that is not there, and a word another command at the same level
// already answers to, by its name or by an alias of its own. Either would be
// a word that does nothing, the second without saying so; see [Tree]. And a
// word that cannot be typed as one: empty, holding a space or a "/", or
// beginning with "-", which is read as a flag. Nothing is added unless every
// word is.
//
// # Why it is said here and not in the schema
//
// What a command answers to is this app's command line, and nobody else
// reads it. The schema is the contract every client is generated from -- the
// servers, the TypeScript, what is pushed to the registry -- and none of them
// has any use for `ws`: an alias there would be one consumer's vocabulary in
// everybody's contract. The entity an app most wants a word for is as often
// payday's as its own, too. `Tenant` is declared in payday's protos and copied
// in whole, so a schema option on it is not the app's to set, and an app that
// calls its tenants workspaces and one that calls them directories want
// different words for the same message.
//
// And the check could not be made there. The words that collide are the ones
// at the level the tree is mounted on, beside the app's own `version`,
// `config` and whatever else it wrote, and `pd gen` never sees those. The tree
// does, at [Tree.Mount].
//
// It is not the slug's either. `#word` in a reference is the schema's name and
// nothing else, so an alias is a word for typing a command and not a second
// name for the entity anywhere.
//
// A method, and not a field of [Options], because what an app most often
// gives a word to is a verb it added itself -- a method of its own, mounted
// with [Tree.Unary] -- and that is not in the tree yet when [New] reads its
// options. A field would check what it could at New and leave the rest
// unchecked, or wait for a moment that says the tree is finished, and only
// [Tree.Mount] says that.
func (t *Tree) Alias(path string, words ...string) error {
	if err := t.mayChange(path); err != nil {
		return err
	}
	if len(words) == 0 {
		return fmt.Errorf("pdcmd: %s: no alias to give", path)
	}

	group, c := t.find(path)
	if c == nil {
		return fmt.Errorf("pdcmd: %s: nothing to alias", path)
	}

	level := t.level(group)
	add := []string{}
	for _, w := range words {
		if err := word(w); err != nil {
			return fmt.Errorf("pdcmd: %s: %w", path, err)
		}
		if c.Name == w || slices.Contains(c.Aliases, w) || slices.Contains(add, w) {
			continue
		}
		if u := answers(level, w, c); u != nil {
			return refused(path, w, group, u)
		}

		add = append(add, w)
	}

	c.Aliases = append(c.Aliases, add...)
	return nil
}

// Hide keeps the command at path out of the help -- `--help`, `--help-all`,
// what an AI agent's `--help` prints -- out of the shell's completion and out
// of the documentation xli generates, and leaves it running by name.
//
//	t.Hide("fact")
//
// It is for the rows an app keeps that other commands write: the facts an
// operation records, the allocation a reservation makes. They are real and
// they can be read, so their commands are kept -- `app fact list` is how
// somebody looking into a reservation reads what it wrote, with the paging,
// `-o` and the reference argument every other list has, which spelling the
// request out to a generic call would not be. What they are not is something
// a person reading `app --help` came for.
//
// So hidden is that and no more. The commands run, the verbs below a hidden
// group are listed in its own help, and whether a call is allowed is the
// server's to say as it is of every other. [Tree.Drop] is the other answer,
// for what this deployment does not serve at all.
func (t *Tree) Hide(path string) error {
	if err := t.mayChange(path); err != nil {
		return err
	}

	_, c := t.find(path)
	if c == nil {
		return fmt.Errorf("pdcmd: %s: nothing to hide", path)
	}

	c.Hidden = true
	return nil
}

// Mount puts the tree on `at` -- the app's root, or a command of its own --
// and refuses, before anything is moved, a word of the tree that already runs
// one of the commands there.
//
//	root := &xli.Command{Name: "app", Commands: xli.Commands{version, config, serve}}
//	if err := t.Mount(root); err != nil {
//		return err
//	}
//
// It is [Tree.Commands] appended to `at.Commands`, plus the one check the
// append cannot make. At the root an entity sits beside `version`, `config`
// and whatever the app wrote itself, and an entity or an alias that is also
// one of those is a command xli never runs. So Mount last, once `at` has the
// rest of what it holds: a command put on it afterwards is beside the tree
// without having been held to it.
//
// The tree is not changed after it. A group added to the tree once it is
// mounted would be in the tree and not on `at` -- a command that silently
// never appears -- so every method that would change it refuses instead.
//
// # Wherever it is put
//
// Nothing here names what the tree is mounted under. The usage line and the
// name in the help are xli's, read off the path the command was run by, and
// no brief or example here spells a parent -- so the same tree reads as `app
// robot list` mounted at the root and `app api robot list` under a command of
// the app's called `api`.
func (t *Tree) Mount(at *xli.Command) error {
	if at == nil {
		return errors.New("pdcmd: mount: on nothing")
	}
	if t.mounted != nil {
		return fmt.Errorf("pdcmd: mount on %s: already mounted on %s", at.Name, t.mounted.Name)
	}

	if err := t.check(); err != nil {
		return err
	}
	for _, c := range t.top {
		for _, w := range append([]string{c.Name}, c.Aliases...) {
			if u := answers(at.Commands, w, nil); u != nil {
				return fmt.Errorf("pdcmd: mount on %s: %q already runs %s there, and is the tree's %s",
					at.Name, w, said(u, w), said(c, w))
			}
		}
	}

	at.Commands = append(at.Commands, t.top...)
	t.mounted = at

	return nil
}

// mayChange says whether the tree may still be changed; see [Tree.Mount].
func (t *Tree) mayChange(path string) error {
	if t.mounted == nil {
		return nil
	}

	return fmt.Errorf("pdcmd: %s: the tree is mounted on %s, so it is changed before Mount and not after", path, t.mounted.Name)
}

// check holds every level of the tree to the rule each change to it was held
// to, once more before it is mounted. `Aliases` is a field anybody can set on
// a command they hold -- one the app added, or one [Tree.Command] answered
// with -- and a word set that way went past every method here.
func (t *Tree) check() error {
	if err := distinct(nil, t.top); err != nil {
		return err
	}
	for _, g := range t.top {
		if err := distinct(g, g.Commands); err != nil {
			return err
		}
	}

	return nil
}

// distinct refuses a word two commands of one level answer to, and a word
// that is not one.
func distinct(group *xli.Command, level xli.Commands) error {
	seen := map[string]*xli.Command{}
	for _, c := range level {
		path := c.Name
		if group != nil {
			path = group.Name + "/" + path
		}

		for _, w := range append([]string{c.Name}, c.Aliases...) {
			if err := word(w); err != nil {
				return fmt.Errorf("pdcmd: %s: %w", path, err)
			}
			if u, ok := seen[w]; ok && u != c {
				return refused(path, w, group, u)
			}

			seen[w] = c
		}
	}

	return nil
}

// find is the command a path names, and the group it is in -- nil for a
// command at the top. Each step is read the way the command line reads a
// word: by name or by alias, the first that answers. There is only ever one,
// which is what every change to the tree holds it to.
func (t *Tree) find(path string) (group, c *xli.Command) {
	entity, verb, isVerb := strings.Cut(path, "/")

	top := t.top.Get(entity)
	if !isVerb || top == nil {
		return nil, top
	}

	return top, top.Commands.Get(verb)
}

// level is the commands beside one another: the group's, or the top's when
// there is no group.
func (t *Tree) level(group *xli.Command) xli.Commands {
	if group == nil {
		return t.top
	}

	return group.Commands
}

// answers is the command at this level that already runs on the word `w`, by
// its name or by an alias, other than `self`; nil when there is none.
func answers(level xli.Commands, w string, self *xli.Command) *xli.Command {
	for _, c := range level {
		if c == self {
			continue
		}
		if c.Name == w || slices.Contains(c.Aliases, w) {
			return c
		}
	}

	return nil
}

// refused is the error for a word `w`, wanted at path, that `u` already
// answers to.
func refused(path, w string, group, u *xli.Command) error {
	at := said(u, w)
	if group != nil {
		at = group.Name + "/" + at
	}

	return fmt.Errorf("pdcmd: %s: %q already runs %s", path, w, at)
}

// said is how `c` answers to `w`, for a refusal to say: by its name, or as an
// alias of it.
func said(c *xli.Command, w string) string {
	if c.Name == w {
		return c.Name
	}

	return c.Name + ", as an alias"
}

// word refuses what cannot be typed as one word of a command line. A name is
// held to it as well as an alias, since the two are read the same way.
func word(w string) error {
	switch {
	case w == "":
		return errors.New("an empty word")
	case strings.HasPrefix(w, "-"):
		return fmt.Errorf("%q: begins with -, which is read as a flag", w)
	case strings.ContainsAny(w, "/ \t\n"):
		return fmt.Errorf("%q: is not one word", w)
	}

	return nil
}

// union is `a`, and whatever of `b` it does not already hold.
func union(a, b []string) []string {
	vs := slices.Clone(a)
	for _, v := range b {
		if !slices.Contains(vs, v) {
			vs = append(vs, v)
		}
	}

	return vs
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
