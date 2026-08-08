package pdcli

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Changed is one file a generation was not in step with.
type Changed struct {
	// Path is relative to the app.
	Path string

	// How is what happened to it: written, rewritten, or left behind by an
	// entity that is no longer in the schema.
	How string
}

const (
	Added  = "not generated"
	Stale  = "out of date"
	Orphan = "no longer generated"
)

// Check generates and answers with what was not already there.
//
// It runs the same generation the real one does rather than a second
// implementation of it, and that is the only way a check is worth anything: one
// that reimplemented the pipeline would drift from it, and the day it drifted
// is the day it stops catching what it exists to catch.
//
// So it **writes**. A tree that was out of step is in step afterwards, which is
// what somebody running this wanted either way -- in CI it is the failure that
// matters, and at a desk it is the fix. What it answers with is what moved,
// taken by hashing before and after rather than by asking git, so it works in a
// tree that was never a repository.
func (g Gen) Check(ctx context.Context) ([]Changed, error) {
	if g.Out == "" {
		g.Out = g.Layout.Root
	}

	before, err := g.hashes()
	if err != nil {
		return nil, err
	}

	if err := g.Run(ctx); err != nil {
		return nil, err
	}

	after, err := g.hashes()
	if err != nil {
		return nil, err
	}

	var vs []Changed
	for p, h := range after {
		switch was, ok := before[p]; {
		case !ok:
			vs = append(vs, Changed{Path: p, How: Added})
		case was != h:
			vs = append(vs, Changed{Path: p, How: Stale})
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			vs = append(vs, Changed{Path: p, How: Orphan})
		}
	}

	sort.Slice(vs, func(i, j int) bool { return vs[i].Path < vs[j].Path })

	return vs, nil
}

// generated is every place a generation writes.
//
// It is written out rather than derived from what the last run produced,
// because a file left behind by an entity that was removed from the schema is
// exactly what a check has to notice -- and it is not in any list the current
// run makes.
var generated = []string{
	filepath.Join(DirProto, "payday"),
	DirEnt,
	DirBare,
	DirPd_,
}

// hashes is the content of everything a generation writes, by path.
func (g Gen) hashes() (map[string]string, error) {
	vs := map[string]string{}

	add := func(p string) error {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(g.Out, p)
		if err != nil {
			return err
		}

		vs[filepath.ToSlash(rel)] = hash(b)

		return nil
	}

	for _, d := range generated {
		root := filepath.Join(g.Out, d)
		err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					// A directory that is not there yet is not a failure: the
					// first generation of an app makes all of them.
					return nil
				}

				return err
			}
			if e.IsDir() {
				return nil
			}

			return add(p)
		})
		if err != nil {
			return nil, err
		}
	}

	// And the generated files that sit beside the app's own rather than in a
	// directory of their own. Under `proto/` that is exactly the `.g.proto`,
	// which is the whole of why they are named that way.
	for _, pat := range []string{"*.pb.go", "*.g.go", filepath.Join(DirProto, "*", "*.g.proto")} {
		ps, err := filepath.Glob(filepath.Join(g.Out, pat))
		if err != nil {
			return nil, err
		}

		slices.Sort(ps)
		for _, p := range ps {
			if err := add(p); err != nil {
				return nil, err
			}
		}
	}

	return vs, nil
}

func hash(b []byte) string {
	v := sha256.Sum256(b)

	const hex = "0123456789abcdef"
	var sb strings.Builder
	for _, c := range v[:8] {
		sb.WriteByte(hex[c>>4])
		sb.WriteByte(hex[c&0x0f])
	}

	return sb.String()
}

// CheckTs is [Gen.Check] for the TypeScript half.
func (g Gen) CheckTs(ctx context.Context) ([]Changed, error) {
	if g.Out == "" {
		g.Out = g.Layout.Root
	}

	before, err := g.hashesIn(DirTsGen)
	if err != nil {
		return nil, err
	}

	if err := g.Ts(ctx); err != nil {
		return nil, err
	}

	after, err := g.hashesIn(DirTsGen)
	if err != nil {
		return nil, err
	}

	return diff(before, after), nil
}

// diff is what moved between two readings of the same tree.
func diff(before, after map[string]string) []Changed {
	var vs []Changed
	for p, h := range after {
		switch was, ok := before[p]; {
		case !ok:
			vs = append(vs, Changed{Path: p, How: Added})
		case was != h:
			vs = append(vs, Changed{Path: p, How: Stale})
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			vs = append(vs, Changed{Path: p, How: Orphan})
		}
	}

	sort.Slice(vs, func(i, j int) bool { return vs[i].Path < vs[j].Path })

	return vs
}

// hashesIn is the content of one directory, by path.
func (g Gen) hashesIn(dir string) (map[string]string, error) {
	vs := map[string]string{}

	root := filepath.Join(g.Out, dir)
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}

			return err
		}
		if e.IsDir() {
			return nil
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(g.Out, p)
		if err != nil {
			return err
		}

		vs[filepath.ToSlash(rel)] = hash(b)

		return nil
	})
	if err != nil {
		return nil, err
	}

	return vs, nil
}
