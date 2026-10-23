package cli_test

import (
	"os"
	"os/exec"
	"regexp"
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
