package pdcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Gen is one run of the generator.
type Gen struct {
	Layout Layout

	// Log is where the steps are said, and may be nil for a silent run.
	Log io.Writer

	// Out is where the generated Go lands, and is [Layout.Root] for a real
	// generation. `--check` points it at a directory nobody is looking at, and
	// that is the whole of how the check differs from the thing it checks --
	// there is no second implementation to drift.
	Out string
}

// Run generates everything an app has, in the order the pieces depend on one
// another.
//
// The order is the part worth reading and the reason this is not four commands
// in a README. A `List` is an RPC: it has to be in the service contract before
// the messages, the stubs, the ent schema and the servers are generated from
// that contract. So the declarations are read twice -- once to write the
// contract, once to write the code -- and a generation that ran the second pass
// without the first would produce a stack that compiles and has no List in it.
func (g Gen) Run(ctx context.Context) error {
	if g.Out == "" {
		g.Out = g.Layout.Root
	}

	// The two halves of a contract before they are merged: what the service
	// generator wrote, and what payday adds to it. Nothing but the merge reads
	// either, so they are temporary -- the same way the generated Go is staged.
	// They used to be `proto.svc/` and `proto.pd/` in the app, and a directory
	// nobody reads is one somebody eventually edits.
	r := run{Gen: g}
	for _, v := range []*string{&r.svc, &r.pd} {
		d, err := os.MkdirTemp("", "pd-gen-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(d)

		*v = d
	}

	for _, step := range []struct {
		what string
		do   func(context.Context) error
	}{
		{"payday's entities, and what this app added to them", r.entities},
		{"the buf dependencies, if nothing has pinned them yet", r.deps},
		{"service contracts from the entities", r.contracts},
		{"merging what payday and the app add to them", r.merge},
		{"messages, servers, ent schema", r.code},
		{"the ent runtime", r.entRuntime},
	} {
		g.say("pd: %s", step.what)
		if err := step.do(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.what, err)
		}
	}

	return nil
}

// run is one [Gen.Run], with the scratch space its steps hand to one another.
type run struct {
	Gen

	// svc and pd are the two halves of every contract, before they are merged.
	svc string
	pd  string
}

func (g Gen) say(format string, vs ...any) {
	if g.Log == nil {
		return
	}

	fmt.Fprintf(g.Log, format+"\n", vs...)
}

// goPackage is the line every copied entity has rewritten.
var goPackage = regexp.MustCompile(`(?m)^option go_package = .*$`)

// protoPackage is the `package` line of a copied entity, which is rewritten to
// the app's for the reason in [Layout.ProtoPkg]: a Tenant and a Holder are the
// app's own concepts, and a caller should not have to learn the name of the
// framework to say who they are.
var protoPackage = regexp.MustCompile(`(?m)^package .*$`)

// pdImport is one entity importing another, which moves with them. It does not
// match `payday.proto`, the options file, which does not move.
var pdImport = regexp.MustCompile(`(?m)^import "payday/([^"]+)";$`)

// entities copies payday's own entities into the app, merged with whatever the
// app added to them.
//
// They are copied rather than imported because everything generated from them
// has to be one set of types in one module: an ent schema in another package
// cannot have an edge to one here, and the wall is an edge. `go_package` is
// rewritten for the same reason.
//
// They keep their names -- `payday/tenant.proto` and not `.g.proto` -- because
// an app's own entity imports one by name, and a file a person writes should
// not have to spell a generated suffix. What says they are generated is the
// directory they are in, and the README written beside them.
func (g Gen) entities(ctx context.Context) error {
	src, err := SchemaDir()
	if err != nil {
		return err
	}

	vs, err := filepath.Glob(filepath.Join(src, "*.proto"))
	if err != nil {
		return err
	}
	if len(vs) == 0 {
		return fmt.Errorf("%s: payday ships no entities here", src)
	}

	dst := filepath.Join(g.Out, g.Layout.DirPd())
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}

	for _, v := range vs {
		name := filepath.Base(v)
		ext := g.Layout.Path(DirExt, "payday", strings.TrimSuffix(name, ".proto")+".ext.proto")

		var b []byte
		if _, err := os.Stat(ext); err == nil {
			g.say("  merge %s + %s", name, filepath.Base(ext))

			// The same refusal the contract merge makes, because it is the
			// same union: a file-level `features.` here lands on the whole
			// copied entity, payday's own fields included. The line is even
			// easier to carry into an entity overlay -- the entity file it is
			// copied from starts with one.
			if err := CheckOverlayFile(ext); err != nil {
				return err
			}

			b, err = g.run(ctx, "go", "tool", "protobuf-merge", v, ext)
			if err != nil {
				return err
			}
		} else {
			b, err = os.ReadFile(v)
			if err != nil {
				return err
			}
		}

		b = goPackage.ReplaceAll(b, []byte(fmt.Sprintf("option go_package = %q;", g.Layout.GoPackage())))
		b = protoPackage.ReplaceAll(b, []byte(fmt.Sprintf("package %s;", g.Layout.ProtoPkg)))

		// And what they import of each other, since they have moved with them.
		// `payday.proto` is deliberately not matched: it is the options file and
		// stays where it is, which is what keeps `payday.entity` one name in
		// every app rather than one per app.
		b = pdImport.ReplaceAll(b, []byte(
			fmt.Sprintf(`import "%s/payday/$1";`, filepath.ToSlash(g.Layout.ProtoPkg))))

		if err := os.WriteFile(filepath.Join(dst, name), b, 0o644); err != nil {
			return err
		}
	}

	return os.WriteFile(filepath.Join(dst, "README.md"), []byte(copied), 0o644)
}

// copied is what this directory says about itself, and it is a file beside the
// entities rather than a comment at the top of each of them.
//
// A comment there would be a comment on the .proto file, and protoc hands those
// to every generator: it came out at the top of the generated Go, the generated
// TypeScript, and anything else anybody ever generates from this schema, each
// one telling its reader to go and edit an overlay that has nothing to do with
// the file they have open.
const copied = `# Generated

Everything in this directory is written by ` + "`pd gen`" + `, and editing it lasts
until the next one.

- ` + "`*.proto`" + ` are payday's own entities, copied in whole. They are copied
  rather than imported because everything generated from them has to be one set
  of types in one Go package: an ent schema in another package cannot have an
  edge to one here, and the wall between tenants is an edge.
- ` + "`*_svc.g.proto`" + ` are the service contracts generated from them, the same
  as the ones beside your own entities.

To change what payday's entities hold, write an overlay in ` + "`" + DirExt + `/payday/` + "`" + `
-- one file per entity, named after it, and it is merged in here on the next
generation.
`

// contracts writes the service contracts and payday's additions to them, into
// the scratch directories the merge reads.
func (r run) contracts(ctx context.Context) error {
	// The previous run's contracts, which are inputs to this one if they are
	// left where buf can read them.
	vs, err := filepath.Glob(filepath.Join(r.Out, DirProto, "*", "*_svc.g.proto"))
	if err != nil {
		return err
	}
	for _, v := range vs {
		if err := os.Remove(v); err != nil {
			return err
		}
	}

	return r.buf(ctx, tmplContracts(r.Layout, r.svc, r.pd))
}

// merge folds each contract together with what payday adds and with whatever
// the app wrote by hand, in that order.
//
// The app is last on purpose: payday's half is generated from the declaration
// and can be rewritten at any time, and the app's half is the only one a person
// typed. An overlay that redeclares a number payday owns is refused before any
// of this, in `pd gen`'s reading of the schema -- protobuf-merge takes the
// overlay's word, so `alias` would quietly become whatever it said.
//
// The contract keeps the name it was generated under -- `_svc.g.proto` all the
// way through -- and that is not only cosmetic. It used to be renamed to
// `_svc.proto` on the way out, which meant a contract and an overlay referred
// to the same file by two names until that line ran, and the merge kept both
// imports. The first hand-written RPC in any app hit it. There is no rename
// now, so there is nothing to reconcile: what a contract imports is what is on
// disk, before the merge and after it.
func (r run) merge(ctx context.Context) error {
	return filepath.WalkDir(r.svc, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, "_svc.g.proto") {
			return err
		}

		rel, err := filepath.Rel(r.svc, p)
		if err != nil {
			return err
		}

		stem := strings.TrimSuffix(rel, "_svc.g.proto")
		out := filepath.Join(r.Out, DirProto, rel)

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		for _, overlay := range []string{
			filepath.Join(r.pd, stem+"_svc.pd.proto"),
			r.Layout.Path(DirExt, stem+"_svc.ext.proto"),
		} {
			if _, err := os.Stat(overlay); err != nil {
				continue
			}

			r.say("  merge %s + %s", rel, filepath.Base(overlay))

			if err := CheckOverlayFile(overlay); err != nil {
				return err
			}

			// protobuf-merge takes files, so what has been merged so far has to
			// be one.
			tmp, err := writeTemp(b, "*.proto")
			if err != nil {
				return err
			}

			b, err = r.run(ctx, "go", "tool", "protobuf-merge", tmp, overlay)
			os.Remove(tmp)
			if err != nil {
				return err
			}
		}

		// The one `go_package` this app has, stamped rather than trusted. The
		// service generator carries the option over from the entity and drops
		// the `;name` off it, so a contract said `.../pb` where the entity said
		// `.../pb;widgetpb` -- two names for one Go package, which protoc
		// refuses in a heap of messages naming files nobody wrote.
		//
		// It is the app's entities that say it and everything generated that
		// follows, which is the same rule the copies in `proto/<pkg>/payday/`
		// are held to.
		b = goPackage.ReplaceAll(b, []byte(fmt.Sprintf("option go_package = %q;", r.Layout.GoPackage())))

		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}

		return os.WriteFile(out, b, 0o644)
	})
}

// fileFeature is a `features.*` set at the top of a file, outside any message.
var fileFeature = regexp.MustCompile(`(?m)^option features\.[a-z_]+\s*=`)

// CheckOverlayFile refuses an overlay that changes the file's features.
//
// It is the one thing an overlay can say that is not about what it adds. The
// merge unions the options, so a `features.field_presence = IMPLICIT` copied
// from the entity file lands on the **whole contract** -- every field of every
// message in it, including the ones the generator wrote and the app has never
// read.
//
// What it costs is not obvious, which is why it is refused rather than
// documented. Here it happened to take `HasId` off an Add request and stop the
// build, which is the lucky version; on a field nothing calls `Has` on it would
// change only what "not set" means on the wire, and nothing would say so.
//
// An overlay adds messages and RPCs. What the file is is the contract's, and
// the contract is generated.
func CheckOverlayFile(p string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}

	m := fileFeature.FindIndex(b)
	if m == nil {
		return nil
	}

	// Only what is outside a message: inside one it is about that message and
	// is the app's to say.
	if bytes.Contains(b[:m[0]], []byte("message ")) {
		return nil
	}

	return fmt.Errorf(
		"%s: sets a file-level `features.` option, and the merge would put it on the whole "+
			"contract -- every field of every message in it, including the ones the generator "+
			"wrote.\n\n"+
			"    %s\n\n"+
			"Delete the line. An overlay adds messages and RPCs; what the file **is** belongs "+
			"to the contract, which is generated",
		filepath.Base(p), strings.TrimSpace(string(b[m[0]:m[1]]))+" ...")
}

// code writes the messages, the stubs, the query helpers, the ent schema, the
// servers, and what payday makes of the declarations.
func (g Gen) code(ctx context.Context) error {
	// Into a staging directory, because the plugins write a tree rooted at the
	// module path and what is wanted is that tree laid over the app.
	stage, err := os.MkdirTemp("", "pd-gen-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	if err := g.buf(ctx, tmplCode(g.Layout, stage)); err != nil {
		return err
	}

	// The generated files that are still there from last time and would not be
	// written again -- an entity removed from the schema leaves its server
	// behind, and a stack that still compiles against a row that is gone is the
	// kind of thing found at run time.
	for _, d := range []string{DirBare, DirPd_, filepath.Join(DirEnt, "schema")} {
		if err := os.RemoveAll(filepath.Join(g.Out, d)); err != nil {
			return err
		}
	}
	vs, _ := filepath.Glob(filepath.Join(g.Out, DirEnt, "*.g.go"))
	for _, v := range vs {
		if err := os.Remove(v); err != nil {
			return err
		}
	}

	// `module=` strips the prefix, so the staging tree is already rooted where
	// the app is: `robot.pb.go` and `internal/ent/schema/robot.go` rather than
	// the whole import path spelled out as directories.
	wrote, err := copyTree(stage, g.Out, unsuffix)
	if err != nil {
		return err
	}

	// And then the message package where it used to be. Not where it is: an app
	// that moved `go_package` from the module root to `api/` leaves a whole
	// second copy of every message behind, and it **compiles** -- two packages
	// of the same types, one of them stale, with nothing to say which the next
	// reader is looking at.
	return sweep(g.Out, wrote)
}

// sweep removes generated Go under `root` that this run did not write, `wrote`
// being what it did.
//
// It looks at `*.pb.go` and `*.g.go` outside the directories that were removed
// whole -- which is the message package and nothing else -- and only at files
// whose first line says a generator wrote them. So it is by what a file says
// about itself rather than by where it is, and a hand-written file would have
// to be named `.pb.go` **and** claim to be generated to be taken.
//
// What it is for is `go_package` moving. Everything else generated is in a
// directory payday owns and is deleted with it; the messages are the one thing
// that lands wherever the schema says, so the only way to know the old place is
// to look.
func sweep(root string, wrote map[string]bool) error {
	skip := map[string]bool{
		filepath.Join(root, DirProto): true,
		filepath.Join(root, DirTs):    true,
		filepath.Join(root, DirEnt):   true,
		filepath.Join(root, DirBare):  true,
		filepath.Join(root, DirPd_):   true,
	}

	var empty []string

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			if skip[p] || strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" {
				return fs.SkipDir
			}

			return nil
		case !strings.HasSuffix(p, ".pb.go") && !strings.HasSuffix(p, ".g.go"):
			return nil
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if wrote[rel] {
			return nil
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(b, []byte("// Code generated by")) {
			return nil
		}

		empty = append(empty, filepath.Dir(p))

		return os.Remove(p)
	})
	if err != nil {
		return err
	}

	// And the directory it was, when it held nothing else. An empty `api/` is
	// harmless and is still somebody opening it to find out why it is there.
	// [os.Remove] on a directory with anything in it fails, which is the check.
	for _, d := range empty {
		if d != root {
			os.Remove(d)
		}
	}

	return nil
}

// unsuffix takes the `.g` back off, and it is the whole of what keeps that
// marker inside `proto/`.
//
// A contract is `robot_svc.g.proto`, and every generator downstream names its
// output after the file it read -- so protoc-gen-go writes `robot_svc.g.pb.go`
// and protoc-gen-go-grpc writes `robot_svc.g_grpc.pb.go`. The marker means
// "generated from something you wrote", which is true of the contract and true
// of every one of those as well: carried along it says nothing and is merely in
// the way, and in TypeScript it ends up in an import somebody types.
//
// Only contracts are `.g.proto`, and every contract ends in `_svc`, so this is
// exact rather than a guess at what a `.g` in a name might have meant.
func unsuffix(name string) string { return strings.Replace(name, "_svc.g", "_svc", 1) }

// entRuntime runs ent over the schema that was just written.
//
// It is the one step that is not payday's own: ent generates its client, its
// predicates and its builders from the schema structs, and everything above
// reads those. It runs in the app's directory because that is where the module
// ent is generating into begins.
func (g Gen) entRuntime(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "go", "tool", "ent", "generate", "./"+DirEnt+"/schema",
		"--target", "./"+DirEnt,
		"--feature", "sql/modifier",
		"--feature", "sql/versioned-migration")
	cmd.Dir = g.Out

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ent generate: %w\n%s", err, stderr.String())
	}

	return nil
}

// deps pins the buf modules the schema imports, the first time.
//
// It is here, between payday's entities and the first `buf generate`, because
// that is the only moment it can be. An app's own schema names `Tenant`,
// so the workspace does not compile until [run.entities] has copied payday's
// entities in -- and `buf dep update` compiles the workspace before it writes
// the lock. Run it before a generation, as the obvious ordering suggests, and
// it fails on a tree that has never been generated: the first command a new app
// runs, refusing.
//
// So `pd gen` owns it, the way it owns the buf template. Only when nothing is
// pinned: a lock with entries in it is this app's pin and is not a thing a
// generation may quietly move, which also means `pd gen --check` in CI never
// reaches the network -- the lock is committed.
func (r run) deps(ctx context.Context) error {
	at := filepath.Join(r.Layout.Work, "buf.lock")

	b, err := os.ReadFile(at)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if bytes.Contains(b, []byte("deps:")) {
		return nil
	}

	r.say("  buf dep update")

	name, err := Buf(ctx)
	if err != nil {
		return err
	}
	if _, err := r.Gen.run(ctx, name, "dep", "update"); err != nil {
		return err
	}

	return nil
}

// buf runs one template.
//
// The template is payday's and is written for each run rather than kept in the
// app, which is the whole reason `pd gen` exists as a command instead of a page
// of instructions. `strategy: all` is the example: the default runs a plugin
// once per directory, and an entity whose tenant is declared in another one is
// then not a generation target when its own file is read. An app maintaining
// its own template gets that wrong once and generates a wall with a hole in it.
func (g Gen) buf(ctx context.Context, template string, args ...string) error {
	p, err := writeTemp([]byte(template), "*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(p)

	name, err := Buf(ctx)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, name, append([]string{"generate", "--template", p}, args...)...)
	cmd.Dir = g.Layout.Work

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buf generate: %w\n%s%s", err, stderr.String(), whyStale(stderr.Bytes()))
	}

	return nil
}

// staleOption is what buf says about a `(payday.entity)` written against a
// newer payday than the one the app has pinned.
var staleOption = regexp.MustCompile(`option \(payday\.entity\): field (\w+) not found`)

// whyStale explains that failure, because the message buf gives names the
// symptom and nothing else.
//
// The app's schema is copied in from **this** payday and the option it is
// written against comes from `buf.build/payday/payday`, so the two can be of
// different ages. `pd gen` writes `buf.lock` only when nothing has, which is
// right -- a pin is the app's to move -- and it means an app that has generated
// once stays where it was pinned however far payday travels afterwards.
//
// What comes out of that is `field own not found` on a file the app never
// wrote, naming a field it has never heard of. Nothing in it says the word
// "pin", and the file it points at is regenerated on every run, so the obvious
// readings are all wrong: a corrupt copy, a bad merge, a broken payday.
func whyStale(b []byte) string {
	m := staleOption.FindAllSubmatch(b, -1)
	if len(m) == 0 {
		return ""
	}

	seen := map[string]bool{}
	names := []string{}
	for _, v := range m {
		if k := string(v[1]); !seen[k] {
			seen[k], names = true, append(names, "`"+k+":`")
		}
	}

	return fmt.Sprintf(
		"\npayday's entities were copied in from the payday you are running, and they say %s -- "+
			"which the `payday.entity` option this app has pinned does not have.\n\n"+
			"    buf dep update\n\n"+
			"`pd gen` writes buf.lock only when nothing has, because a pin is yours to move.",
		strings.Join(names, ", "))
}

// run answers with what a command wrote to stdout, and with its stderr in the
// error when it fails -- which is where every generator says what was wrong.
func (g Gen) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = g.Layout.Work

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w\n%s", name, err, stderr.String())
	}

	return stdout.Bytes(), nil
}

func writeTemp(b []byte, pattern string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := f.Write(b); err != nil {
		os.Remove(f.Name())
		return "", err
	}

	return f.Name(), nil
}

// copyTree lays one tree over another, with `name` deciding what each file is
// called on the way, and answers with what it wrote as paths under `dst`.
func copyTree(src string, dst string, name func(string) string) (map[string]bool, error) {
	wrote := map[string]bool{}

	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel = filepath.Join(filepath.Dir(rel), name(filepath.Base(rel)))
		}

		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}

		wrote[rel] = true

		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		return nil, err
	}

	return wrote, nil
}

func goList(args ...string) (string, error) {
	out, err := exec.Command("go", append([]string{"list"}, args...)...).Output()
	if err != nil {
		return "", err
	}

	return string(out), nil
}

// Ts writes the TypeScript half: the messages and the service descriptors, as
// protobuf-es emits them.
//
// It is a step of its own and not part of [Gen.Run], because the two have
// different audiences. A backend-only app runs `pd gen`, has no node_modules,
// and must not have its generation fail over a plugin it was never going to
// use; an app with a page in front of it asks for this as well.
//
// What it does **not** generate is a client. protobuf-es v2 emits the service
// descriptors, and `@connectrpc/connect`'s `createClient` takes those directly
// -- so the client is a runtime function of a descriptor rather than a file per
// service, and there is nothing to keep in step. That is the same discipline
// §10.5 asks of the local store: generate the declaration, implement it once.
func (g Gen) Ts(ctx context.Context) error {
	if g.Out == "" {
		g.Out = g.Layout.Root
	}

	plugin, err := TsPlugin(g.Layout)
	if err != nil {
		return err
	}

	g.say("pd: typescript, with %s", plugin)

	// Removed rather than written over: a message taken out of the schema
	// leaves a file behind, and TypeScript that still imports it compiles until
	// somebody calls it.
	if err := os.RemoveAll(filepath.Join(g.Out, DirTsGen)); err != nil {
		return err
	}

	// `--include-imports`, which the Go passes do not need and this one cannot
	// do without. A Go plugin resolves `orm.proto` to a package the app already
	// depends on; TypeScript has no such thing, so a file the schema imports and
	// this does not generate is an import of a module that is not there. The
	// well-known types are the exception and protobuf-es skips them itself --
	// it ships them.
	if err := g.buf(ctx, tmplTs(g.Layout, g.Out, plugin), "--include-imports"); err != nil {
		return err
	}

	// And [unsuffix] here as well, with the difference that a TypeScript file
	// names the ones it imports: `robot_svc.g_pb.ts` is a name somebody types in
	// `ts/src`, so the specifiers move with the files.
	return renameTs(filepath.Join(g.Out, DirTsGen))
}

// tsImport is the `.g` inside a module specifier, which is the same substring
// that is in the file name and is replaced with it.
var tsImport = regexp.MustCompile(`_svc\.g(_pb\.js")`)

func renameTs(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		b = tsImport.ReplaceAll(b, []byte("_svc${1}"))
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}

		name := unsuffix(d.Name())
		if name == d.Name() {
			return nil
		}

		return os.Rename(p, filepath.Join(filepath.Dir(p), name))
	})
}

// ErrNoTsPlugin is a working copy with no protobuf-es to generate with.
var ErrNoTsPlugin = errors.New("protoc-gen-es is not installed")

// TsPlugin finds the protobuf-es plugin.
//
// It looks in the app's own TypeScript package first and then in the
// workspace's, which is the order an npm workspace resolves in, and refuses
// rather than falling back to `npx`: `npx` with nothing installed downloads
// whatever is newest, so a generation would silently use a different version of
// the plugin from the one the app's lockfile pins -- and the way that is found
// out is generated code that changed for no reason in somebody else's checkout.
func TsPlugin(l Layout) (string, error) {
	for _, d := range []string{l.Path(DirTs), l.Work, filepath.Join(l.Work, DirTs)} {
		p := filepath.Join(d, "node_modules", ".bin", "protoc-gen-es")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("%w: npm install --save-dev @bufbuild/protoc-gen-es, in %s",
		ErrNoTsPlugin, l.Rel(DirTs))
}
