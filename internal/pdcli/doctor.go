package pdcli

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Finding is one thing about a working copy that will not do.
type Finding struct {
	// What is the thing that is wrong, in one line.
	What string

	// Fix is what to type, or what to add, when there is such a thing.
	Fix string

	// Fatal says a generation cannot run at all, as against a generation that
	// runs and produces something quietly worse.
	Fatal bool
}

func (f Finding) String() string {
	if f.Fix == "" {
		return f.What
	}

	return f.What + "\n\n    " + strings.ReplaceAll(f.Fix, "\n", "\n    ") + "\n"
}

// tools are what a generation shells out to.
//
// They are the app's own tool directives rather than payday's, and that is the
// one place `pd gen` cannot close the drift by writing the file itself: a
// plugin is a Go program built from the app's module graph, so its version is
// the app's to pin. What payday can do is say when one is missing -- which is
// this -- rather than letting `buf generate` fail with a message about an
// executable nobody has heard of.
// cli is payday's own command, which is a tool of the app for the same reason
// the plugins are: `go tool pd` builds it from this module's graph, so the
// version an app generates with is the version it pinned.
//
// It is kept out of [tools] because nothing shells out to it -- an app that is
// missing it cannot be running `pd doctor` to be told so.
const cli = "github.com/lesomnus/payday/cmd/pd"

var tools = []string{
	"google.golang.org/protobuf/cmd/protoc-gen-go",
	"google.golang.org/grpc/cmd/protoc-gen-go-grpc",
	"github.com/protobuf-orm/protobuf-merge",
	"github.com/protobuf-orm/protoc-gen-orm-service",
	"github.com/protobuf-orm/protoc-gen-orm-go",
	"github.com/protobuf-orm/protoc-gen-orm-ent",
	"github.com/lesomnus/payday/cmd/protoc-gen-pd",
	"entgo.io/ent/cmd/ent",
}

// deps are the buf modules an app's schema imports.
var deps = []string{
	"buf.build/payday/payday",
	"buf.build/orm/orm",
}

// Doctor looks at a working copy and says what would go wrong.
//
// Everything it reports is something that fails **later and further away** than
// where it was caused: a missing tool directive is a buf error about an
// executable, a missing buf dependency is a compile error about an unknown
// option, and a `strategy` left at the default is a wall with a hole in it and
// no error at all. Each of those is cheap to find here and expensive to find
// where it surfaces.
//
// An empty answer means a generation will run and mean what it says.
func Doctor(ctx context.Context, l Layout) []Finding {
	var vs []Finding

	if _, err := exec.LookPath("buf"); err != nil {
		vs = append(vs, Finding{
			What:  "buf is not on the PATH, and every generation is one",
			Fix:   "go install github.com/bufbuild/buf/cmd/buf@latest",
			Fatal: true,
		})
	}

	have, err := goTools(ctx, l.Root)
	if err != nil {
		vs = append(vs, Finding{What: fmt.Sprintf("cannot read this module's tools: %s", err), Fatal: true})
	} else {
		var missing []string
		for _, v := range tools {
			if !have[v] {
				missing = append(missing, v)
			}
		}
		if len(missing) > 0 {
			fix := &strings.Builder{}
			for _, v := range missing {
				fmt.Fprintf(fix, "go get -tool %s\n", v)
			}

			vs = append(vs, Finding{
				What:  fmt.Sprintf("%d of the generators this app is built with are not tools of it", len(missing)),
				Fix:   strings.TrimRight(fix.String(), "\n"),
				Fatal: true,
			})
		}
	}

	vs = append(vs, doctorBuf(l)...)
	vs = append(vs, doctorSchema(l)...)
	vs = append(vs, doctorLayers(l)...)

	return vs
}

// doctorLayers finds a layer that cannot be put in a transaction.
//
// `enttx.Rebind` asks for the [enttx.Binder] at run time -- `any(s).(Binder[S])`
// -- so a layer that does not answer `WithDriver` is not a compile error
// anywhere. It is `ErrNotBindable` the first time somebody opens a transaction,
// which is a batch or a multi-write RPC, which is not the day the layer was
// written.
//
// Embedding `Overlay` does not give it: `Overlay` embeds the generated `Server`
// interface, and `WithDriver` is not one of its methods -- deliberately, since a
// promoted one would rebind the layer **underneath** this one and answer with a
// stack this layer is not in. So the method has to be written, once per layer,
// and forgetting it looks exactly like remembering it.
//
// What it reads is the shape rather than the types: a struct embedding
// `Overlay` is a layer, and a `WithDriver` method on it is the answer. A method
// with the wrong signature reads as present here -- which is what the `var _`
// line in the fix is for, and why the fix carries it.
func doctorLayers(l Layout) []Finding {
	byDir := map[string]*layerPkg{}

	err := filepath.WalkDir(l.Work, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			// Generated, vendored, or not Go at all -- and `internal/ent` is
			// hundreds of files nobody writes a layer in.
			case "node_modules", "testdata", "vendor", ".git", "ent":
				return fs.SkipDir
			}

			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}

		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
		if err != nil {
			// Not doctor's to report: a file that does not parse fails at the
			// build, loudly, with a better message than this could give.
			return nil
		}

		dir := filepath.Dir(p)
		pkg := byDir[dir]
		if pkg == nil {
			pkg = &layerPkg{bound: map[string]bool{}}
			byDir[dir] = pkg
		}
		pkg.read(l, p, f)

		return nil
	})
	if err != nil {
		return nil
	}

	var vs []Finding
	for _, dir := range slices.Sorted(maps.Keys(byDir)) {
		pkg := byDir[dir]
		// `api.Server` where the layer said `api.Overlay`, and plain `Server`
		// where it embedded the generated package's own.
		srv := "Server"
		if pkg.api != "" {
			srv = pkg.api + ".Server"
		}

		for _, v := range pkg.layers {
			if pkg.bound[v.name] {
				continue
			}

			vs = append(vs, Finding{
				What: fmt.Sprintf("%s is a layer with no WithDriver, so a transaction will refuse the stack it is in", v.at),
				Fix: fmt.Sprintf(`func (s %[1]s) WithDriver(drv dialect.Driver) (%[2]s, error) {
	v, err := s.Next().WithDriver(drv)
	if err != nil {
		return nil, err
	}

	return New(v), nil
}

// And this, so that a signature that drifts is a compile error here rather
// than another refusal at run time.
var _ enttx.Binder[%[2]s] = %[1]s{}`, v.name, srv),
			})
		}
	}

	return vs
}

// layerPkg is one directory's worth: what looks like a layer, and what answers
// `WithDriver`.
type layerPkg struct {
	layers []layerDecl
	bound  map[string]bool

	// api is what the package calls the generated one, so the fix reads the way
	// the file around it does.
	api string
}

type layerDecl struct {
	name string
	at   string
}

func (p *layerPkg) read(l Layout, path string, f *ast.File) {
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil || len(d.Recv.List) == 0 || d.Name.Name != "WithDriver" {
				continue
			}
			if n := receiver(d.Recv.List[0].Type); n != "" {
				p.bound[n] = true
			}

		case *ast.GenDecl:
			for _, s := range d.Specs {
				s, ok := s.(*ast.TypeSpec)
				if !ok {
					continue
				}
				t, ok := s.Type.(*ast.StructType)
				if !ok || t.Fields == nil {
					continue
				}

				for _, fld := range t.Fields.List {
					// Embedded: a field with a type and no name.
					if len(fld.Names) > 0 {
						continue
					}

					switch e := fld.Type.(type) {
					case *ast.Ident:
						// `Overlay` -- the generated package's own layers.
						if e.Name != "Overlay" {
							continue
						}

					case *ast.SelectorExpr:
						// `api.Overlay`, which is what an app writes.
						if e.Sel.Name != "Overlay" {
							continue
						}
						if x, ok := e.X.(*ast.Ident); ok {
							p.api = x.Name
						}

					default:
						continue
					}

					p.layers = append(p.layers, layerDecl{
						name: s.Name.Name,
						at:   l.rel(path) + ": " + s.Name.Name,
					})
				}
			}
		}
	}
}

// receiver is the type a method is on, pointer or not.
func receiver(e ast.Expr) string {
	if v, ok := e.(*ast.StarExpr); ok {
		e = v.X
	}
	if v, ok := e.(*ast.Ident); ok {
		return v.Name
	}

	return ""
}

// doctorBuf reads the app's `buf.yaml`, which is the one buf file payday does
// not write.
//
// It stays the app's because it declares dependencies, and what an app's schema
// imports is the app's business. The templates are payday's for the opposite
// reason -- nothing in them is a choice; see [tmplCode].
func doctorBuf(l Layout) []Finding {
	p := filepath.Join(l.Work, "buf.yaml")
	b, err := os.ReadFile(p)
	if err != nil {
		return []Finding{{What: fmt.Sprintf("cannot read %s: %s", p, err), Fatal: true}}
	}

	var vs []Finding
	for _, d := range deps {
		if bytes.Contains(b, []byte(d)) {
			continue
		}

		vs = append(vs, Finding{
			What:  fmt.Sprintf("%s does not depend on %s, which every payday schema imports", p, d),
			Fix:   fmt.Sprintf("deps:\n  - %s", d),
			Fatal: true,
		})
	}

	if !bytes.Contains(b, []byte(l.Rel(DirProto))) && !bytes.Contains(b, []byte("./"+DirProto)) {
		vs = append(vs, Finding{
			What: fmt.Sprintf("%s declares no module at %s, so buf will not read the schema", p, l.Rel(DirProto)),
			Fix:  fmt.Sprintf("modules:\n  - path: %s", l.Rel(DirProto)),
		})
	}

	// The overlays live under the schema, so buf finds them, and an overlay is
	// not a file that compiles: it is a fragment naming messages that exist only
	// after the merge. Excluding them is the one line of `buf.yaml` that is
	// payday's shape rather than the app's choice.
	if _, err := os.Stat(l.Path(DirExt)); err == nil && !bytes.Contains(b, []byte(l.Rel(DirExt))) {
		vs = append(vs, Finding{
			What: fmt.Sprintf("%s does not exclude %s, and buf will try to compile the overlays there",
				p, l.Rel(DirExt)),
			Fix: fmt.Sprintf("modules:\n  - path: %s\n    excludes:\n      - %s",
				l.Rel(DirProto), l.Rel(DirExt)),
			Fatal: true,
		})
	}

	return vs
}

// doctorSchema reads the schema the way the generator does, so that everything
// `pd gen` refuses is refused here too -- and reported all at once rather than
// one per run.
//
// It is the same reading and not a second one. What it cannot do is find a
// **missing** overlay or a stale copy of payday's own entities, since those are
// only visible by generating; `pd gen --check` is for that.
func doctorSchema(l Layout) []Finding {
	var vs []Finding

	src, err := SchemaDir()
	if err != nil {
		return append(vs, Finding{What: err.Error(), Fatal: true})
	}

	mine, err := filepath.Glob(filepath.Join(src, "*.proto"))
	if err != nil || len(mine) == 0 {
		return append(vs, Finding{
			What:  fmt.Sprintf("payday ships no entities at %s", src),
			Fatal: true,
		})
	}

	// An overlay for something payday does not have is a file that is silently
	// never merged -- the likeliest cause being a typo in its name.
	ext, _ := filepath.Glob(filepath.Join(l.Path(DirExt, "payday"), "*.ext.proto"))
	for _, v := range ext {
		name := strings.TrimSuffix(filepath.Base(v), ".ext.proto") + ".proto"
		if _, err := os.Stat(filepath.Join(src, name)); err == nil {
			continue
		}

		var names []string
		for _, m := range mine {
			names = append(names, strings.TrimSuffix(filepath.Base(m), ".proto"))
		}

		vs = append(vs, Finding{
			What: fmt.Sprintf("%s extends a payday entity that does not exist, so it is never merged",
				l.Rel(DirExt, "payday", filepath.Base(v))),
			Fix: "payday has: " + strings.Join(names, ", "),
		})
	}

	return vs
}

// goTools is what `go tool` in this module answers, as a set.
func goTools(ctx context.Context, dir string) (map[string]bool, error) {
	cmd := exec.CommandContext(ctx, "go", "tool")
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}

	vs := map[string]bool{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if v := strings.TrimSpace(line); v != "" {
			vs[v] = true
		}
	}

	return vs, nil
}
