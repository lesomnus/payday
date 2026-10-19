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

// older is the page a long-lived app has: a TypeScript and a protobuf runtime
// from before the floors, and the rest at or above them.
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

// TestAnUpgradeMovesWhatIsBelowItsFloor, and what moves with it, to the
// template's version, in the section the page had it in -- and nothing else.
func TestAnUpgradeMovesWhatIsBelowItsFloor(t *testing.T) {
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
	x.False(got["@bufbuild/protobuf"].Partner)

	x.True(got["@bufbuild/protoc-gen-es"].Partner, "the generator moves with the runtime it was below with")
	x.Equal("devDependencies", got["@bufbuild/protoc-gen-es"].Section)

	targets, err := pdcli.Targets()
	x.NoError(err)
	x.Equal(targets["typescript"].Range, got["typescript"].To, "the template's")
	x.False(got["typescript"].Partner)

	_, moved := got["vite"]
	x.False(moved, "above its floor, so Renovate's to move")
}

// TestAnUpgradeIsOneInstallForEachSection: what moves is installed by npm, in
// the page's directory, one install for each section so that a pair is resolved
// together and a dev dependency stays one.
func TestAnUpgradeIsOneInstallForEachSection(t *testing.T) {
	x := require.New(t)
	l := older(t)
	npm := fake(t, "npm")

	var log bytes.Buffer
	x.NoError(pdcli.Upgrade{Layout: l, Log: &log}.Run(context.Background()))

	targets, err := pdcli.Targets()
	x.NoError(err)
	dir := filepath.Join(l.Root, "ts")
	x.Equal([]string{
		dir + " install @bufbuild/protobuf@" + targets["@bufbuild/protobuf"].Range,
		dir + " install --save-dev @bufbuild/protoc-gen-es@" + targets["@bufbuild/protoc-gen-es"].Range + " typescript@" + targets["typescript"].Range,
	}, npm())
	x.Contains(log.String(), "below 5.9.3", "it says why")
	x.Contains(log.String(), "nothing is committed")

	t.Run("and a dry run runs none of it", func(t *testing.T) {
		x := require.New(t)
		npm := fake(t, "npm")

		var log bytes.Buffer
		x.NoError(pdcli.Upgrade{Layout: l, Log: &log, DryRun: true}.Run(context.Background()))
		x.Empty(npm())
		x.Contains(log.String(), "npm install --save-dev")
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

	// A generator to find, so the page is generated as well.
	bin := filepath.Join(l.Root, "ts", "node_modules", ".bin")
	x.NoError(os.MkdirAll(bin, 0o755))
	x.NoError(os.WriteFile(filepath.Join(bin, "protoc-gen-es"), []byte("#!/bin/sh\n"), 0o755))

	goes := fake(t, "go")
	var log bytes.Buffer
	x.NoError(pdcli.Upgrade{Layout: l, Log: &log, Payday: "v0.9.0"}.Run(context.Background()))

	x.Equal([]string{
		l.Root + " get github.com/lesomnus/payday@v0.9.0",
		l.Root + " mod tidy",
		l.Root + " tool pd upgrade " + l.Root,
		l.Root + " tool pd gen --ts " + l.Root,
	}, goes(), "one generation: --ts is the Go half as well")
	x.Contains(log.String(), "nothing is committed")

	t.Run("and one whose page has no generator is generated without --ts", func(t *testing.T) {
		x := require.New(t)
		l := page(t, app(t, "github.com/acme/thing"), nil, map[string]string{"typescript": "~5.9.3"}, nil)

		goes := fake(t, "go")
		x.NoError(pdcli.Upgrade{Layout: l, Payday: "latest"}.Run(context.Background()))
		x.Equal(l.Root+" tool pd gen "+l.Root, goes()[3])
	})
}

// TestTheGeneratorBelowItsFloorIsRefused: `pd gen --ts` writes nothing with a
// protoc-gen-es, or against a runtime, older than payday builds with -- and is
// not held to the rest of the floors, which are the page's own build.
func TestTheGeneratorBelowItsFloorIsRefused(t *testing.T) {
	x := require.New(t)
	l := older(t)

	vs, err := pdcli.Below(l)
	x.NoError(err)
	x.Len(vs, 1, "%+v", vs)
	x.Equal("@bufbuild/protobuf", vs[0].Name, "not its partner, which is above its own, and not typescript")

	bin := filepath.Join(l.Root, "ts", "node_modules", ".bin")
	x.NoError(os.MkdirAll(bin, 0o755))
	x.NoError(os.WriteFile(filepath.Join(bin, "protoc-gen-es"), []byte("#!/bin/sh\n"), 0o755))

	err = pdcli.Gen{Layout: l}.Ts(context.Background())
	x.ErrorIs(err, pdcli.ErrBelowFloor)
	x.Contains(err.Error(), "@bufbuild/protobuf 2.10.0 is older than 2.12.1")
	x.Contains(err.Error(), "go tool pd upgrade")
}
