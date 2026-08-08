package pdcli

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"unicode"
)

// The template, embedded so that `go tool pd new` works from a checkout that
// has payday as a dependency and nothing else.
//
// It holds only what a person writes. Nothing generated is in it, and that is
// deliberate: committing generated code into a template means committing the
// output of one version of the generators, and the first thing anybody does is
// regenerate it. What `new` writes is a tree that `pd gen` then fills in, and a
// test does exactly that and builds the result -- which is a better check than
// any amount of committed output.
//
//go:embed all:template
var tree embed.FS

// New is one app being written out.
type New struct {
	// Dir is where it goes. It has to be empty or not there.
	Dir string

	// Module is the Go module path: "github.com/acme/thing".
	Module string

	// Name is what the app is called, and is the last segment of the module
	// when nothing said. Everything about where configuration comes from is
	// derived from it -- the files that are read, the prefix of every
	// environment variable -- which is why it is written once.
	Name string
}

// Write puts the app there.
func (n New) Write() error {
	if n.Module == "" {
		return fmt.Errorf("say what module this is, e.g. github.com/acme/thing")
	}
	if n.Name == "" {
		n.Name = alias(path(n.Module))
	}
	if !nameOk.MatchString(n.Name) {
		return fmt.Errorf("%q is not a name; it begins with a lowercase letter and holds lowercase letters, digits and single hyphens", n.Name)
	}
	if n.Dir == "" {
		n.Dir = n.Name
	}

	// Refused rather than merged. Writing a template over a directory somebody
	// already has is the one mistake here that cannot be undone by deleting
	// something.
	if vs, err := os.ReadDir(n.Dir); err == nil && len(vs) > 0 {
		return fmt.Errorf("%s is not empty", n.Dir)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	data := struct{ Module, Name, Env string }{
		Module: n.Module,
		Name:   n.Name,
		Env:    strings.ToUpper(strings.ReplaceAll(n.Name, "-", "_")),
	}

	return fs.WalkDir(tree, "template", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		// The path is a template too: the binary lives in a directory named
		// after the app, and the configuration file is named after it.
		rel := strings.ReplaceAll(strings.TrimPrefix(p, "template/"), "{{.Name}}", n.Name)
		out := filepath.Join(n.Dir, strings.TrimSuffix(rel, ".tmpl"))

		if filepath.Base(out) == "app.yaml" {
			out = filepath.Join(filepath.Dir(out), n.Name+".yaml")
		}

		b, err := tree.ReadFile(p)
		if err != nil {
			return err
		}

		if strings.HasSuffix(p, ".tmpl") {
			t, err := template.New(rel).Parse(string(b))
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}

			sb := &strings.Builder{}
			if err := t.Execute(sb, data); err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}

			b = []byte(sb.String())
		}

		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}

		return os.WriteFile(out, b, 0o644)
	})
}

// nameOk is the same grammar an alias keeps, because that is what a name is
// used as: the prefix of every environment variable, the name of the
// configuration file, the name of the local store's database.
var nameOk = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

func path(module string) string {
	i := strings.LastIndex(module, "/")
	if i < 0 {
		return module
	}

	return module[i+1:]
}

// alias folds a module's last segment into something that can be a name.
func alias(v string) string {
	b := &strings.Builder{}
	for _, c := range v {
		switch {
		case unicode.IsLetter(c) || unicode.IsDigit(c):
			b.WriteRune(unicode.ToLower(c))
		case b.Len() > 0:
			b.WriteByte('-')
		}
	}

	return strings.Trim(b.String(), "-")
}

// Tools is what an app's go.mod declares, in the order they are added.
var Tools = tools

// Setup makes the app ready to generate: the tools, then buf's dependencies.
//
// It is a flag rather than the default because it is minutes on a cold module
// cache and needs the network, and `pd new` should be able to write a tree
// without either. What it saves is not typing -- [New.Steps] prints the
// commands -- it is the order, which is not guessable: `go mod tidy` cannot run
// until the generated packages exist, and they cannot be generated until the
// tools are there.
func (n New) Setup(ctx context.Context, log io.Writer) error {
	dir := n.Dir
	if dir == "" {
		dir = n.Name
	}

	run := func(name string, args ...string) error {
		if log != nil {
			fmt.Fprintf(log, "  %s %s\n", name, strings.Join(args, " "))
		}

		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir

		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, stderr.String())
		}

		return nil
	}

	// The tools first. `go mod tidy` cannot run yet: the app imports its own
	// generated packages, and a module that is not there resolves as one to
	// fetch -- which fails, loudly, against a repository nobody has pushed.
	for _, t := range tools {
		if err := run("go", "get", "-tool", t); err != nil {
			return err
		}
	}

	if err := run("buf", "dep", "update"); err != nil {
		return err
	}

	return nil
}

// Steps is what to type, for a `new` that did not set the app up itself.
func (n New) Steps() []string {
	// The same defaults [New.Write] applies, and applied again here because it
	// takes a value: an app named after its module wrote `cmd/widget/` and then
	// said to run `./cmd/`.
	if n.Name == "" {
		n.Name = alias(path(n.Module))
	}

	dir := n.Dir
	if dir == "" {
		dir = n.Name
	}

	vs := []string{"cd " + dir}
	for _, t := range tools {
		vs = append(vs, "go get -tool "+t)
	}

	return append(vs,
		"buf dep update",
		"go tool pd gen .",
		"go mod tidy",
		"go run ./cmd/"+n.Name+" serve",
	)
}
