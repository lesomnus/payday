package pdcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"

	"golang.org/x/mod/semver"
)

// Floor is the oldest version of a package an app's page may have and still be
// built against what this payday generates and ships.
//
// A floor is a version payday has built a page with: CI's `floor` job makes an
// app with `pd new`, puts every package here at exactly its floor and builds it,
// on Node at [NodeFloor]. An upgrade moves a package below its floor to the
// floor, in the major it is in, and to its [Target] -- the version the page
// `pd new` writes declares, which the `new` job builds -- only when the floor
// is a later major (see [Upgrades]). So a floor is raised when payday comes to
// need something newer, and never because something newer exists: keeping
// current is Renovate's, and an app above its floors is left alone.
type Floor struct {
	// Name is the npm package.
	Name string

	// Minimum is the oldest version, as npm writes one.
	Minimum string

	// Why is the reason it is that one: what a person reads when they are held
	// to it, and what somebody raising it checks next.
	Why string

	// With are the packages that move with this one. A major of one is a
	// major of the others, and npm refuses the half of the pair that moved
	// alone -- which is how @vitejs/plugin-react 6 failed on its own.
	With []string

	// Gen says `pd gen --ts` refuses below it. The code it writes is the
	// generator's and imports the runtime's, so an older one writes code the
	// rest of the page was not made with.
	Gen bool
}

// Floors is every package payday holds an app's page to.
var Floors = []Floor{{
	Name:    "@bufbuild/protobuf",
	Minimum: "2.12.1",
	With:    []string{"@bufbuild/protoc-gen-es"},
	Gen:     true,
	Why:     "the runtime the code `pd gen --ts` writes imports, of the generator's release",
}, {
	Name:    "@bufbuild/protoc-gen-es",
	Minimum: "2.12.1",
	With:    []string{"@bufbuild/protobuf"},
	Gen:     true,
	Why:     "what `pd gen --ts` writes the messages and services with",
}, {
	Name:    "@connectrpc/connect",
	Minimum: "2.1.2",
	With:    []string{"@connectrpc/connect-web"},
	Why:     "@lesomnus/payday's peer: the client a page makes of the services `pd gen --ts` writes",
}, {
	Name:    "@connectrpc/connect-web",
	Minimum: "2.1.2",
	With:    []string{"@connectrpc/connect"},
	Why:     "the browser's transport for that client, of connect's release",
}, {
	Name:    "@lesomnus/payday",
	Minimum: "0.1.2",
	Why:     "payday's half of the page, which the template's src is written against: its store.ts deletes other callers' mirrors with deleteOtherDisks, which 0.1.2 brought",
}, {
	Name:    "typescript",
	Minimum: "5.9.3",
	Why:     "what payday's declarations and the template's tsconfig are compiled with",
}, {
	Name:    "vite",
	Minimum: "7.2.0",
	With:    []string{"@vitejs/plugin-react"},
	Why:     "the vite.config.ts `pd new` and `pd sandbox init` write is written for it",
}, {
	Name:    "@vitejs/plugin-react",
	Minimum: "5.0.4",
	With:    []string{"vite"},
	Why:     "the React plugin that vite.config.ts loads, whose majors follow Vite's",
}}

// NodeFloor is the oldest Node a page is built with, and why. Nothing here
// installs one: it is the machine's, so it is told rather than moved.
const (
	NodeFloor = "22.12.0"
	NodeWhy   = "Vite 7 and 8 run on Node 20.19 or 22.12 and later; payday builds its floor on Node 22 and its target on 24"
)

// Target is the range the page `pd new` writes declares for a package, in the
// section it declares it in: what an upgrade moves a package to when its floor
// is a later major than the one the page has.
type Target struct {
	Range   string
	Section string
}

const (
	sectionDeps = "dependencies"
	sectionDev  = "devDependencies"
)

// Targets reads them off the template this payday writes, so that what an
// upgrade moves to and what a new app starts with are one list -- which
// Renovate keeps current and CI's `new` job builds.
func Targets() (map[string]Target, error) {
	b, err := tree.ReadFile("template/ts/package.json.tmpl")
	if err != nil {
		return nil, err
	}

	// What `pd new` hands the template, for an app called anything: only the
	// name line reads it.
	t, err := template.New("package.json").Parse(string(b))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := t.Execute(&out, struct{ Module, Name, Env string }{"example.com/app", "app", "APP"}); err != nil {
		return nil, err
	}

	var p manifest
	if err := json.Unmarshal(out.Bytes(), &p); err != nil {
		return nil, fmt.Errorf("the template's package.json: %w", err)
	}

	vs := map[string]Target{}
	for name, r := range p.Dependencies {
		vs[name] = Target{Range: r, Section: sectionDeps}
	}
	for name, r := range p.DevDependencies {
		vs[name] = Target{Range: r, Section: sectionDev}
	}

	return vs, nil
}

type manifest struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// Have is what an app's page says of one package.
type Have struct {
	// Section is where its package.json declares it, and Range what it asks.
	Section string
	Range   string

	// Installed is the version a lockfile or node_modules says is there, and
	// empty when neither does.
	Installed string
}

// Version is the version to hold to a floor: the one installed, or, with
// nothing installed, the lowest the range admits -- which is what a fresh
// install could pick. Empty when neither can be read.
func (h Have) Version() string {
	if h.Installed != "" {
		return h.Installed
	}

	if m := lowest.FindStringSubmatch(h.Range); m != nil {
		return m[1]
	}

	return ""
}

// lowest is the version a range begins at, for the ranges a person writes:
// `^1.2.3`, `~1.2.3`, `>=1.2.3` and `1.2.3`.
var lowest = regexp.MustCompile(`^\s*(?:\^|~|>=|=)?\s*v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)\s*$`)

// Page reads the app's page: what its package.json declares, and what is
// installed of each. It answers false for an app with no page, which is a
// backend that `pd gen` serves on its own.
func Page(l Layout) (map[string]Have, bool, error) {
	b, err := os.ReadFile(l.Path(DirTs, "package.json"))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	var p manifest
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, false, fmt.Errorf("%s: %w", l.Rel(DirTs, "package.json"), err)
	}

	vs := map[string]Have{}
	for name, r := range p.Dependencies {
		vs[name] = Have{Section: sectionDeps, Range: r}
	}
	for name, r := range p.DevDependencies {
		vs[name] = Have{Section: sectionDev, Range: r}
	}

	locks := installedIn(l)
	for name, v := range vs {
		v.Installed = locks(name)
		vs[name] = v
	}

	return vs, true, nil
}

// installedIn answers with what is installed of a package, asking in the order
// npm resolves in: the page's own lockfile, a workspace's above it, and then
// whatever node_modules holds.
func installedIn(l Layout) func(name string) string {
	type lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	read := func(p string) *lock {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		v := &lock{}
		if json.Unmarshal(b, v) != nil {
			return nil
		}
		return v
	}

	page := read(l.Path(DirTs, "package-lock.json"))
	work := read(filepath.Join(l.Work, "package-lock.json"))
	rel, _ := filepath.Rel(l.Work, l.Path(DirTs))

	return func(name string) string {
		for _, at := range []struct {
			lock *lock
			key  string
		}{
			{page, "node_modules/" + name},
			{work, filepath.ToSlash(filepath.Join(rel, "node_modules", name))},
			{work, "node_modules/" + name},
		} {
			if at.lock == nil {
				continue
			}
			if v := at.lock.Packages[at.key].Version; v != "" {
				return v
			}
		}

		for _, d := range []string{l.Path(DirTs), l.Work} {
			b, err := os.ReadFile(filepath.Join(d, "node_modules", name, "package.json"))
			if err != nil {
				continue
			}
			var v struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(b, &v) == nil && v.Version != "" {
				return v.Version
			}
		}

		return ""
	}
}

// Step is one package an upgrade moves.
type Step struct {
	Name    string
	Section string

	// From is what it is now, and To the range it is moved to.
	From string
	To   string

	// Floor is what it was held to, and Why is why. Partner is set for a
	// package that was not below its own floor and moves with one that was.
	Floor   string
	Why     string
	Partner bool
}

// Upgrades are the packages of the app's page below their floors, and the
// packages they move with. A package the page does not have is not added, and
// one whose version cannot be read is not guessed at.
//
// A package below its floor moves to the floor and no further than the major
// it is in, `^` the floor: what payday needs of it, and nothing an app has not
// asked for -- a later major is a migration, and Renovate's to propose. Only a
// package whose floor is itself a later major has a major to cross anyway, and
// that one moves to what `pd new` writes, its [Target], with every package it
// moves with that the page has: a major of one is a major of the others.
func Upgrades(l Layout) ([]Step, error) {
	page, ok, err := Page(l)
	if err != nil || !ok {
		return nil, err
	}

	return upgrades(page)
}

func upgrades(page map[string]Have) ([]Step, error) {
	targets, err := Targets()
	if err != nil {
		return nil, err
	}

	below := map[string]bool{}
	for _, f := range Floors {
		h, ok := page[f.Name]
		if !ok {
			continue
		}
		if v := h.Version(); v != "" && older(v, f.Minimum) {
			below[f.Name] = true
		}
	}

	cross := map[string]bool{}
	for _, f := range Floors {
		if !below[f.Name] || semver.Major("v"+f.Minimum) == semver.Major("v"+page[f.Name].Version()) {
			continue
		}
		cross[f.Name] = true
		for _, w := range f.With {
			if _, ok := page[w]; ok {
				cross[w] = true
			}
		}
	}

	var vs []Step
	for _, f := range Floors {
		if !below[f.Name] && !cross[f.Name] {
			continue
		}
		to := "^" + f.Minimum
		if cross[f.Name] {
			t, ok := targets[f.Name]
			if !ok {
				return nil, fmt.Errorf("%s has a floor and the template declares no version of it", f.Name)
			}
			to = t.Range
		}
		h := page[f.Name]
		vs = append(vs, Step{
			Name:    f.Name,
			Section: h.Section,
			From:    h.Version(),
			To:      to,
			Floor:   f.Minimum,
			Why:     f.Why,
			Partner: !below[f.Name],
		})
	}

	return vs, nil
}

// Below is the generator's half of [Upgrades]: what `pd gen --ts` refuses to
// write with, given the plugin [TsPlugin] found.
//
// The generator is held to its floor by the version of the plugin that runs
// -- its own package's, [PluginVersion] -- and not by what a lockfile says: a
// lockfile is what the next install makes, and a node_modules an install has
// not caught up with writes with what it has. The runtime its code imports is
// read as any other package is.
func Below(l Layout, plugin string) ([]Step, error) {
	page, ok, err := Page(l)
	if err != nil || !ok {
		return nil, err
	}

	v, err := PluginVersion(plugin)
	if err != nil {
		return nil, err
	}
	h := page[tsPluginPackage]
	h.Installed = v
	page[tsPluginPackage] = h

	vs, err := upgrades(page)
	if err != nil {
		return nil, err
	}

	gen := map[string]bool{}
	for _, f := range Floors {
		gen[f.Name] = f.Gen
	}

	return slices.DeleteFunc(vs, func(v Step) bool { return v.Partner || !gen[v.Name] }), nil
}

// tsPluginPackage is the npm package protoc-gen-es is in.
const tsPluginPackage = "@bufbuild/protoc-gen-es"

// PluginVersion is the version of the protoc-gen-es a plugin path runs: that
// of the package it is part of. The path is a `.bin` entry, which npm makes a
// link into the package and pnpm a script that calls it, so the link is
// followed to the package when there is one, and otherwise the package is
// the one of that name in the same node_modules -- which is where every
// package manager puts the package whose bin it is.
func PluginVersion(plugin string) (string, error) {
	type pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	read := func(p string) (pkg, bool) {
		b, err := os.ReadFile(p)
		if err != nil {
			return pkg{}, false
		}
		var v pkg
		if json.Unmarshal(b, &v) != nil || v.Name != tsPluginPackage || v.Version == "" {
			return pkg{}, false
		}
		return v, true
	}

	if real, err := filepath.EvalSymlinks(plugin); err == nil {
		for d := filepath.Dir(real); filepath.Base(d) != "node_modules" && filepath.Dir(d) != d; d = filepath.Dir(d) {
			if v, ok := read(filepath.Join(d, "package.json")); ok {
				return v.Version, nil
			}
		}
	}

	modules := filepath.Dir(filepath.Dir(plugin))
	if v, ok := read(filepath.Join(modules, filepath.FromSlash(tsPluginPackage), "package.json")); ok {
		return v.Version, nil
	}

	return "", fmt.Errorf("%w: %s is not part of an installed %s, so which one it is cannot be told",
		ErrNoTsPlugin, plugin, tsPluginPackage)
}

// older says a is an older version than b, both as npm writes them.
func older(a, b string) bool {
	return semver.Compare("v"+a, "v"+b) < 0
}

// NodeVersion is the Node on PATH, as npm writes a version, and empty with no
// Node there.
func NodeVersion(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "node", "--version").Output()
	if err != nil {
		return ""
	}

	return strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
}
