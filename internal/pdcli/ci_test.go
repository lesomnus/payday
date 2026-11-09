package pdcli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"
)

// TestEveryJobThatSharesACacheKeepsAllOfIt.
//
// setup-go saves the module cache from the first job with its key to finish,
// and every job with the key restores that one on every run after. A job that
// downloaded only what it builds left the others fetching the rest from the
// proxy, until one of those fetches timed out and failed a run nothing had
// changed (`.github/actions/modules` says which). So each job that sets up Go
// with its cache downloads every module right after, whatever it builds, and
// this holds a job added later to the same.
func TestEveryJobThatSharesACacheKeepsAllOfIt(t *testing.T) {
	x := require.New(t)

	workflows, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.y*ml"))
	x.NoError(err)
	x.NotEmpty(workflows)

	type step struct {
		Uses string         `yaml:"uses"`
		With map[string]any `yaml:"with"`
	}
	type file struct {
		Jobs map[string]struct {
			Steps []step `yaml:"steps"`
		} `yaml:"jobs"`
	}

	const modules = ".github/actions/modules"

	cached := 0
	for _, p := range workflows {
		b, err := os.ReadFile(p)
		x.NoError(err)

		var f file
		x.NoError(yaml.Unmarshal(b, &f), p)

		for name, j := range f.Jobs {
			for i, s := range j.Steps {
				action, _, _ := strings.Cut(s.Uses, "@")
				// Cached unless it says otherwise, which is setup-go's default.
				if action != "actions/setup-go" || fmt.Sprint(s.With["cache"]) == "false" {
					continue
				}
				cached++

				x.Less(i+1, len(j.Steps), "%s: %s sets up Go with its cache and does nothing after", p, name)
				next := j.Steps[i+1]
				x.True(strings.HasSuffix(next.Uses, "/"+modules),
					"%s: %s sets up Go with its cache and does not download every module right after, so what it saves is what it built", p, name)

				// Where payday is checked out, which the action is told: a
				// checkout in a directory finds the action under it.
				at := strings.TrimSuffix(strings.TrimPrefix(next.Uses, "./"), modules)
				if at = strings.TrimSuffix(at, "/"); at == "" {
					at = "."
				}
				said := "."
				if v, ok := next.With["payday"]; ok {
					said = fmt.Sprint(v)
				}
				x.Equal(at, said, "%s: %s downloads the modules of payday at %s, and finds the action at %s", p, name, said, at)
			}
		}
	}
	x.NotZero(cached, "no job sets up Go with its cache, so this asserted nothing")
}
