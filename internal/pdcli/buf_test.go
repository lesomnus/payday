package pdcli_test

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
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

// TestPdBuf is `pd buf`, run as a script runs it: a process, whose line, streams
// and status are what the command is for, and only a process has those.
func TestPdBuf(t *testing.T) {
	pd := filepath.Join(t.TempDir(), "pd")
	b, err := exec.CommandContext(t.Context(), "go", "build", "-o", pd, "github.com/lesomnus/payday/cmd/pd").CombinedOutput()
	require.NoError(t, err, "%s", b)

	// The buf a generation runs, from a directory that is no app, and not
	// whatever else a machine has: a script that looked for one took the
	// highest version in the cache, and a machine that has run two paydays
	// has the newer one's there.
	t.Run("is the buf this payday pins", func(t *testing.T) {
		if testing.Short() {
			t.Skip("fetches a release")
		}
		x := require.New(t)

		t.Setenv(pdcli.BufEnv, "")

		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(t.Context(), pd, "buf", "--version")
		cmd.Dir = t.TempDir()
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		x.NoError(cmd.Run(), "%s", stderr.String())
		x.Equal(pdcli.BufVersion+"\n", stdout.String())
	})

	// What buf found is answered as buf answers it, 100, and pd says nothing
	// over it: a script tells a finding from a buf that never ran by the
	// number, and `pd: exit status 100` under the diff is pd talking over
	// buf. And `--diff` after `format`, a flag after an argument, is buf's.
	t.Run("answers with buf's status", func(t *testing.T) {
		if testing.Short() {
			t.Skip("fetches a release")
		}
		x := require.New(t)

		t.Setenv(pdcli.BufEnv, "")

		dir := t.TempDir()
		x.NoError(os.WriteFile(filepath.Join(dir, "a.proto"),
			[]byte("syntax = \"proto3\";\n\npackage a;\n\nmessage   A { string  b = 1; }\n"), 0o644))

		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(t.Context(), pd, "buf", "format", "--diff", "--exit-code")
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = &stdout, &stderr

		var exit *exec.ExitError
		x.ErrorAs(cmd.Run(), &exit)
		x.Equal(100, exit.ExitCode(), "%s", stderr.String())
		x.Contains(stdout.String(), "+message A {")
		x.Empty(stderr.String())
	})

	// Everything after `buf` is buf's -- `--help` and `-h`, which pd would
	// answer itself, a flag after an argument, a `--`, an argument with a
	// space in it -- and so are stdin, stdout, stderr and the directory,
	// whichever buf it is.
	t.Run("hands buf the line, the streams and the directory", func(t *testing.T) {
		x := require.New(t)

		buf := filepath.Join(t.TempDir(), "buf")
		x.NoError(os.WriteFile(buf, []byte("#!/bin/sh\n"+
			"printf '%s\\n' \"$@\"\n"+
			"pwd -P\n"+
			"cat\n"+
			"echo 'from buf' >&2\n"+
			"exit 3\n"), 0o755))
		t.Setenv(pdcli.BufEnv, buf)

		dir := t.TempDir()
		args := []string{"lint", "--error-format", "json", "--help", "-h", "--", "a b"}

		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(t.Context(), pd, append([]string{"buf"}, args...)...)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader("from stdin\n")
		cmd.Stdout, cmd.Stderr = &stdout, &stderr

		var exit *exec.ExitError
		x.ErrorAs(cmd.Run(), &exit)
		x.Equal(3, exit.ExitCode(), "%s", stderr.String())

		here, err := filepath.EvalSymlinks(dir)
		x.NoError(err)
		x.Equal(strings.Join(args, "\n")+"\n"+here+"\n"+"from stdin\n", stdout.String())
		x.Equal("from buf\n", stderr.String())
	})

	// A signal sent to pd is buf's to answer, and the status is buf's answer
	// -- not pd stopped, and buf left running with nobody waiting for it.
	t.Run("passes a signal on", func(t *testing.T) {
		x := require.New(t)

		// Ready once its trap is set, and gone in five seconds whatever
		// happens, so that a pd that kept the signal leaves nothing running.
		buf := filepath.Join(t.TempDir(), "buf")
		x.NoError(os.WriteFile(buf, []byte("#!/bin/sh\n"+
			"trap 'exit 42' TERM\n"+
			"echo ready\n"+
			"i=0\n"+
			"while [ \"$i\" -lt 50 ]; do sleep 0.1; i=$((i + 1)); done\n"), 0o755))
		t.Setenv(pdcli.BufEnv, buf)

		cmd := exec.CommandContext(t.Context(), pd, "buf")
		out, err := cmd.StdoutPipe()
		x.NoError(err)
		x.NoError(cmd.Start())

		v, err := bufio.NewReader(out).ReadString('\n')
		x.NoError(err)
		x.Equal("ready\n", v)
		x.NoError(cmd.Process.Signal(syscall.SIGTERM))

		var exit *exec.ExitError
		x.ErrorAs(cmd.Wait(), &exit)
		x.Equal(42, exit.ExitCode(), "%v", exit)
	})
}
