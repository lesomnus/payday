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

	for _, step := range []struct {
		what string
		do   func(context.Context) error
	}{
		{"payday's entities, and what this app added to them", g.entities},
		{"service contracts from the entities", g.contracts},
		{"merging what payday and the app add to them", g.merge},
		{"messages, servers, ent schema", g.code},
		{"the ent runtime", g.entRuntime},
	} {
		g.say("pd: %s", step.what)
		if err := step.do(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.what, err)
		}
	}

	return nil
}

func (g Gen) say(format string, vs ...any) {
	if g.Log == nil {
		return
	}

	fmt.Fprintf(g.Log, format+"\n", vs...)
}

// goPackage is the line every copied entity has rewritten.
var goPackage = regexp.MustCompile(`(?m)^option go_package = .*$`)

// entities copies payday's own entities into the app, merged with whatever the
// app added to them.
//
// They are copied rather than imported because everything generated from them
// has to be one set of types in one module: an ent schema in another package
// cannot have an edge to one here, and the wall is an edge. `go_package` is
// rewritten for the same reason.
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

	dst := filepath.Join(g.Out, DirProto, "payday")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}

	for _, v := range vs {
		name := filepath.Base(v)
		ext := g.Layout.Path(DirExt, "payday", strings.TrimSuffix(name, ".proto")+".ext.proto")

		var b []byte
		if _, err := os.Stat(ext); err == nil {
			g.say("  merge %s + %s", name, filepath.Base(ext))
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

		b = goPackage.ReplaceAll(b, []byte(fmt.Sprintf("option go_package = %q;", g.Layout.Module)))
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o644); err != nil {
			return err
		}
	}

	return nil
}

// contracts writes the service contracts and payday's additions to them.
func (g Gen) contracts(ctx context.Context) error {
	for _, d := range []string{DirSvc, DirPd} {
		if err := os.RemoveAll(filepath.Join(g.Out, d)); err != nil {
			return err
		}
	}

	// The previous run's contracts, which are inputs to this one if they are
	// left where buf can read them.
	for _, d := range []string{"app", "payday"} {
		vs, _ := filepath.Glob(filepath.Join(g.Out, DirProto, d, "*_svc.proto"))
		for _, v := range vs {
			if err := os.Remove(v); err != nil {
				return err
			}
		}
	}

	return g.buf(ctx, tmplContracts(g.Layout, g.Out))
}

// merge folds each contract together with what payday adds and with whatever
// the app wrote by hand, in that order.
//
// The app is last on purpose: payday's half is generated from the declaration
// and can be rewritten at any time, and the app's half is the only one a person
// typed. An overlay that redeclares a number payday owns is refused before any
// of this, in `pd gen`'s reading of the schema -- protobuf-merge takes the
// overlay's word, so `alias` would quietly become whatever it said.
func (g Gen) merge(ctx context.Context) error {
	root := filepath.Join(g.Out, DirSvc)

	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, "_svc.g.proto") {
			return err
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}

		stem := strings.TrimSuffix(rel, "_svc.g.proto")
		out := filepath.Join(g.Out, DirProto, stem+"_svc.proto")

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		for _, overlay := range []string{
			filepath.Join(g.Out, DirPd, stem+"_svc.pd.proto"),
			g.Layout.Path(DirExt, stem+"_svc.ext.proto"),
		} {
			if _, err := os.Stat(overlay); err != nil {
				continue
			}

			g.say("  merge %s + %s", rel, filepath.Base(overlay))

			// protobuf-merge takes files, so what has been merged so far has to
			// be one.
			tmp, err := writeTemp(b, "*.proto")
			if err != nil {
				return err
			}

			b, err = g.run(ctx, "go", "tool", "protobuf-merge", tmp, overlay)
			os.Remove(tmp)
			if err != nil {
				return err
			}
		}

		// The contract imports its neighbours by the name they had before they
		// were merged, and after this they are all `_svc.proto`.
		b = importSvc.ReplaceAll(b, []byte(`${1}${2}_svc.proto${3}`))

		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}

		return os.WriteFile(out, b, 0o644)
	})
}

var importSvc = regexp.MustCompile(`(import ")([^"]*)_svc\.g\.proto(";)`)

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
	for _, pat := range []string{"*.g.go", "*.pb.go", filepath.Join(DirEnt, "*.g.go")} {
		vs, _ := filepath.Glob(filepath.Join(g.Out, pat))
		for _, v := range vs {
			if err := os.Remove(v); err != nil {
				return err
			}
		}
	}

	// `module=` strips the prefix, so the staging tree is already rooted where
	// the app is: `robot.pb.go` and `ent/schema/robot.go` rather than the whole
	// import path spelled out as directories.
	return copyTree(stage, g.Out)
}

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

	cmd := exec.CommandContext(ctx, "buf", append([]string{"generate", "--template", p}, args...)...)
	cmd.Dir = g.Layout.Work

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("buf generate: %w\n%s", err, stderr.String())
	}

	return nil
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

func copyTree(src string, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
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

		return os.WriteFile(out, b, 0o644)
	})
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
	return g.buf(ctx, tmplTs(g.Layout, g.Out, plugin), "--include-imports")
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
