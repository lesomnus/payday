package cli_test

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// tags is what the sandbox is built with -- the same words as the `wasm` script
// in `ts/package.json` and CI's build of it, because a graph listed under other
// tags is a graph nobody ships.
const tags = "grpcnotrace"

// heavy is what a page cannot use and a process can: what this package links,
// and what `grpcnotrace` leaves out.
var heavy = regexp.MustCompile(`^(` + strings.Join([]string{
	// The language a Watch filter is evaluated in (`filter.go`): cel-go, the
	// ANTLR runtime its parser is generated for, and `text/template`, which
	// `cel` imports for a prompt it can render. The last is worse than its
	// size: a program that calls a method by name through `reflect`, as a
	// template does, turns off the linker's pruning of methods nothing calls.
	// `github.com/google/cel-go` is the module's path before `cel.dev`, which
	// a dependency could still bring.
	`cel\.dev/`,
	`github\.com/google/cel-go`,
	`github\.com/antlr4-go/`,
	`text/template`,

	// The database engine a process opens (`driver.go`): SQLite compiled to
	// Wasm and run on wazero. The page opens "sqlite3-wasm", in a Worker.
	`github\.com/lesomnus/payday/config/dbsqlite3$`,
	`github\.com/ncruces/`,
	`github\.com/tetratelabs/wazero`,

	// The migration engine `entschema.Check` and `Schema.Create` reach.
	`ariga\.io/atlas`,
	`github\.com/hashicorp/hcl`,
	`github\.com/zclconf/go-cty`,
	`github\.com/lesomnus/payday/internal/apptest/internal/ent/migrate$`,

	// gRPC's debug page, which is what the tag is for.
	`golang\.org/x/net/trace`,
}, "|") + `)`)

// TestTheSandboxCarriesNoEngine is the rule at the top of `cli.go`, where it can
// fail.
//
// Nothing else would say. The page works with all of it linked, and is
// megabytes heavier to download: CEL came in through the generated `server/pd`,
// which imports `payday/resource`, and was 10 MB of this module before anybody
// measured it (payday#163). So the Wasm build's dependency graph is read, under
// `GOOS=js`, because `go list -deps` there is exactly what the linker follows --
// and an import is linked whether or not anything calls it, since every
// package's `init` is.
func TestTheSandboxCarriesNoEngine(t *testing.T) {
	x := require.New(t)

	c := exec.CommandContext(t.Context(), "go", "list", "-tags", tags, "-deps", "./wasm")
	c.Dir = ".."
	c.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	c.Stderr = os.Stderr

	out, err := c.Output()
	x.NoError(err)

	var got []string
	n := 0
	for p := range strings.SplitSeq(string(out), "\n") {
		if p == "" {
			continue
		}
		n++
		if heavy.MatchString(p) {
			got = append(got, p)
		}
	}

	// A graph of nothing carries nothing, and would pass.
	x.Greater(n, 100)
	x.Empty(got, "the sandbox links what a page cannot use; see cli/cli.go")
}

// TestTheSandboxLetsTheLinkerPruneMethods is the half of that rule no list of
// packages can say: nothing the sandbox links may call a method by index.
//
// A function that calls `reflect.Type.Method`, or `MethodByName` with a name
// the compiler cannot see, is marked for the linker, and a program that can
// reach one keeps every exported method of every type converted to an
// interface -- in every package, used or not -- since any of them might now be
// called by name. Nothing fails: the page works, and is larger. And there is no
// package to keep out, because the call was in one the sandbox has to link. The
// generated client's `runtime.go` makes the ent schema's fields at init, for
// their defaults and value scanners, and each field it made listed its type's
// methods that way. That was 24 MB of this module's 63, and an app with no
// default of its own has paid it since payday's Audit gave its `domain` column
// one (payday#87), which put the schema in every app's `runtime.go`.
// protobuf-orm/ent lists them in its loader now, which only codegen runs
// (protobuf-orm/ent#20).
//
// The linker says which marked functions it reached, and how, in its dump
// (`-dumpdep`). So the sandbox is linked as it ships, under `GOOS=js` with the
// same tags, and the dump is read.
func TestTheSandboxLetsTheLinkerPruneMethods(t *testing.T) {
	x := require.New(t)

	c := exec.CommandContext(t.Context(), "go", "build", "-tags", tags,
		"-ldflags=-dumpdep",
		"-o", filepath.Join(t.TempDir(), "app.wasm"),
		"./wasm",
	)
	c.Dir = ".."
	c.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")

	// The go command hands on what the linker says, the dump with it, on
	// stderr.
	var dump bytes.Buffer
	c.Stdout = &dump
	c.Stderr = &dump
	if err := c.Run(); err != nil {
		lines := strings.Split(strings.TrimSpace(dump.String()), "\n")
		x.NoError(err, "%s", strings.Join(lines[max(0, len(lines)-40):], "\n"))
	}

	// A line is `from -> to`, written when `to` is first reached, each name
	// followed by what the linker marked it with.
	var (
		from   = map[string]string{}
		marked []string
		iface  int
	)
	s := bufio.NewScanner(&dump)
	s.Buffer(nil, 1<<20)
	for s.Scan() {
		a, b, ok := strings.Cut(s.Text(), " -> ")
		if !ok {
			continue
		}
		if strings.HasSuffix(b, "<UsedInIface>") {
			iface++
		}
		a, _ = linked(a)
		b, m := linked(b)
		if _, ok := from[b]; ok {
			continue
		}
		from[b] = a
		if m {
			marked = append(marked, b)
		}
	}
	x.NoError(s.Err())

	// A dump that marks nothing would pass anything. The linker marks what is
	// converted to an interface the same way, and a sandbox converts thousands.
	x.Greater(iface, 1000)

	if len(marked) > 0 {
		chain := []string{marked[0]}
		for n := marked[0]; from[n] != "" && from[n] != "_"; n = from[n] {
			chain = append(chain, from[n])
		}
		slices.Reverse(chain)
		x.Fail("the sandbox calls a method by index, so the linker keeps every method",
			"%d marked functions are reached; the first through\n\t%s", len(marked), strings.Join(chain, "\n\t"))
	}
}

// linked is a name in the linker's dump without what it marked the name with,
// and whether that was a method called by index.
func linked(s string) (string, bool) {
	s, m := strings.CutSuffix(s, "<ReflectMethod>")
	s = strings.TrimSuffix(s, "<UsedInIface>")
	return strings.TrimSuffix(s, " "), m
}
