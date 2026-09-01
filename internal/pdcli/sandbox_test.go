package pdcli_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// TestTheSandboxIsNotWrittenByNew, which is the whole reason it is a command.
//
// `wasm/main.go` makes `github.com/lesomnus/grpc-dgram` a direct requirement of
// the app -- a module payday itself does not depend on -- so an app that is
// only a server must not acquire one by scaffolding. This is the assertion that
// keeps the two apart, since the cost of getting it wrong is invisible: the
// tree still builds.
func TestTheSandboxIsNotWrittenByNew(t *testing.T) {
	x := require.New(t)

	dir := filepath.Join(t.TempDir(), "app")
	x.NoError((pdcli.New{Dir: dir, Module: "github.com/acme/thing"}).Write())

	_, err := os.Stat(filepath.Join(dir, "wasm"))
	x.True(os.IsNotExist(err), "pd new wrote a sandbox")

	for _, v := range []string{"src/sandbox.ts", "src/sandbox-worker.ts"} {
		_, err := os.Stat(filepath.Join(dir, "ts", v))
		x.True(os.IsNotExist(err), "pd new wrote %s", v)
	}
}

// scaffold is what `pd new` writes, discovered.
//
// A whole app rather than the two files [Discover] needs, because that is what
// `pd sandbox init` runs on and half of what it does is about the rest of it --
// whether there is a `cmd` to build on, and what `ts/vite.config.ts` says.
func scaffold(t *testing.T, x *require.Assertions) (string, pdcli.Layout) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "app")
	x.NoError((pdcli.New{Dir: dir, Module: "github.com/acme/thing"}).Write())

	l, err := pdcli.Discover(dir)
	x.NoError(err)

	return dir, l
}

// TestAnAppWithNoCmdIsRefused.
//
// The sandbox is this app's own `cmd` package with a different transport under
// it, so an app that moved or removed it has nothing for this to build on.
// Refused with the reason rather than written and left to fail at `go build`,
// where the message is about an import path and does not say what to do.
func TestAnAppWithNoCmdIsRefused(t *testing.T) {
	x := require.New(t)

	dir, l := scaffold(t, x)
	x.NoError(os.RemoveAll(filepath.Join(dir, "cmd")))

	_, err := (pdcli.Sandbox{Layout: l}).Init()
	x.ErrorContains(err, "cmd.Build")

	// And nothing was written, so a refusal leaves the working copy as it was.
	_, err = os.Stat(filepath.Join(dir, "wasm"))
	x.True(os.IsNotExist(err))
}

// TestTheSandboxIsGoThatParsesAndNamesThisApp.
//
// Parsed rather than compiled, for the reason
// [TestTheTemplateIsGoThatParses] gives: compiling it means a generation and a
// module graph. What is checked past parsing is the part a template gets wrong
// -- the import paths, which are the app's and not payday's.
func TestTheSandboxIsGoThatParsesAndNamesThisApp(t *testing.T) {
	x := require.New(t)

	dir, l := scaffold(t, x)

	steps, err := (pdcli.Sandbox{Layout: l}).Init()
	x.NoError(err)

	p := filepath.Join(dir, "wasm", "main.go")
	b, err := os.ReadFile(p)
	x.NoError(err)

	f, err := parser.ParseFile(token.NewFileSet(), p, b, parser.SkipObjectResolution)
	x.NoError(err)
	x.Equal("main", f.Name.Name)

	got := string(b)
	x.Contains(got, `app "github.com/acme/thing"`, "the generated messages")
	x.Contains(got, `"github.com/acme/thing/cmd"`, "the app's own wiring")
	x.NotContains(got, "apptest")

	// The build tag, without which `go build ./...` compiles a main that names
	// a driver only wasm has.
	x.Contains(got, "//go:build js && wasm")

	// And the two lines that must be in one file, in one file.
	w, err := os.ReadFile(filepath.Join(dir, "ts", "src", "sandbox-worker.ts"))
	x.NoError(err)
	x.Contains(string(w), "sqlite3-wasm-go")
	x.Contains(string(w), "@lesomnus/grpc-dgram/wasm/worker")

	// What is left to type. The wasm build is not run here for the reason
	// `pd new` does not run `go mod tidy`: it fetches a module and writes 65Mb.
	joined := strings.Join(steps, "\n")
	x.Contains(joined, "GOOS=js GOARCH=wasm go build")
	x.Contains(joined, "wasm_exec.js")
}

// TestASecondSandboxIsRefused. There is one of these, which is why the verb is
// `init`, and what is already there is somebody's.
func TestASecondSandboxIsRefused(t *testing.T) {
	x := require.New(t)

	_, l := scaffold(t, x)

	_, err := (pdcli.Sandbox{Layout: l}).Init()
	x.NoError(err)

	_, err = (pdcli.Sandbox{Layout: l}).Init()
	x.ErrorContains(err, "already there")
}

// TestAViteConfigSomebodyEditedIsLeftAlone.
//
// The one file here that already exists is the app's, and payday does not edit
// what a person wrote. Replaced when it is still exactly what `pd new` left,
// reported otherwise -- and the report has to carry both settings, because a
// page missing either fails in a way that does not name it.
func TestAViteConfigSomebodyEditedIsLeftAlone(t *testing.T) {
	x := require.New(t)

	dir, l := scaffold(t, x)

	p := filepath.Join(dir, "ts", "vite.config.ts")
	mine := "// mine\nexport default {}\n"
	x.NoError(os.WriteFile(p, []byte(mine), 0o644))

	steps, err := (pdcli.Sandbox{Layout: l}).Init()
	x.NoError(err)

	b, err := os.ReadFile(p)
	x.NoError(err)
	x.Equal(mine, string(b), "somebody's configuration was overwritten")

	joined := strings.Join(steps, "\n")
	x.Contains(joined, "optimizeDeps")
	x.Contains(joined, "Cross-Origin-Embedder-Policy")

	// And the rest was still written: a working copy this could not finish is
	// not one it half-finished.
	_, err = os.Stat(filepath.Join(dir, "wasm", "main.go"))
	x.NoError(err)
}

// TestAnUntouchedViteConfigIsReplaced, which is the other half of the same
// rule and the case nearly every app is in.
func TestAnUntouchedViteConfigIsReplaced(t *testing.T) {
	x := require.New(t)

	dir, l := scaffold(t, x)

	_, err := (pdcli.Sandbox{Layout: l}).Init()
	x.NoError(err)

	b, err := os.ReadFile(filepath.Join(dir, "ts", "vite.config.ts"))
	x.NoError(err)
	x.Contains(string(b), "optimizeDeps")
	x.Contains(string(b), "Cross-Origin-Opener-Policy")

	// The page still builds: what was there before is still there.
	x.Contains(string(b), "@vitejs/plugin-react")

	// And a demo build compresses. `pd doctor` does not check for this -- a
	// missing compression is a bigger download and not a failure that hides
	// its cause -- so this is the only thing that would notice it going.
	x.Contains(string(b), "brotli()")
}
