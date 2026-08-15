package pdcli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// found is what `pd doctor` said, joined, so a test can ask whether a thing was
// reported without caring which finding carried it.
func found(t *testing.T, l pdcli.Layout) string {
	t.Helper()

	vs := pdcli.Doctor(t.Context(), l)

	b := &strings.Builder{}
	for _, v := range vs {
		b.WriteString(v.String())
		b.WriteString("\n")
	}

	return b.String()
}

// sandboxed is an app with a sandbox in it, and `wasm_exec.js` where the page
// serves it -- which is the state `pd sandbox init` plus its printed steps
// leave behind.
func sandboxed(t *testing.T, x *require.Assertions) (string, pdcli.Layout) {
	t.Helper()

	dir, l := scaffold(t, x)

	_, err := (pdcli.Sandbox{Layout: l}).Init()
	x.NoError(err)

	out, err := exec.CommandContext(t.Context(), "go", "env", "GOROOT").Output()
	x.NoError(err)

	b, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(out)), "lib", "wasm", "wasm_exec.js"))
	x.NoError(err)

	x.NoError(os.MkdirAll(filepath.Join(dir, "ts", "public"), 0o755))
	x.NoError(os.WriteFile(filepath.Join(dir, "ts", "public", "wasm_exec.js"), b, 0o644))

	return dir, l
}

// TestAnAppWithNoSandboxIsNotAskedAboutOne, which is most apps.
//
// The checks below are about files that only exist for a sandbox, and reporting
// them to somebody who never asked for one would be `pd doctor` telling every
// backend app that it is missing something it does not want.
func TestAnAppWithNoSandboxIsNotAskedAboutOne(t *testing.T) {
	x := require.New(t)

	_, l := scaffold(t, x)

	got := found(t, l)
	x.NotContains(got, "wasm_exec.js")
	x.NotContains(got, "sandbox-worker")
	x.NotContains(got, "Cross-Origin")
}

// TestASandboxThatWasWrittenIsClean, which is the assertion that makes the four
// below mean anything: a fresh `pd sandbox init` reports nothing, so what they
// report is what was broken and not a check that always fires.
func TestASandboxThatWasWrittenIsClean(t *testing.T) {
	x := require.New(t)

	_, l := sandboxed(t, x)

	got := found(t, l)
	x.NotContains(got, "wasm_exec.js")
	x.NotContains(got, "sandbox-worker")
	x.NotContains(got, "Cross-Origin")
	x.NotContains(got, "pre-bundled")
}

// TestTheFourWaysASandboxGoesQuietlyWrong.
//
// Every one of these compiles, links and serves. What each produces is a
// failure that does not name its cause -- which is the only test for whether it
// belongs in `pd doctor` at all.
func TestTheFourWaysASandboxGoesQuietlyWrong(t *testing.T) {
	t.Run("no cross-origin isolation", func(t *testing.T) {
		x := require.New(t)
		dir, l := sandboxed(t, x)

		// The configuration `pd new` writes, put back: it is a perfectly good
		// vite config and it is missing both settings.
		x.NoError(os.WriteFile(filepath.Join(dir, "ts", "vite.config.ts"),
			[]byte("import { defineConfig } from 'vite'\n\nexport default defineConfig({})\n"), 0o644))

		got := found(t, l)
		x.Contains(got, "SharedArrayBuffer does not exist")
		x.Contains(got, "Cross-Origin-Embedder-Policy")
	})

	t.Run("pre-bundled", func(t *testing.T) {
		x := require.New(t)
		dir, l := sandboxed(t, x)

		// The headers kept, the exclusion dropped -- which is the half that is
		// easy to lose, since the page works until the worker starts.
		x.NoError(os.WriteFile(filepath.Join(dir, "ts", "vite.config.ts"), []byte(
			`export default { server: { headers: { 'Cross-Origin-Embedder-Policy': 'require-corp' } } }`), 0o644))

		got := found(t, l)
		x.Contains(got, ".vite/deps/")
		x.Contains(got, "optimizeDeps")
	})

	t.Run("the worker split in two", func(t *testing.T) {
		x := require.New(t)
		dir, l := sandboxed(t, x)

		// Only one of the two imports, which is what moving the driver to the
		// page looks like from here.
		x.NoError(os.WriteFile(filepath.Join(dir, "ts", "src", "sandbox-worker.ts"),
			[]byte("import '@lesomnus/grpc-dgram/wasm/worker'\n"), 0o644))

		got := found(t, l)
		x.Contains(got, "one realm")
	})

	t.Run("a wasm_exec.js from another toolchain", func(t *testing.T) {
		x := require.New(t)
		dir, l := sandboxed(t, x)

		p := filepath.Join(dir, "ts", "public", "wasm_exec.js")
		b, err := os.ReadFile(p)
		x.NoError(err)

		// One byte, which is all it takes: the file carries no version, so
		// nothing but the bytes says whether it is the right one.
		x.NoError(os.WriteFile(p, append(b, '\n'), 0o644))

		got := found(t, l)
		x.Contains(got, "not the one this Go ships")
	})

	t.Run("no wasm_exec.js at all", func(t *testing.T) {
		x := require.New(t)
		dir, l := sandboxed(t, x)

		x.NoError(os.Remove(filepath.Join(dir, "ts", "public", "wasm_exec.js")))

		got := found(t, l)
		x.Contains(got, "is not there")
		x.Contains(got, "go env GOROOT")
	})
}
