// Package pdcli is what `pd` does, kept out of `main` so that it can be tested
// by calling it rather than by running a binary and reading its output.
package pdcli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"
)

// Layout is where an app's parts are.
//
// Almost all of it is convention rather than configuration, and that is the
// point: a generator whose layout is configurable is one where two apps are
// laid out differently, and every piece of advice about either of them then has
// to begin by asking which.
//
// Nothing here is a flag. What is not convention is read from a file that
// already exists and that the app owns anyway -- the module path from `go.mod`,
// the workspace from `buf.yaml`, and where the messages go from the
// `go_package` the schema declares. So an app can move its generated Go without
// payday growing a way to be configured, and there is still one place that says
// where it is.
type Layout struct {
	// Root is the directory the app's generated code lands in -- the one with
	// `proto/` in it.
	Root string

	// Module is the Go import path of [Layout.Root]. It is where the ent
	// runtime, the generated servers and `go.mod` are, and everything named in
	// this file is relative to it.
	Module string

	// Pkg is the Go import path of the generated messages: the `go_package`
	// the app's own entities declare.
	//
	// It is read rather than decided, because it is already written down. An
	// app that wants its generated Go somewhere other than the module root --
	// `<module>/api`, say, so that `ls` at the top is the app rather than a
	// hundred `.pb.go` -- says so once in its schema, and everything follows
	// from that one line.
	//
	// **One** `go_package` for the whole app, and an app whose entities
	// disagree is refused rather than generated: two Go packages is two sets of
	// ent schemas, an ent schema cannot have an edge to one in another package,
	// and the tenant wall is an edge. The failure without the refusal is a wall
	// that is not there.
	//
	// It is [Layout.Module] when nothing says otherwise, which is what `pd new`
	// writes and what nearly every app will keep.
	Pkg string

	// PkgName is what the messages' Go package is called, when the schema said
	// so rather than leaving it to be the last segment of [Layout.Pkg].
	//
	// `option go_package = "<module>/api;thingpb"` is the form, and it is the
	// one way the directory and the name are different things -- files in
	// `api/`, written `thingpb.Thing`. Empty when nothing said.
	PkgName string

	// Work is the directory holding `buf.yaml`, which is where buf is run from
	// and what every path in a template is relative to. It is [Layout.Root] for
	// an app that is one module, and above it for a workspace of several.
	Work string
}

// The names under [Layout.Root]. They are constants rather than fields for the
// reason in [Layout]: an app that moved `proto/` somewhere else would be an app
// whose every instruction needs a footnote.
//
// There is **one** proto directory, and what is in it says which half it
// belongs to:
//
//   - `proto/<pkg>/thing.proto` is yours. So is anything else you write there.
//   - `proto/<pkg>/thing_svc.g.proto` is generated from it -- the same `.g.`
//     that marks generated Go.
//   - `proto/payday/` is payday's own entities, copied in whole. Every file
//     there is generated, which is why they do not need the suffix, and a
//     README written beside them says so. It is a file and not a comment at
//     the top of each: a comment there is a comment on the .proto, and protoc
//     hands those to every generator, so it came out at the top of the Go and
//     the TypeScript as well.
//   - `proto/ext/` is yours again: the overlays. It is excluded from the buf
//     module, because an overlay is a fragment rather than a file that
//     compiles.
//
// What used to be `proto.svc/` and `proto.pd/` is gone. Both were inputs to the
// merge and nothing else read them, so they are written to a temporary
// directory the way the generated Go already was.
const (
	DirProto = "proto"     // one directory: the schema, yours and generated
	DirExt   = "proto/ext" // overlays: what the app adds to what is generated
	DirEnt   = "internal/ent"
	DirBare  = "server/bare"
	DirPd_   = "server/pd"

	// DirTs is the app's TypeScript package, and DirTsGen is the part of it
	// `pd gen --ts` writes.
	//
	// Generated TypeScript lives inside the package rather than beside it
	// because it is imported by hand-written TypeScript in the same package,
	// and a `..` import across a package boundary is a thing every bundler has
	// its own opinion about.
	DirTs    = "ts"
	DirTsGen = "ts/gen"
)

// ErrNoApp is a directory that is not one payday generates for.
var ErrNoApp = errors.New("no payday app here")

// Discover works out the layout from `dir`.
//
// It walks up for the two files that cannot be guessed -- `go.mod` for the
// module path and `buf.yaml` for the workspace -- and refuses rather than
// guessing when either is missing. A `pd gen` that invented a module path would
// write it into every generated file, and the way that is found out is an
// import cycle three commands later.
func Discover(dir string) (Layout, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return Layout{}, err
	}

	if _, err := os.Stat(filepath.Join(root, DirProto)); err != nil {
		return Layout{}, fmt.Errorf("%w: %s holds no %s/", ErrNoApp, root, DirProto)
	}

	mod, modDir, err := findUp(root, "go.mod")
	if err != nil {
		return Layout{}, fmt.Errorf("%w: %w", ErrNoApp, err)
	}

	b, err := os.ReadFile(mod)
	if err != nil {
		return Layout{}, err
	}

	path := modfile.ModulePath(b)
	if path == "" {
		return Layout{}, fmt.Errorf("%s: names no module", mod)
	}

	// The import path of the app directory, which is the module path plus
	// however far below the module it sits.
	rel, err := filepath.Rel(modDir, root)
	if err != nil {
		return Layout{}, err
	}
	if rel != "." {
		path = path + "/" + filepath.ToSlash(rel)
	}

	_, work, err := findUp(root, "buf.yaml")
	if err != nil {
		return Layout{}, fmt.Errorf("%w: %w", ErrNoApp, err)
	}

	l := Layout{Root: root, Module: path, Work: work}
	if l.Pkg, l.PkgName, err = readPkg(l); err != nil {
		return Layout{}, err
	}

	return l, nil
}

// GoPackage is what the copied entities are made to declare: [Layout.Pkg], with
// the name after it when the app's own schema said one.
//
// They have to say exactly what the app's entities say. Two files in one
// directory declaring two different Go packages is a build failure, and the
// copies are the ones that would be wrong.
func (l Layout) GoPackage() string {
	if l.PkgName == "" {
		return l.Pkg
	}

	return l.Pkg + ";" + l.PkgName
}

// PkgDir is where the messages land under the app, and "." when that is the app
// itself.
func (l Layout) PkgDir() string {
	if l.Pkg == l.Module {
		return "."
	}

	return strings.TrimPrefix(l.Pkg, l.Module+"/")
}

// Up is the way back to the module root from the message package, as a path
// prefix: "" when they are the same, and "../" for each directory between.
//
// Everything except the messages is named relative to the module root, and
// every generator names its output relative to `go_package` -- so this is what
// the two are joined with. The generators join rather than concatenate, which
// is what makes a `..` in a plugin option a path and not nonsense.
func (l Layout) Up() string {
	if d := l.PkgDir(); d != "." {
		return strings.Repeat("../", strings.Count(d, "/")+1)
	}

	return ""
}

// goPkgAt is `option go_package = "..."` in a .proto.
var goPkgAt = regexp.MustCompile(`(?m)^option go_package\s*=\s*"([^"]*)"`)

// readPkg is the one `go_package` the app's own entities declare, taken apart
// into the import path and the package name after it.
//
// Only the app's: `proto/payday/` is written by the generation and says
// whatever it was last told, and `proto/ext/` is a fragment that may say
// nothing at all.
//
// The `path;name` form is split rather than refused, because it is the only way
// to say a directory and a package name that differ -- files in `api/`, written
// `thingpb.Thing`. Kept whole it would be a directory called `api;thingpb`,
// which nothing would look in: the generation would still work, since protoc
// reads the option itself, and `pd gen --check` would quietly stop watching the
// messages.
func readPkg(l Layout) (string, string, error) {
	var (
		pkg string
		at  string
	)

	err := filepath.WalkDir(l.Path(DirProto), func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			if p == l.Path(DirProto, "payday") || p == l.Path(DirExt) {
				return fs.SkipDir
			}

			return nil
		case !strings.HasSuffix(p, ".proto") || strings.HasSuffix(p, ".g.proto"):
			return nil
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		m := goPkgAt.FindSubmatch(b)
		if m == nil {
			return nil
		}

		v := string(m[1])
		switch {
		case pkg == "":
			pkg, at = v, p
		case pkg != v:
			return fmt.Errorf("this app declares two go_package:\n\n    %s\n      %s\n    %s\n      %s\n\n"+
				"It has to be one. Everything generated is one Go package because an ent schema "+
				"cannot have an edge to one in another, and the wall between tenants **is** an "+
				"edge -- so two packages is two sets of schemas and a wall that is not there",
				l.rel(at), pkg, l.rel(p), v)
		}

		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", "", err
	}
	if pkg == "" {
		// Nothing declared one, which is a schema with no entities of its own
		// yet. The module root is what `pd new` writes.
		return l.Module, "", nil
	}

	path, name, _ := strings.Cut(pkg, ";")
	if path != l.Module && !strings.HasPrefix(path, l.Module+"/") {
		return "", "", fmt.Errorf("%s declares go_package %q, which is not in this module (%s).\n\n"+
			"Everything generated lands where `go_package` says, and `pd gen` lays that over "+
			"the app -- so a path outside the module is a tree written somewhere nobody imports",
			l.rel(at), pkg, l.Module)
	}

	return path, name, nil
}

// rel is a path under the app as a person would type it, for a message.
func (l Layout) rel(p string) string {
	v, err := filepath.Rel(l.Root, p)
	if err != nil {
		return p
	}

	return filepath.ToSlash(v)
}

// Rel is a path under the app, written the way a buf template has to have it:
// relative to the workspace, with forward slashes.
func (l Layout) Rel(parts ...string) string {
	v, err := filepath.Rel(l.Work, filepath.Join(append([]string{l.Root}, parts...)...))
	if err != nil {
		// Both are absolute and rooted at the same volume, so this cannot
		// happen; a path that is wrong is better than a panic in a generator.
		return filepath.Join(parts...)
	}

	return filepath.ToSlash(v)
}

// Path is a path under the app, absolute, for everything that is not a buf
// template.
func (l Layout) Path(parts ...string) string {
	return filepath.Join(append([]string{l.Root}, parts...)...)
}

// findUp answers with the first `name` at or above `dir`, and the directory it
// was in.
func findUp(dir string, name string) (string, string, error) {
	for {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, dir, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", "", err
		}

		up := filepath.Dir(dir)
		if up == dir {
			return "", "", fmt.Errorf("no %s at or above %s", name, dir)
		}

		dir = up
	}
}

// SchemaDir is where payday's own entities are, for the copy that begins a
// generation.
//
// It is asked of the module graph rather than being a path written down, so an
// app generating against payday v2 copies v2's entities without anybody having
// edited a script. An app inside payday's own module is the one case where the
// answer is here.
func SchemaDir() (string, error) {
	if d, err := goList("-m", "-f", "{{.Dir}}", "github.com/lesomnus/payday"); err == nil {
		return filepath.Join(strings.TrimSpace(d), "schema", "payday"), nil
	}

	// Not a dependency, which means this is payday itself.
	d, err := goList("-m", "-f", "{{.Dir}}")
	if err != nil {
		return "", fmt.Errorf("payday is not a dependency here and this is not payday: %w", err)
	}

	return filepath.Join(strings.TrimSpace(d), "schema", "payday"), nil
}
