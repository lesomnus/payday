package pdcli_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// TestABufIsNamedRatherThanFound.
//
// The environment wins outright and nothing checks it against a digest: naming
// a buf is saying which one, and a payday that refused it would be refusing the
// answer it asked for. It is the escape hatch for the machine with no route to
// GitHub and for somebody bisecting buf itself.
func TestABufIsNamedRatherThanFound(t *testing.T) {
	x := require.New(t)

	t.Setenv(pdcli.BufEnv, "/somewhere/of/my/own/buf")

	v, err := pdcli.Buf(t.Context())
	x.NoError(err)
	x.Equal("/somewhere/of/my/own/buf", v)
}

// TestTheBufThisPaydayPinsIsTheBufThatRuns.
//
// The version is not a thing an app is asked, so this is the assertion that the
// answer is the pinned one and that it is the real thing: it is fetched,
// verified against the digest published beside the release, and then run.
//
// It touches the network on a cold cache, which is the honest shape of the
// thing being tested -- there is no version to check without fetching one.
func TestTheBufThisPaydayPinsIsTheBufThatRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("fetches a release")
	}

	x := require.New(t)

	at, err := pdcli.Buf(t.Context())
	x.NoError(err)
	x.Contains(at, pdcli.BufVersion, "the path says which buf ran")

	b, err := exec.CommandContext(t.Context(), at, "--version").CombinedOutput()
	x.NoError(err)
	x.Equal(pdcli.BufVersion, strings.TrimSpace(string(b)))

	// And again, which is the cached path: nothing is fetched twice, and the
	// answer is the same file.
	again, err := pdcli.Buf(t.Context())
	x.NoError(err)
	x.Equal(at, again)
}

// TestTheBufCIRunsIsTheBufThisPaydayPins.
//
// CI runs buf itself in one place -- `format`, `lint`, `build` and `breaking`
// in the `go` job -- and that buf is the one `pd` pins, or those checks pass
// or fail on a buf no generation runs. buf-action given no version takes the
// latest release, so every step that sets a buf up names one, and what it
// names is BufVersion: moved with it, by hand, and never alone, whether by a
// person or by a bot.
//
// And `.github/actions/buf`, which keeps `pd`'s buf between runs, reads the
// version off this package's source, so the line it reads is held to the form
// it reads.
func TestTheBufCIRunsIsTheBufThisPaydayPins(t *testing.T) {
	x := require.New(t)

	github := filepath.Join("..", "..", ".github")
	workflows, err := filepath.Glob(filepath.Join(github, "workflows", "*.y*ml"))
	x.NoError(err)
	x.NotEmpty(workflows)
	actions, err := filepath.Glob(filepath.Join(github, "actions", "*", "action.y*ml"))
	x.NoError(err)

	type step struct {
		Uses string         `yaml:"uses"`
		With map[string]any `yaml:"with"`
	}
	type file struct {
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
		Runs struct {
			Steps []step `yaml:"steps"`
		} `yaml:"runs"`
	}

	steps := 0
	for _, p := range append(workflows, actions...) {
		b, err := os.ReadFile(p)
		x.NoError(err)

		var f file
		x.NoError(yaml.Unmarshal(b, &f), p)

		all := f.Runs.Steps
		for _, j := range f.Jobs {
			all = append(all, j.Steps...)
		}
		for _, s := range all {
			steps++

			action, _, _ := strings.Cut(s.Uses, "@")
			if action != "bufbuild/buf-action" && action != "bufbuild/buf-setup-action" {
				continue
			}

			v, ok := s.With["version"]
			x.True(ok, "%s: %s names no version, so it takes buf's latest release; give it the one pd pins, %s", p, s.Uses, pdcli.BufVersion)
			x.Equal(pdcli.BufVersion, fmt.Sprint(v), "%s: %s sets up a buf other than the one pd pins", p, s.Uses)
		}
	}
	x.NotZero(steps, "no steps were read")

	// What the action's sed reads: one line, and the version on it.
	b, err := os.ReadFile("buf.go")
	x.NoError(err)
	read := regexp.MustCompile(`(?m)^const BufVersion = "([^"]*)"$`).FindAllStringSubmatch(string(b), -1)
	x.Len(read, 1, ".github/actions/buf reads the version off the one line `const BufVersion = \"...\"` in buf.go")
	x.Equal(pdcli.BufVersion, read[0][1])
}
