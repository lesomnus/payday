package pdcli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// TestTheTemplateIsWhatAPersonWrites, and nothing that a generator does.
//
// A template carrying generated code is a template carrying the output of one
// version of the generators, and the first thing anybody does with it is
// regenerate. So what is checked here is that none of it is there -- and the
// slow test below is what says the rest of it appears when `pd gen` runs.
func TestTheTemplateIsWhatAPersonWrites(t *testing.T) {
	x := require.New(t)

	dir := filepath.Join(t.TempDir(), "app")
	x.NoError((pdcli.New{Dir: dir, Module: "github.com/acme/thing"}).Write())

	var vs []string
	x.NoError(filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		rel, _ := filepath.Rel(dir, p)
		vs = append(vs, filepath.ToSlash(rel))

		return nil
	}))

	for _, v := range vs {
		x.NotContains(v, ".g.go", "%s is generated", v)
		x.NotContains(v, ".pb.go", "%s is generated", v)
		x.NotContains(v, "gen/", "%s is generated", v)
	}

	// And the pieces a person does write.
	for _, want := range []string{
		"go.mod",
		"buf.yaml",
		"cmd/serve.go",
		"cmd/config.go",
		"cmd/auth.go",
		"cmd/thing/main.go",
		"proto/app/thing.proto",
		"thing.yaml",
		"ts/src/client.ts",
	} {
		x.Contains(vs, want)
	}
}

// TestTheNameIsWrittenOnce, which is what everything about where configuration
// comes from is derived from.
func TestTheNameIsWrittenOnce(t *testing.T) {
	x := require.New(t)

	dir := filepath.Join(t.TempDir(), "app")
	x.NoError((pdcli.New{Dir: dir, Module: "github.com/acme/Thing-Server"}).Write())

	// Folded into something that can be an alias, since that is what it is used
	// as: the prefix of every environment variable and the name of the file.
	b, err := os.ReadFile(filepath.Join(dir, "cmd", "config.go"))
	x.NoError(err)
	x.Contains(string(b), `Name = "thing-server"`)

	_, err = os.Stat(filepath.Join(dir, "thing-server.yaml"))
	x.NoError(err)

	_, err = os.Stat(filepath.Join(dir, "cmd", "thing-server", "main.go"))
	x.NoError(err)

	// And the module goes everywhere an import does.
	b, err = os.ReadFile(filepath.Join(dir, "cmd", "serve.go"))
	x.NoError(err)
	x.Contains(string(b), `"github.com/acme/Thing-Server/server/pd"`)
	x.NotContains(string(b), "apptest")
}

// TestWritingOverSomebodyElsesWorkIsRefused, which is the one mistake here that
// deleting something does not undo.
func TestWritingOverSomebodyElsesWorkIsRefused(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	x.NoError(os.WriteFile(filepath.Join(dir, "README.md"), []byte("mine"), 0o644))

	err := (pdcli.New{Dir: dir, Module: "github.com/acme/thing"}).Write()
	x.ErrorContains(err, "not empty")
}

// TestTheStepsAreInAnOrderThatWorks.
//
// None of it is guessable, and every one of these was found by typing the
// printed steps into an empty directory and watching one of them fail.
//
//   - `go tool pd` builds payday's command from **this app's** module graph, so
//     the tool directive has to have been fetched like any other;
//   - the generators have to build before anything is generated, and that needs
//     sums -- but a plain `go mod tidy` cannot run until the generated packages
//     exist, since a module that is not there resolves as one to fetch. So:
//     `-e` first, and the real one after;
//   - and `buf dep update` is not a step at all. It compiles the workspace
//     before writing the lock, and the workspace does not compile until
//     `pd gen` has copied payday's entities in -- so as a first step it fails
//     on every new app. `pd gen` owns it; see `run.deps`.
func TestTheStepsAreInAnOrderThatWorks(t *testing.T) {
	x := require.New(t)

	vs := (pdcli.New{Module: "github.com/acme/thing"}).Steps()
	joined := strings.Join(vs, "\n")

	x.Contains(joined, "go get -tool github.com/lesomnus/payday/cmd/pd")

	tools := strings.LastIndex(joined, "go get -tool")
	lenient := strings.Index(joined, "go mod tidy -e")
	gen := strings.Index(joined, "pd gen .")
	tidy := strings.Index(joined, "\ngo mod tidy\n")
	build := strings.Index(joined, "go build ./...")

	x.Less(tools, lenient, "the generators cannot build without their sums")
	x.Less(lenient, gen)
	x.Less(gen, tidy, "tidy before gen fetches this app's own packages from a repository nobody pushed")
	x.Less(tidy, build)

	x.NotContains(joined, "buf dep update", "it cannot run before a generation; pd gen does it")
}
