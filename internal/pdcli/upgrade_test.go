package pdcli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"

	"github.com/lesomnus/payday/internal/pdcli"
)

// page writes an app's page: its package.json, with each package in the
// section given, and the lockfile that says what is installed of each.
func page(t *testing.T, root string, deps, devDeps, installed map[string]string) pdcli.Layout {
	t.Helper()
	x := require.New(t)

	dir := filepath.Join(root, "ts")
	x.NoError(os.MkdirAll(dir, 0o755))
	b, err := json.Marshal(map[string]any{"name": "app-ui", "dependencies": deps, "devDependencies": devDeps})
	x.NoError(err)
	x.NoError(os.WriteFile(filepath.Join(dir, "package.json"), b, 0o644))

	if installed != nil {
		pkgs := map[string]any{"": map[string]any{"name": "app-ui"}}
		for name, v := range installed {
			pkgs["node_modules/"+name] = map[string]any{"version": v}
		}
		b, err = json.Marshal(map[string]any{"lockfileVersion": 3, "packages": pkgs})
		x.NoError(err)
		x.NoError(os.WriteFile(filepath.Join(dir, "package-lock.json"), b, 0o644))
	}

	l, err := pdcli.Discover(root)
	x.NoError(err)

	return l
}

// fake puts a program called name on PATH that writes where it ran and what it
// was asked into a log, and answers with that log.
func fake(t *testing.T, name string) func() []string {
	t.Helper()

	dir := t.TempDir()
	log := filepath.Join(dir, name+".log")
	sh := "#!/bin/sh\necho \"$(pwd) $*\" >> " + log + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(sh), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() []string {
		b, err := os.ReadFile(log)
		if os.IsNotExist(err) {
			return nil
		}
		require.NoError(t, err)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// generator installs a protoc-gen-es into the page the way npm does: the
// package, with its version, and a `.bin` entry that links into it.
func generator(t *testing.T, l pdcli.Layout, version string) string {
	t.Helper()
	x := require.New(t)

	modules := filepath.Join(l.Root, "ts", "node_modules")
	pkg := filepath.Join(modules, "@bufbuild", "protoc-gen-es")
	x.NoError(os.MkdirAll(filepath.Join(pkg, "bin"), 0o755))
	x.NoError(os.WriteFile(filepath.Join(pkg, "package.json"),
		[]byte(`{"name":"@bufbuild/protoc-gen-es","version":"`+version+`"}`), 0o644))
	x.NoError(os.WriteFile(filepath.Join(pkg, "bin", "protoc-gen-es"), []byte("#!/bin/sh\n"), 0o755))

	bin := filepath.Join(modules, ".bin")
	x.NoError(os.MkdirAll(bin, 0o755))
	x.NoError(os.Symlink(filepath.Join("..", "@bufbuild", "protoc-gen-es", "bin", "protoc-gen-es"), filepath.Join(bin, "protoc-gen-es")))

	return filepath.Join(bin, "protoc-gen-es")
}

// older is the page a long-lived app has: a TypeScript, a protobuf runtime and
// a payday from before the floors, and the rest at or above them.
func older(t *testing.T) pdcli.Layout {
	return page(t, app(t, "github.com/acme/thing"),
		map[string]string{
			"@bufbuild/protobuf":  "^2.10.0",
			"@connectrpc/connect": "^2.1.2",
			"@lesomnus/payday":    "^0.1.1",
		},
		map[string]string{
			"@bufbuild/protoc-gen-es": "^2.10.0",
			"typescript":              "~5.8.3",
			"vite":                    "^7.2.0",
			"@vitejs/plugin-react":    "^5.0.4",
		},
		map[string]string{
			"@bufbuild/protobuf":      "2.10.0",
			"@bufbuild/protoc-gen-es": "2.16.0",
			"@connectrpc/connect":     "2.2.0",
			"@lesomnus/payday":        "0.1.1",
			"typescript":              "5.8.3",
			"vite":                    "7.3.7",
			"@vitejs/plugin-react":    "5.2.0",
		})
}

// TestEveryFloorIsOneTheTemplateMovesTo: every package with a floor is one the
// page `pd new` writes declares, at a version no older than the floor -- an
// upgrade moves a package to the template's -- and the packages that move
// together say so of each other.
func TestEveryFloorIsOneTheTemplateMovesTo(t *testing.T) {
	x := require.New(t)

	targets, err := pdcli.Targets()
	x.NoError(err)

	named := map[string]pdcli.Floor{}
	for _, f := range pdcli.Floors {
		named[f.Name] = f
	}
	for _, f := range pdcli.Floors {
		x.True(semver.IsValid("v"+f.Minimum), "%s: %s", f.Name, f.Minimum)
		x.NotEmpty(f.Why, f.Name)

		target, ok := targets[f.Name]
		x.True(ok, "%s has a floor and the template does not declare it", f.Name)
		low := (pdcli.Have{Range: target.Range}).Version()
		x.NotEmpty(low, "%s: %s", f.Name, target.Range)
		x.GreaterOrEqual(semver.Compare("v"+low, "v"+f.Minimum), 0, "%s: the template's %s is older than the floor %s", f.Name, target.Range, f.Minimum)

		for _, w := range f.With {
			partner, ok := named[w]
			x.True(ok, "%s moves with %s, which has no floor", f.Name, w)
			x.Contains(partner.With, f.Name, "%s moves with %s and not the other way round", f.Name, w)
		}
	}
	x.True(semver.IsValid("v" + pdcli.NodeFloor))
}

// TestTheVersionIsTheOneInstalled: a package is held to its floor by what is
// installed -- the lockfile, then node_modules -- and by the lowest its range
// admits only when nothing is.
func TestTheVersionIsTheOneInstalled(t *testing.T) {
	x := require.New(t)

	l := page(t, app(t, "github.com/acme/thing"), nil,
		map[string]string{"typescript": "^5.0.0", "vite": "^7.2.0", "@vitejs/plugin-react": "latest"},
		map[string]string{"typescript": "5.9.3"})
	x.NoError(os.MkdirAll(filepath.Join(l.Root, "ts", "node_modules", "vite"), 0o755))
	x.NoError(os.WriteFile(filepath.Join(l.Root, "ts", "node_modules", "vite", "package.json"), []byte(`{"version":"7.2.0"}`), 0o644))

	have, ok, err := pdcli.Page(l)
	x.NoError(err)
	x.True(ok)
	x.Equal("5.9.3", have["typescript"].Version(), "the lockfile")
	x.Equal("7.2.0", have["vite"].Version(), "node_modules, with no lockfile entry")
	x.Empty(have["@vitejs/plugin-react"].Version(), "a range that says no version")

	x.Equal("1.2.3", (pdcli.Have{Range: "~1.2.3"}).Version())
	x.Equal("1.2.3", (pdcli.Have{Range: ">=1.2.3"}).Version())

	vs, err := pdcli.Upgrades(l)
	x.NoError(err)
	x.Empty(vs, "nothing is below a floor, and a version nobody can read is not guessed at")
}

// TestAnUpgradeMovesWhatIsBelowItsFloorToItsFloor, in the major it is in and
// the section the page had it in -- and nothing else. TypeScript 5.8 is below
// 5.9.3 and goes to ^5.9.3, not to the 7 `pd new` writes: a major is a
// migration, and the app's to make.
func TestAnUpgradeMovesWhatIsBelowItsFloorToItsFloor(t *testing.T) {
	x := require.New(t)

	vs, err := pdcli.Upgrades(older(t))
	x.NoError(err)

	got := map[string]pdcli.Step{}
	for _, v := range vs {
		got[v.Name] = v
	}
	x.Len(got, 3, "%+v", vs)

	x.Equal("dependencies", got["@bufbuild/protobuf"].Section)
	x.Equal("2.10.0", got["@bufbuild/protobuf"].From)
	x.Equal("^2.12.1", got["@bufbuild/protobuf"].To)
	x.False(got["@bufbuild/protobuf"].Partner)

	x.Equal("devDependencies", got["typescript"].Section)
	x.Equal("^5.9.3", got["typescript"].To, "the floor, in TypeScript 5")
	x.False(got["typescript"].Partner)

	// What the template's store.ts calls was 0.1.2's, so a page from before it
	// moves there -- and no further, though `pd new` writes a later one.
	x.Equal("dependencies", got["@lesomnus/payday"].Section)
	x.Equal("0.1.1", got["@lesomnus/payday"].From)
	x.Equal("^0.1.2", got["@lesomnus/payday"].To)
	x.False(got["@lesomnus/payday"].Partner)

	_, moved := got["@bufbuild/protoc-gen-es"]
	x.False(moved, "above its own floor, and in the same major as the runtime that moves")
	_, moved = got["vite"]
	x.False(moved, "above its floor, so Renovate's to move")
}

// TestAnUpgradeCrossesAMajorOnlyToTheTemplates: a package whose floor is a
// later major than the one it is in has a major to cross anyway, so it goes to
// what `pd new` writes -- and so does every package it moves with, since a
// major of one is a major of the others. One still in its floor's major goes
// no further than the floor.
func TestAnUpgradeCrossesAMajorOnlyToTheTemplates(t *testing.T) {
	x := require.New(t)

	l := page(t, app(t, "github.com/acme/thing"),
		map[string]string{"@lesomnus/payday": "^0.0.5"},
		map[string]string{
			"typescript":           "^4.9.5",
			"vite":                 "^6.3.0",
			"@vitejs/plugin-react": "^5.2.0",
		},
		map[string]string{
			"@lesomnus/payday":     "0.0.5",
			"typescript":           "4.9.5",
			"vite":                 "6.3.0",
			"@vitejs/plugin-react": "5.2.0",
		})

	vs, err := pdcli.Upgrades(l)
	x.NoError(err)
	got := map[string]pdcli.Step{}
	for _, v := range vs {
		got[v.Name] = v
	}
	x.Len(got, 4, "%+v", vs)

	targets, err := pdcli.Targets()
	x.NoError(err)
	x.Equal(targets["typescript"].Range, got["typescript"].To, "TypeScript 4 is below 5.9.3, a later major")
	x.Equal(targets["vite"].Range, got["vite"].To, "Vite 6 is below 7.2.0, a later major")
	x.Equal(targets["@vitejs/plugin-react"].Range, got["@vitejs/plugin-react"].To, "with Vite")
	x.True(got["@vitejs/plugin-react"].Partner, "5.2.0 is above its own floor, and moves only because Vite does")
	x.Equal("^0.1.2", got["@lesomnus/payday"].To, "0.0.5 is below 0.1.2, in the same major")
}

// TestAnUpgradeIsOneResolution: what moves is set in package.json by npm, each
// range in the section the page has it in, and then installed by one `npm
// install` -- so a pair is resolved together even when its halves are in two
// sections, as the protobuf runtime, a dependency, and its generator, a dev
// dependency, are.
func TestAnUpgradeIsOneResolution(t *testing.T) {
	x := require.New(t)
	l := page(t, app(t, "github.com/acme/thing"),
		map[string]string{"@bufbuild/protobuf": "^2.10.0"},
		map[string]string{"@bufbuild/protoc-gen-es": "^2.10.0", "typescript": "~5.8.3"},
		map[string]string{"@bufbuild/protobuf": "2.10.0", "@bufbuild/protoc-gen-es": "2.10.0", "typescript": "5.8.3"})
	npm := fake(t, "npm")

	var log bytes.Buffer
	x.NoError(pdcli.Upgrade{Layout: l, Log: &log}.Run(context.Background()))

	dir := filepath.Join(l.Root, "ts")
	x.Equal([]string{
		dir + " pkg set dependencies[@bufbuild/protobuf]=^2.12.1 devDependencies[@bufbuild/protoc-gen-es]=^2.12.1 devDependencies[typescript]=^5.9.3",
		dir + " install",
	}, npm())
	x.Contains(log.String(), "below 5.9.3", "it says why")
	x.Contains(log.String(), "nothing is committed")

	t.Run("and a dry run runs none of it", func(t *testing.T) {
		x := require.New(t)
		npm := fake(t, "npm")

		var log bytes.Buffer
		x.NoError(pdcli.Upgrade{Layout: l, Log: &log, DryRun: true}.Run(context.Background()))
		x.Empty(npm())
		x.Contains(log.String(), "npm pkg set dependencies[@bufbuild/protobuf]=^2.12.1")
		x.Contains(log.String(), "npm install")
	})

	t.Run("and a page at its floors is left alone", func(t *testing.T) {
		x := require.New(t)
		npm := fake(t, "npm")

		l := page(t, app(t, "github.com/acme/thing"), nil,
			map[string]string{"typescript": "~5.9.3"}, map[string]string{"typescript": "5.9.3"})
		var log bytes.Buffer
		x.NoError(pdcli.Upgrade{Layout: l, Log: &log}.Run(context.Background()))
		x.Empty(npm())
		x.Contains(log.String(), "the page has what this payday needs")
	})
}

// TestAnUpgradeToAnotherPaydayIsThatPaydays: moved to another payday, an app
// meets **its** floors and is generated by it -- so the upgrade and the
// generation after `go get` are `go tool pd`'s, the payday just fetched, and
// not this one's.
func TestAnUpgradeToAnotherPaydayIsThatPaydays(t *testing.T) {
	x := require.New(t)
	l := older(t)
	generator(t, l, "2.16.0")

	goes := fake(t, "go")
	var log bytes.Buffer
	x.NoError(pdcli.Upgrade{Layout: l, Log: &log, Payday: "v0.9.0"}.Run(context.Background()))

	x.Equal([]string{
		l.Root + " get github.com/lesomnus/payday@v0.9.0",
		l.Root + " mod tidy",
		l.Root + " tool pd upgrade " + l.Root,
		l.Root + " tool pd gen --ts " + l.Root,
	}, goes(), "one generation: --ts is the Go half as well")
	x.Contains(log.String(), "the next two are run by the payday `go get` fetched")
	x.Contains(log.String(), "nothing is committed")

	t.Run("and a dry run says the last two are not this payday's", func(t *testing.T) {
		x := require.New(t)
		goes := fake(t, "go")

		var log bytes.Buffer
		x.NoError(pdcli.Upgrade{Layout: l, Log: &log, Payday: "v0.9.0", DryRun: true}.Run(context.Background()))
		x.Empty(goes())
		x.Contains(log.String(), "the next two are run by the payday `go get` fetches, not by this one")
		x.Contains(log.String(), "a dry run fetches nothing")
		x.Contains(log.String(), "go tool pd upgrade "+l.Root, "what a run would run")
		x.NotContains(log.String(), "--dry-run", "it is not passed on to a step that does not run")
	})

	t.Run("and a page is generated with --ts because it is a page", func(t *testing.T) {
		x := require.New(t)
		l := page(t, app(t, "github.com/acme/thing"), nil,
			map[string]string{"@bufbuild/protoc-gen-es": "^2.16.0", "typescript": "~5.9.3"}, nil)

		goes := fake(t, "go")
		err := pdcli.Upgrade{Layout: l, Payday: "v0.9.0"}.Run(context.Background())
		x.ErrorIs(err, pdcli.ErrNoTsPlugin, "its generator is not installed, so it is refused")
		x.Contains(err.Error(), "npm ci", "the page declares it, so installing what it declares is the fix")
		x.Empty(goes(), "before anything moved")

		generator(t, l, "2.16.0")
		x.NoError(pdcli.Upgrade{Layout: l, Payday: "v0.9.0"}.Run(context.Background()))
		x.Equal(l.Root+" tool pd gen --ts "+l.Root, goes()[3])
	})

	t.Run("and an app with no page is generated without --ts", func(t *testing.T) {
		x := require.New(t)
		l, err := pdcli.Discover(app(t, "github.com/acme/thing"))
		x.NoError(err)

		goes := fake(t, "go")
		x.NoError(pdcli.Upgrade{Layout: l, Payday: "v0.9.0"}.Run(context.Background()))
		x.Equal(l.Root+" tool pd gen "+l.Root, goes()[3])
	})
}

// TestTheGeneratorBelowItsFloorIsRefused: `pd gen --ts` writes nothing with a
// protoc-gen-es, or against a runtime, older than payday builds with -- and is
// not held to the rest of the floors, which are the page's own build.
func TestTheGeneratorBelowItsFloorIsRefused(t *testing.T) {
	x := require.New(t)
	l := older(t)
	plugin := generator(t, l, "2.16.0")

	vs, err := pdcli.Below(l, plugin)
	x.NoError(err)
	x.Len(vs, 1, "%+v", vs)
	x.Equal("@bufbuild/protobuf", vs[0].Name, "not its partner, which is above its own, and not typescript")

	err = pdcli.Gen{Layout: l}.Ts(context.Background())
	x.ErrorIs(err, pdcli.ErrBelowFloor)
	x.Contains(err.Error(), "@bufbuild/protobuf 2.10.0 is older than 2.12.1")
	x.Contains(err.Error(), "go tool pd upgrade")
}

// TestTheGeneratorIsTheOneThatRuns: the generator is held to its floor by the
// version of its own package -- the plugin that writes the code -- and not by
// the lockfile, which says what the next install makes. A lockfile Renovate
// moved and a node_modules nobody reinstalled is the case it is for.
func TestTheGeneratorIsTheOneThatRuns(t *testing.T) {
	x := require.New(t)

	l := page(t, app(t, "github.com/acme/thing"),
		map[string]string{"@bufbuild/protobuf": "^2.16.0"},
		map[string]string{"@bufbuild/protoc-gen-es": "^2.16.0"},
		map[string]string{"@bufbuild/protobuf": "2.16.0", "@bufbuild/protoc-gen-es": "2.16.0"})
	plugin := generator(t, l, "2.10.0")

	v, err := pdcli.PluginVersion(plugin)
	x.NoError(err)
	x.Equal("2.10.0", v)

	err = pdcli.Gen{Layout: l}.Ts(context.Background())
	x.ErrorIs(err, pdcli.ErrBelowFloor, "the lockfile says 2.16.0; the plugin is 2.10.0")
	x.Contains(err.Error(), "@bufbuild/protoc-gen-es 2.10.0 is older than 2.12.1")

	t.Run("and the other way round", func(t *testing.T) {
		x := require.New(t)
		l := page(t, app(t, "github.com/acme/thing"),
			map[string]string{"@bufbuild/protobuf": "^2.16.0"},
			map[string]string{"@bufbuild/protoc-gen-es": "^2.10.0"},
			map[string]string{"@bufbuild/protobuf": "2.16.0", "@bufbuild/protoc-gen-es": "2.10.0"})

		vs, err := pdcli.Below(l, generator(t, l, "2.16.0"))
		x.NoError(err)
		x.Empty(vs, "the plugin that runs is 2.16.0, whatever the lockfile says")
	})

	t.Run("and a .bin entry that is a script, as pnpm writes it", func(t *testing.T) {
		x := require.New(t)
		l := page(t, app(t, "github.com/acme/thing"), nil, nil, nil)
		modules := filepath.Join(l.Root, "ts", "node_modules")
		pkg := filepath.Join(modules, "@bufbuild", "protoc-gen-es")
		x.NoError(os.MkdirAll(pkg, 0o755))
		x.NoError(os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@bufbuild/protoc-gen-es","version":"2.13.0"}`), 0o644))
		x.NoError(os.MkdirAll(filepath.Join(modules, ".bin"), 0o755))
		shim := filepath.Join(modules, ".bin", "protoc-gen-es")
		x.NoError(os.WriteFile(shim, []byte("#!/bin/sh\nexec node ../.pnpm/x/bin \"$@\"\n"), 0o755))

		v, err := pdcli.PluginVersion(shim)
		x.NoError(err)
		x.Equal("2.13.0", v, "the package of that name in the same node_modules")
	})

	t.Run("and a plugin that is part of no package is not guessed at", func(t *testing.T) {
		x := require.New(t)
		bin := filepath.Join(t.TempDir(), "node_modules", ".bin")
		x.NoError(os.MkdirAll(bin, 0o755))
		x.NoError(os.WriteFile(filepath.Join(bin, "protoc-gen-es"), []byte("#!/bin/sh\n"), 0o755))

		_, err := pdcli.PluginVersion(filepath.Join(bin, "protoc-gen-es"))
		x.ErrorIs(err, pdcli.ErrNoTsPlugin)
	})
}
