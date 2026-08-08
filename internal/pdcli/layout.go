// Package pdcli is what `pd` does, kept out of `main` so that it can be tested
// by calling it rather than by running a binary and reading its output.
package pdcli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

// Layout is where an app's parts are.
//
// Almost all of it is convention rather than configuration, and that is the
// point: a generator whose layout is configurable is one where two apps are
// laid out differently, and every piece of advice about either of them then has
// to begin by asking which. What cannot be convention is the module path and
// where the buf workspace begins, and both of those are read from files that
// already exist.
type Layout struct {
	// Root is the directory the app's generated code lands in -- the one with
	// `proto/` in it.
	Root string

	// Module is the Go import path of [Layout.Root]. Everything generated
	// declares it, which is why an app is one `go_package` and not several:
	// the ent schemas of two packages cannot have an edge between them.
	Module string

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
//     there is generated, which is why they do not need the suffix, and each
//     one says so on its first line.
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

	return Layout{Root: root, Module: path, Work: work}, nil
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
