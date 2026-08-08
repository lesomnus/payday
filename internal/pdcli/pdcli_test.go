package pdcli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// app writes the smallest tree [pdcli.Discover] will take, and answers with it.
func app(t *testing.T, module string) string {
	t.Helper()
	x := require.New(t)

	root := t.TempDir()
	x.NoError(os.MkdirAll(filepath.Join(root, "proto", "app"), 0o755))
	x.NoError(os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module "+module+"\n\ngo 1.25\n"), 0o644))
	x.NoError(os.WriteFile(filepath.Join(root, "buf.yaml"),
		[]byte("version: v2\nmodules:\n  - path: ./proto\n    excludes:\n      - proto/ext\ndeps:\n  - buf.build/payday/payday:dev\n  - buf.build/orm/orm\n  - buf.build/patch/patch\n"), 0o644))

	return root
}

// TestOverlaysAreKeptFromBuf is the one line of `buf.yaml` that is payday's
// shape rather than the app's choice.
//
// The overlays live under `proto/` now, which is where buf looks. An overlay is
// not a file that compiles -- it names messages that exist only after the merge
// -- so a module that does not exclude them is one where `pd gen` fails on its
// first step with an error about a type nobody declared.
func TestOverlaysAreKeptFromBuf(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	x.NoError(os.MkdirAll(filepath.Join(root, pdcli.DirExt, "app"), 0o755))
	x.NoError(os.WriteFile(filepath.Join(root, "buf.yaml"),
		[]byte("version: v2\nmodules:\n  - path: ./proto\ndeps:\n  - buf.build/payday/payday:dev\n  - buf.build/orm/orm\n"), 0o644))

	l, err := pdcli.Discover(root)
	x.NoError(err)

	var said string
	for _, v := range pdcli.Doctor(t.Context(), l) {
		said += v.String() + "\n"
	}
	x.Contains(said, "does not exclude proto/ext")
	x.Contains(said, "excludes:")
}

// TestOverlaysAreOnlyMentionedWhenThereAreSome, since an app with none has
// nothing to exclude and should not be told to write a line about it.
func TestOverlaysAreOnlyMentionedWhenThereAreSome(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	x.NoError(os.WriteFile(filepath.Join(root, "buf.yaml"),
		[]byte("version: v2\nmodules:\n  - path: ./proto\ndeps:\n  - buf.build/payday/payday:dev\n  - buf.build/orm/orm\n"), 0o644))

	l, err := pdcli.Discover(root)
	x.NoError(err)

	var said string
	for _, v := range pdcli.Doctor(t.Context(), l) {
		said += v.String() + "\n"
	}
	x.NotContains(said, "exclude")
}

// TestTheGeneratedMarkerStaysInTheSchema is what [pdcli.DirProto] promises.
//
// A contract is `robot_svc.g.proto`, and every generator downstream names its
// output after the file it read -- so without taking it back off, the app root
// holds `robot_svc.g_grpc.pb.go` and `ts/gen` holds a `robot_svc.g_pb.ts` that
// somebody has to type in an import. The marker says "generated from something
// you wrote", which is as true of those as of the contract, so carried along it
// says nothing and is only in the way.
//
// It is checked against the app rather than by calling the function, because
// what is claimed is about the tree that is committed.
func TestTheGeneratedMarkerStaysInTheSchema(t *testing.T) {
	x := require.New(t)

	root := filepath.Join("..", "apptest")
	proto := filepath.Join(root, pdcli.DirProto)

	x.NoError(filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}

			return nil
		}
		if strings.HasPrefix(p, proto) {
			return nil
		}

		x.NotContains(d.Name(), "_svc.g", "%s: the `.g` of a contract left the schema", p)

		return nil
	}))

	// And that the marker is there to begin with, so this does not pass by
	// there being nothing to find.
	vs, err := filepath.Glob(filepath.Join(proto, "*", "*_svc.g.proto"))
	x.NoError(err)
	x.NotEmpty(vs)
}

func TestTheLayoutIsReadAndNotGuessed(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	l, err := pdcli.Discover(root)
	x.NoError(err)
	x.Equal("github.com/acme/thing", l.Module)
	x.Equal(root, l.Root)
	x.Equal(root, l.Work)
}

// TestAnAppBelowItsModuleCarriesThePath is the case that made this read files
// rather than take a flag.
//
// The generated code declares `go_package`, and everything about whether the
// stack compiles is whether that is the import path the files actually land at.
// A guess is written into every generated file and found out three commands
// later as an import cycle.
func TestAnAppBelowItsModuleCarriesThePath(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	deep := filepath.Join(root, "internal", "sub")
	x.NoError(os.MkdirAll(filepath.Join(deep, "proto"), 0o755))

	l, err := pdcli.Discover(deep)
	x.NoError(err)
	x.Equal("github.com/acme/thing/internal/sub", l.Module)
	x.Equal(deep, l.Root)

	// And buf is run from where the workspace begins, not from the app.
	x.Equal(root, l.Work)
	x.Equal("internal/sub/proto", l.Rel(pdcli.DirProto))
}

func TestSomewhereThatIsNotAnAppIsRefused(t *testing.T) {
	x := require.New(t)

	// No proto/.
	root := t.TempDir()
	x.NoError(os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644))
	_, err := pdcli.Discover(root)
	x.ErrorIs(err, pdcli.ErrNoApp)

	// No go.mod, so no module path, so nothing to write into what is generated.
	bare := t.TempDir()
	x.NoError(os.MkdirAll(filepath.Join(bare, "proto"), 0o755))
	_, err = pdcli.Discover(bare)
	x.ErrorIs(err, pdcli.ErrNoApp)
}

// TestEveryPluginRunsOverEverything is the reason payday writes the templates
// instead of an app keeping them.
//
// buf's default strategy runs a plugin once per directory. An entity whose
// tenant is declared in another one is then not a generation target when its
// own file is read -- so the wall is generated with a hole in it, everything
// compiles, and nothing says a word. It is not a preference and there is no app
// for which the other answer is right, which is exactly what makes it payday's
// to decide rather than the app's to remember.
func TestEveryPluginRunsOverEverything(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	l, err := pdcli.Discover(root)
	x.NoError(err)

	for _, v := range pdcli.Templates(l, root) {
		var tmpl struct {
			Plugins []struct {
				Local    []string `yaml:"local"`
				Opt      []string `yaml:"opt"`
				Strategy string   `yaml:"strategy"`
			} `yaml:"plugins"`
		}
		x.NoError(yaml.Unmarshal([]byte(v), &tmpl))
		x.NotEmpty(tmpl.Plugins)

		for _, p := range tmpl.Plugins {
			name := strings.Join(p.Local, " ")
			x.Equal("all", p.Strategy, "%s runs per directory", name)
		}
	}
}

// TestEverythingLandsInOneGoPackage is the other half of the same rule.
//
// Two packages means two sets of ent schemas and no edge between them, and the
// wall is an edge. It is checked by reading the template rather than by
// generating, because the failure is not a compile error in the general case --
// it is a wall that narrows nothing.
func TestEverythingLandsInOneGoPackage(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	l, err := pdcli.Discover(root)
	x.NoError(err)

	// The pass that writes Go is the one with `module=`; the contracts pass
	// writes .proto and has none.
	var seen int
	for _, v := range pdcli.Templates(l, root) {
		for _, line := range strings.Split(v, "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
			if !strings.HasPrefix(line, "module=") {
				continue
			}

			seen++
			x.Equal("module=github.com/acme/thing", line)
		}
	}
	x.Equal(5, seen, "a plugin that writes Go without a module= would land somewhere else")
}

// TestDoctorSaysWhatIsMissingBeforeBufDoes.
func TestDoctorSaysWhatIsMissingBeforeBufDoes(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	x.NoError(os.WriteFile(filepath.Join(root, "buf.yaml"),
		[]byte("version: v2\nmodules:\n  - path: ./proto\n"), 0o644))

	l, err := pdcli.Discover(root)
	x.NoError(err)

	vs := pdcli.Doctor(t.Context(), l)
	x.NotEmpty(vs)

	var said string
	for _, v := range vs {
		said += v.String() + "\n"
	}

	// The dependency every payday schema imports, missing.
	x.Contains(said, "buf.build/payday/payday")

	// And the generators, which this temporary module has none of. The fix is
	// what to type rather than a description of the problem.
	x.Contains(said, "go get -tool")
}

// TestAnOverlayForNothingIsFound is the one that would otherwise be silent.
//
// An overlay is merged by name. One named after an entity payday does not have
// is never merged and never mentioned -- the app's extra field simply is not
// there, and the first sign of it is a column that does not exist.
func TestAnOverlayForNothingIsFound(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	dir := filepath.Join(root, pdcli.DirExt, "payday")
	x.NoError(os.MkdirAll(dir, 0o755))
	x.NoError(os.WriteFile(filepath.Join(dir, "holdr.ext.proto"), []byte("edition = \"2023\";\n"), 0o644))

	l, err := pdcli.Discover(root)
	x.NoError(err)

	var said string
	for _, v := range pdcli.Doctor(t.Context(), l) {
		said += v.String() + "\n"
	}
	x.Contains(said, "holdr.ext.proto")
	x.Contains(said, "never merged")

	// And it says what payday does have, since the cause is almost always a
	// typo in the name.
	x.Contains(said, "holder")
}

// TestAnOverlayDoesNotChangeWhatTheFileIs is the one thing an overlay can say
// that is not about what it adds.
//
// The merge unions the file's options, so a `features.field_presence = IMPLICIT`
// copied out of the entity file lands on the **whole contract** -- every field
// of every message in it, including the ones the generator wrote. It was found
// by writing the first hand-written RPC in an app: it took `HasId` off an Add
// request and stopped the build, which is the lucky version. On a field nothing
// calls `Has` on it would change only what "not set" means on the wire.
func TestAnOverlayDoesNotChangeWhatTheFileIs(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	x.NoError(os.MkdirAll(filepath.Join(root, pdcli.DirExt, "app"), 0o755))

	p := filepath.Join(root, pdcli.DirExt, "app", "thing_svc.ext.proto")
	x.NoError(os.WriteFile(p, []byte(`edition = "2023";

package app;

option features.field_presence = IMPLICIT;

service ThingService {
  rpc Ping(PingRequest) returns (PingRequest);
}

message PingRequest {
  string what = 1;
}
`), 0o644))

	err := pdcli.CheckOverlayFile(p)
	x.Error(err)
	x.ErrorContains(err, "the whole contract")
	x.ErrorContains(err, "Delete the line")
}

// TestAnOverlayMaySayItInsideAMessage, which is about that message and is the
// app's to say.
func TestAnOverlayMaySayItInsideAMessage(t *testing.T) {
	x := require.New(t)

	root := app(t, "github.com/acme/thing")
	x.NoError(os.MkdirAll(filepath.Join(root, pdcli.DirExt, "app"), 0o755))

	p := filepath.Join(root, pdcli.DirExt, "app", "thing_svc.ext.proto")
	x.NoError(os.WriteFile(p, []byte(`edition = "2023";

package app;

message PingRequest {
  option features.field_presence = IMPLICIT;

  string what = 1;
}
`), 0o644))

	x.NoError(pdcli.CheckOverlayFile(p))
}
