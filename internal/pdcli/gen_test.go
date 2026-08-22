package pdcli_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdcli"
)

// genApp is internal/apptest, copied whole -- committed generated tree and all
// -- into a workspace of its own, where a real generation can run without
// touching this checkout.
//
// Three files written around the copy are what let it run offline. payday's
// own proto module is copied in beside it, because `payday.proto` comes from
// the sibling module of this checkout's buf workspace and a temporary tree has
// no workspace to find it in. The committed buf.lock keeps the rest of the
// graph off the network: `orm` and `patch` resolve from buf's cache by the
// pinned commits. And a go.work naming this checkout is how `go tool` finds
// the generators and ent -- they are tools of payday's module and of the
// app's, and a workspace makes every module's tools runnable from anywhere
// under it.
func genApp(t *testing.T) pdcli.Layout {
	t.Helper()
	if testing.Short() {
		t.Skip("runs a real generation")
	}

	x := require.New(t)

	payday, err := filepath.Abs(filepath.Join("..", ".."))
	x.NoError(err)

	root := t.TempDir()
	app := filepath.Join(root, "app")

	// The app as committed, which a clean generation reproduces byte for byte.
	// `ts/` stays behind: [pdcli.Gen.Run] does not touch it, and it holds a
	// node_modules.
	x.NoError(copyDir(filepath.Join(payday, "internal", "apptest"), app, "ts"))
	x.NoError(copyDir(filepath.Join(payday, "proto"), filepath.Join(root, "payday-proto")))

	// This checkout's own go directive, so the temporary workspace never
	// claims less than the modules it uses.
	b, err := os.ReadFile(filepath.Join(payday, "go.work"))
	x.NoError(err)
	goLine := regexp.MustCompile(`(?m)^go .*$`).Find(b)
	x.NotNil(goLine)

	b, err = os.ReadFile(filepath.Join(payday, "buf.lock"))
	x.NoError(err)

	for name, v := range map[string][]byte{
		"buf.yaml": []byte(`version: v2
modules:
  - path: ./payday-proto
    name: buf.build/payday/payday
  - path: ./app/proto
    excludes:
      - app/proto/ext
deps:
  - buf.build/orm/orm
  - buf.build/patch/patch
`),
		"buf.lock": b,
		"go.work":  []byte(string(goLine) + "\n\nuse (\n\t./app\n\t" + payday + "\n)\n"),
	} {
		x.NoError(os.WriteFile(filepath.Join(root, name), v, 0o644))
	}

	l, err := pdcli.Discover(app)
	x.NoError(err)

	return l
}

// copyDir lays src under dst, leaving behind whatever skip names at the top.
func copyDir(src string, dst string, skip ...string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}

		out := filepath.Join(dst, rel)
		if d.IsDir() {
			if slices.Contains(skip, rel) {
				return fs.SkipDir
			}

			return os.MkdirAll(out, 0o755)
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		return os.WriteFile(out, b, 0o644)
	})
}

// TestAnEditedCopyIsCaughtByCheck.
//
// Everything under `proto/<pkg>/payday/` is rewritten on every generation, and
// the README written beside the copies says an edit lasts until the next one.
// [pdcli.Gen.Check] is that next one: it runs the same generation the real one
// does and answers with what moved, so an edited copy has to come back as "out
// of date" -- CI proves the exit-0 case on a clean tree, and nothing else
// exercises this half of the contract.
func TestAnEditedCopyIsCaughtByCheck(t *testing.T) {
	x := require.New(t)

	l := genApp(t)

	at := filepath.Join(l.Root, l.DirPd(), "holder.proto")
	b, err := os.ReadFile(at)
	x.NoError(err)

	const edit = "// A hand edit, which the README beside this file says will not last.\n"
	x.NoError(os.WriteFile(at, append(b, edit...), 0o644))

	vs, err := pdcli.Gen{Layout: l}.Check(t.Context())
	x.NoError(err)
	x.Contains(vs, pdcli.Changed{
		Path: filepath.ToSlash(filepath.Join(l.DirPd(), "holder.proto")),
		How:  pdcli.Stale,
	})

	// Check **writes** -- a tree that was out of step is in step afterwards --
	// so the edit is not restored, it is overwritten: the file is what the
	// generation says again, and the report is the only trace the edit leaves.
	b, err = os.ReadFile(at)
	x.NoError(err)
	x.NotContains(string(b), edit)
}

// TestAnOverlayNothingMergesIsRefused.
//
// The merge walks the **contracts** and looks up an overlay beside each one, so
// an overlay whose name matches no contract is never visited: it generates
// cleanly, merges nothing, and the first sign is a method that is not there.
// A typo in a filename is the likeliest cause, and it is the one mistake in
// this directory with no symptom until much later.
//
// `doctor` already says this about **entity** overlays, in almost these words.
// This is the same sentence for the other kind, and it is said at generation
// rather than at doctor because that is where it costs nothing to hear.
func TestAnOverlayNothingMergesIsRefused(t *testing.T) {
	x := require.New(t)

	l := genApp(t)

	// Beside a real one, so what is being tested is the name and not the
	// directory: `robot_svc.ext.proto` is merged and this is not.
	at := filepath.Join(l.Path(pdcli.DirExt), "app", "robto_svc.ext.proto")

	b, err := os.ReadFile(filepath.Join(l.Path(pdcli.DirExt), "app", "robot_svc.ext.proto"))
	x.NoError(err)
	x.NoError(os.WriteFile(at, b, 0o644))

	err = pdcli.Gen{Layout: l}.Run(t.Context())
	x.Error(err, "an overlay that reaches nothing generated quietly")
	x.Contains(err.Error(), "robto_svc.ext.proto")
	x.Contains(err.Error(), "never merged")

	t.Run("and the one beside it still is", func(t *testing.T) {
		x := require.New(t)

		x.NoError(os.Remove(at))
		x.NoError(pdcli.Gen{Layout: l}.Run(t.Context()))
	})
}
