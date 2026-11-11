package pdcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/lesomnus/payday/version"
)

// Upgrade brings an app up to what this payday needs, by running the tools that
// own each half rather than by editing what they write: npm for the page, and
// `go` for payday itself. It commits nothing; what changed is in the working
// tree for whoever ran it to read.
type Upgrade struct {
	Layout Layout

	// Log is where each step is said, and where the tools say what they did.
	Log io.Writer

	// DryRun says what would run and runs none of it.
	DryRun bool

	// Payday is a version `go get` takes -- `v0.2.0`, a commit, `latest` --
	// to move the app to before anything else. Empty is the payday running.
	Payday string
}

// Run is one upgrade.
func (u Upgrade) Run(ctx context.Context) error {
	if u.Payday != "" {
		return u.payday(ctx)
	}

	vs, err := Upgrades(u.Layout)
	if err != nil {
		return err
	}

	if len(vs) == 0 {
		u.say("pd: the page has what this payday needs")
	}
	for _, v := range vs {
		if v.Partner {
			u.say("pd: %s %s -> %s, with the package it moves with", v.Name, v.From, v.To)
			continue
		}
		u.say("pd: %s %s -> %s: below %s, %s", v.Name, v.From, v.To, v.Floor, v.Why)
	}

	// Every range is set first, each in the section the page has it in, and
	// then one `npm install` resolves what package.json asks for as one tree --
	// so a pair is resolved together even when its halves are in two sections,
	// as @bufbuild/protobuf, the bundle's, and protoc-gen-es, a dev dependency,
	// are. An install for each section resolved them one after the other: the
	// runtime moved under a generator that names its own release of it
	// exactly, and npm took it only by nesting a second runtime under the
	// generator until the next install moved that too.
	if len(vs) > 0 {
		ts := u.Layout.Path(DirTs)
		set := []string{"pkg", "set"}
		for _, v := range vs {
			set = append(set, v.Section+"["+v.Name+"]="+v.To)
		}
		if err := u.exec(ctx, ts, "npm", set...); err != nil {
			return err
		}
		if err := u.exec(ctx, ts, "npm", "install"); err != nil {
			return err
		}
	}

	if len(vs) > 0 && !u.DryRun {
		u.say("pd: nothing is committed; `git diff` says what moved")
	}

	if v := NodeVersion(ctx); v != "" && older(v, NodeFloor) {
		u.say("pd: node %s is older than %s, which nothing here can install: %s", v, NodeFloor, NodeWhy)
	}

	return nil
}

// payday moves the app to another payday and then runs **that** payday's
// generation and upgrade: the code an app generates has to be its generator's
// -- a runtime refuses code another payday wrote -- and its floors are the
// ones to meet.
//
// # The generator before tidy
//
// Until the new payday writes it again, the generated code is the old one's,
// and it imports what the old one had. `go mod tidy` reads every import, and
// one of a package the new payday does not have -- `payday/batch`, the day it
// went -- it does not leave missing: it asks for payday's `latest`, and moves
// the app there if that has it. That was v0.1.0, a tag pushed by accident
// months behind main, until v0.1.1 published its retraction. tidy moved the
// app onto it, said nothing of why, and the generation after it was v0.1.0's.
// So the Go half is written first, by the payday `go get` fetched, and tidy
// reads what it wrote.
//
// # And what no generator writes, asked about
//
// Code the generator does not write imports such a package too: the app's
// own, or a dependency generated with an older payday -- roster's `server/pd`,
// in an app that links an older roster. Nothing here writes those again. So
// one of them refuses the move before tidy, named ([ErrPaydayLacks]); tidy
// moving payday anyway puts go.mod and go.sum back as `go get` left them and
// refuses ([ErrPaydayMoved]); and a payday its module retracts is refused as
// soon as `go get` lands on it ([ErrPaydayRetracted]).
func (u Upgrade) payday(ctx context.Context) error {
	dir := u.Layout.Root

	// Whether there is a page is the page's to say, and not whether its
	// generator happens to be installed: without one, the Go half moved to
	// the new payday and the page's stayed the old one's. So a page whose
	// generator is not installed is refused here, before anything moves.
	have, page, err := Page(u.Layout)
	if err != nil {
		return err
	}
	if page {
		if _, err := TsPlugin(u.Layout); err != nil {
			fix := "npm install --save-dev " + tsPluginPackage
			if _, ok := have[tsPluginPackage]; ok {
				fix = "npm ci"
			}
			return fmt.Errorf("%w: the page is generated with the rest, so its generator has to be installed first -- %s, in %s",
				ErrNoTsPlugin, fix, u.Layout.Rel(DirTs))
		}
	}

	was, err := keep(dir)
	if err != nil {
		return err
	}
	if err := u.exec(ctx, dir, "go", "get", version.Module+"@"+u.Payday); err != nil {
		return err
	}
	if !u.DryRun {
		if err := u.retracted(ctx, dir); err != nil {
			return errors.Join(err, was.restore())
		}
	}

	// The rest is the payday `go get` fetched, which is not the one running:
	// `go tool pd` is whatever go.mod now names. A dry run fetches nothing, so
	// it can say what will be run but not what that payday will write or
	// move, and it says so rather than pass `--dry-run` on to a step it does
	// not run either.
	if u.DryRun {
		u.say("pd: the rest is run by the payday `go get` fetches, not by this one, and a dry run fetches nothing: what it would write and move is that payday's, and is not shown here")
	} else {
		u.say("pd: the rest is run by the payday `go get` fetched: its generator, and its floors")
	}

	// The Go half, before tidy reads what it imports.
	if err := u.exec(ctx, dir, "go", "tool", "pd", "gen", dir); err != nil {
		return err
	}

	got, err := keep(dir)
	if err != nil {
		return err
	}
	if !u.DryRun {
		if err := u.lacking(ctx, dir, got.payday()); err != nil {
			return err
		}
	}
	if err := u.exec(ctx, dir, "go", "mod", "tidy"); err != nil {
		return err
	}
	if !u.DryRun {
		now, err := keep(dir)
		if err != nil {
			return err
		}
		if now.payday() != got.payday() {
			return errors.Join(fmt.Errorf("%w, from %s to %s. tidy does that for an import it cannot find in the payday go.mod names -- "+
				"a package of payday's that one does not have -- and finds in another. It reads every file whatever its build constraints, "+
				"where the question before it asked only this platform's, so look for one behind a constraint. "+
				"go.mod and go.sum are as `go get` left them",
				ErrPaydayMoved, got.payday(), now.payday()), got.restore())
		}
	}

	if err := u.exec(ctx, dir, "go", "tool", "pd", "upgrade", dir); err != nil {
		return err
	}

	// The page's half, now that its floors are met. `--ts` writes the Go half
	// again with it, the same as it was.
	if page {
		if err := u.exec(ctx, dir, "go", "tool", "pd", "gen", "--ts", dir); err != nil {
			return err
		}
	}

	if !u.DryRun {
		u.say("pd: nothing is committed; `git diff` says what moved")
	}

	return nil
}

// ErrPaydayLacks is an app that imports, itself or through a dependency, a
// package of payday's that the payday it is moving to does not have.
var ErrPaydayLacks = errors.New("an import of a payday package this payday does not have")

// ErrPaydayMoved is `go mod tidy` moving an app off the payday `go get` put it
// on.
var ErrPaydayMoved = errors.New("go mod tidy moved payday off the version it was moved to")

// ErrPaydayRetracted is a payday its own module retracts.
var ErrPaydayRetracted = errors.New("that payday is retracted")

// retracted refuses the payday go.mod names if payday's module retracts it.
//
// A retraction is said in the go.mod of a later version, and the go command
// knows of it once that version is published: v0.1.1's go.mod retracts v0.1.0
// and v0.1.1.
func (u Upgrade) retracted(ctx context.Context, dir string) error {
	out, err := u.output(ctx, dir, "go", "list", "-m", "-retracted",
		"-f", `{{.Version}}{{range .Retracted}}{{println}}{{.}}{{end}}`, version.Module)
	if err != nil {
		return err
	}

	vs := strings.Split(strings.TrimSpace(out), "\n")
	if len(vs) < 2 {
		return nil
	}

	return fmt.Errorf("%w: %s, %s. go.mod and go.sum are as they were", ErrPaydayRetracted, vs[0], strings.Join(vs[1:], "; "))
}

// lacking refuses the app if anything it builds -- its own packages and their
// tests, and every package they import -- imports a package of payday's that
// payday `at` does not have: one go list could not find, where payday would
// be.
func (u Upgrade) lacking(ctx context.Context, dir string, at string) error {
	out, err := u.output(ctx, dir, "go", "list", "-e", "-deps", "-test",
		"-f", `{{.ImportPath}}{{"\t"}}{{if .Standard}}std{{else}}{{with .Module}}{{.Path}} {{.Version}}{{end}}{{end}}{{"\t"}}{{join .Imports " "}}`,
		"./...")
	if err != nil {
		return err
	}

	type pkg struct {
		path, module string
		imports      []string
	}
	missing := map[string]bool{}
	pkgs := []pkg{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}

		// A test's variant of a package is named with the test after it.
		path, _, _ := strings.Cut(f[0], " ")
		if f[1] == "" && (path == version.Module || strings.HasPrefix(path, version.Module+"/")) {
			missing[path] = true
			continue
		}
		pkgs = append(pkgs, pkg{path: path, module: strings.TrimSpace(f[1]), imports: strings.Fields(f[2])})
	}
	if len(missing) == 0 {
		return nil
	}

	// Each, with who imports it -- which go list did not say of a package it
	// could not load, so a missing one is named either way.
	by := map[string][]string{}
	for _, p := range pkgs {
		for _, v := range p.imports {
			if !missing[v] {
				continue
			}

			// A package of the main module has no version.
			whose := "the app's own"
			if strings.Contains(p.module, " ") {
				whose = p.module
			}
			if line := fmt.Sprintf("%s (%s)", p.path, whose); !slices.Contains(by[v], line) {
				by[v] = append(by[v], line)
			}
		}
	}
	lines := []string{}
	for _, v := range slices.Sorted(maps.Keys(missing)) {
		if len(by[v]) == 0 {
			lines = append(lines, "  "+v)
			continue
		}
		slices.Sort(by[v])
		for _, w := range by[v] {
			lines = append(lines, "  "+v+", imported by "+w)
		}
	}

	return fmt.Errorf("%w: payday %s has no\n\n%s\n\n"+
		"and `go mod tidy` would find each in another payday and move the app onto that one. "+
		"Change the app's own code to what this payday has, and move a dependency to its version generated with this payday "+
		"-- an app that links roster gets the roster generated with it first -- then upgrade again. "+
		"go.mod names payday %s and the generated code is that payday's; tidy has not run",
		ErrPaydayLacks, at, strings.Join(lines, "\n"), at)
}

// output runs one tool where it belongs and answers with what it wrote, for a
// question rather than a step: it is not said.
func (u Upgrade) output(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stderr = u.out()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}

	return string(out), nil
}

// mod is go.mod and go.sum as they were at one moment, to be put back.
type mod struct {
	path, sum string
	b, sumB   []byte

	// noSum is a module that had no go.sum.
	noSum bool
}

func keep(dir string) (mod, error) {
	p, at, err := findUp(dir, "go.mod")
	if err != nil {
		return mod{}, err
	}

	m := mod{path: p, sum: filepath.Join(at, "go.sum")}
	if m.b, err = os.ReadFile(m.path); err != nil {
		return mod{}, err
	}
	if m.sumB, err = os.ReadFile(m.sum); os.IsNotExist(err) {
		m.noSum, err = true, nil
	}

	return m, err
}

// payday is the version of payday it requires, or nothing.
func (m mod) payday() string {
	f, err := modfile.ParseLax(m.path, m.b, nil)
	if err != nil {
		return ""
	}
	for _, r := range f.Require {
		if r.Mod.Path == version.Module {
			return r.Mod.Version
		}
	}

	return ""
}

func (m mod) restore() error {
	if err := os.WriteFile(m.path, m.b, 0o644); err != nil {
		return err
	}
	if m.noSum {
		if err := os.Remove(m.sum); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	return os.WriteFile(m.sum, m.sumB, 0o644)
}

// exec runs one tool where it belongs, with what it says passed through.
func (u Upgrade) exec(ctx context.Context, dir string, name string, args ...string) error {
	u.say("pd: %s %s (in %s)", name, strings.Join(args, " "), u.Layout.Rel(relTo(u.Layout, dir)))
	if u.DryRun {
		return nil
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = u.out()
	cmd.Stderr = u.out()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}

	return nil
}

func (u Upgrade) out() io.Writer {
	if u.Log == nil {
		return io.Discard
	}

	return u.Log
}

func (u Upgrade) say(format string, vs ...any) {
	fmt.Fprintf(u.out(), format+"\n", vs...)
}

// relTo is dir as the parts [Layout.Rel] takes: below the app's root, or the
// root itself.
func relTo(l Layout, dir string) string {
	v := strings.TrimPrefix(strings.TrimPrefix(dir, l.Root), string(os.PathSeparator))
	if v == "" {
		return "."
	}

	return v
}
