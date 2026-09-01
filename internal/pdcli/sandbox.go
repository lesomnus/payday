package pdcli

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// The sandbox half of the template, embedded separately from the rest.
//
// Separately because `pd new` must not write it. The sandbox is only worth
// having to somebody who is building the page as well, and it is not free to
// the rest: `wasm/main.go` imports `github.com/lesomnus/grpc-dgram`, which
// becomes a **direct** requirement of the app -- a module payday itself does
// not depend on. A backend-only app should not acquire one by scaffolding.
//
// `pd new` writes `ts/` to everybody and that is not the same imposition: an
// unused directory costs nothing, and an unused module requirement is in
// `go.mod`, in the sums, and in every audit of what this app depends on.
//
//go:embed all:template-sandbox
var sandboxTree embed.FS

// DirWasm is the second entry point: the app, built for a platform with no file
// system, no listener and no network.
const DirWasm = "wasm"

// Sandbox is `pd sandbox init` -- the files an app needs to run itself in a
// page, written once and then the app's.
//
// # Why it is `init` and not `add`
//
// There is one of these. `pd entity add` is `add` because an app has many
// entities and each is a separate thing to name; a second sandbox is not a
// thing to have, and a verb that suggested otherwise would be inviting a
// question with no answer.
type Sandbox struct {
	Layout Layout
}

// ErrSandbox is a working copy this cannot be written into.
type ErrSandbox struct {
	// What is wrong, and Fix is what to do about it when there is such a thing.
	What string
	Fix  string
}

func (e ErrSandbox) Error() string {
	if e.Fix == "" {
		return e.What
	}

	return e.What + "\n\n    " + strings.ReplaceAll(e.Fix, "\n", "\n    ")
}

// Init writes the sandbox and answers with what is left to type.
//
// What it will not do is edit a file somebody else wrote. `ts/vite.config.ts`
// is the one file here that already exists, and it is replaced only when it is
// still byte-for-byte what `pd new` left -- otherwise the two settings are
// reported for the app to add, which is the same rule `pd gen` holds to about
// hand-written Go.
func (s Sandbox) Init() ([]string, error) {
	l := s.Layout

	// The sandbox is the app's `cmd` package with a different transport under
	// it, so an app that moved or removed it has nothing for this to build on.
	// Refused with the reason rather than written and left to fail at `go
	// build`, where the message is about an import path.
	if _, err := os.Stat(l.Path("cmd")); err != nil {
		return nil, ErrSandbox{
			What: fmt.Sprintf("%s builds on cmd.Build, cmd.Config and cmd.Resolver, and there is no %s",
				DirWasm, l.Rel("cmd")),
			Fix: "the sandbox is this app's own server on a message port; there is nothing to serve without them",
		}
	}

	// Refused rather than merged, the same way `pd new` refuses a directory
	// that is not empty: what is there is somebody's, and a sandbox that has
	// been edited is the likeliest thing to be there.
	if _, err := os.Stat(l.Path(DirWasm)); err == nil {
		return nil, ErrSandbox{
			What: fmt.Sprintf("%s is already there, so this app has a sandbox", l.Rel(DirWasm)),
			Fix:  fmt.Sprintf("delete %s to have it written again", l.Rel(DirWasm)),
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	data := struct{ Module, Pkg, Name string }{
		Module: l.Module,
		Pkg:    l.Pkg,
		Name:   name(l.Module),
	}

	// The vite configuration is decided before anything is written, so that a
	// working copy this cannot finish is one nothing was written into.
	vite, viteWhy, err := s.vite()
	if err != nil {
		return nil, err
	}

	err = fs.WalkDir(sandboxTree, "template-sandbox", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		rel := strings.TrimPrefix(p, "template-sandbox/")
		if rel == "ts/vite.config.ts" && !vite {
			return nil
		}

		b, err := sandboxTree.ReadFile(p)
		if err != nil {
			return err
		}

		if strings.HasSuffix(rel, ".tmpl") {
			t, err := template.New(rel).Parse(string(b))
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}

			sb := &strings.Builder{}
			if err := t.Execute(sb, data); err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}

			b = []byte(sb.String())
			rel = strings.TrimSuffix(rel, ".tmpl")
		}

		out := l.Path(filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}

		return os.WriteFile(out, b, 0o644)
	})
	if err != nil {
		return nil, err
	}

	return s.steps(viteWhy), nil
}

// vite reports whether `ts/vite.config.ts` may be replaced, and what to say
// when it may not.
//
// The rule is the one thing that makes this safe to do at all: the file is
// replaced when it is still exactly what `pd new` wrote, and left alone
// otherwise. An app that has touched it has decided something, and string
// surgery on somebody's configuration is not a thing to do quietly.
func (s Sandbox) vite() (bool, string, error) {
	p := s.Layout.Path("ts", "vite.config.ts")

	have, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		// No page at all. The file is written, and whatever is missing around
		// it is the app's to fill in.
		return true, "", nil
	}
	if err != nil {
		return false, "", err
	}

	was, err := tree.ReadFile("template/ts/vite.config.ts")
	if err != nil {
		return false, "", err
	}

	if bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace(was)) {
		return true, "", nil
	}

	return false, fmt.Sprintf(`%s is not what `+"`pd new`"+` wrote, so it was left alone. Add both:

    optimizeDeps: { exclude: ['@lesomnus/grpc-dgram'] },
    server: {
        headers: {
            'Cross-Origin-Opener-Policy': 'same-origin',
            'Cross-Origin-Embedder-Policy': 'require-corp',
        },
    },

The first is because pre-bundling rewrites the module into `+"`.vite/deps/`"+`,
where the worker Url it builds resolves to nothing. The second is because
SQLite in a Worker needs a SharedArrayBuffer, which does not exist without
cross-origin isolation. `+"`pd doctor`"+` checks for both.`, s.Layout.Rel("ts", "vite.config.ts")), nil
}

// steps is what to type once the files are there.
//
// The build is not run here for the reason [New.Setup] is a flag: it fetches a
// module and writes 65Mb, and `pd sandbox init` should be able to write a tree
// without doing either.
func (s Sandbox) steps(viteWhy string) []string {
	vs := []string{
		"go get github.com/lesomnus/grpc-dgram",
		"cd ts && npm install @lesomnus/grpc-dgram sqlite3-wasm-go && cd ..",
		"",
		"# the JS half of the Go runtime, which is version-coupled to the",
		"# compiler that builds the module -- so it is copied and never vendored",
		`cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" ts/public/`,
		"GOOS=js GOARCH=wasm go build -o ts/public/app.wasm ./" + DirWasm,
	}

	if viteWhy != "" {
		vs = append(vs, "", "# "+strings.ReplaceAll(viteWhy, "\n", "\n# "))
	}

	return vs
}

// name is a module's last segment, folded the way [New] folds it.
func name(module string) string { return alias(path(module)) }
