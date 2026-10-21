package pdcli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

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
// upgrade and generation: its floors are the ones to meet, and the code an app
// generates has to be its generator's -- a runtime refuses code another
// payday wrote.
func (u Upgrade) payday(ctx context.Context) error {
	dir := u.Layout.Root

	// One generation: `--ts` is the Go half and then the page's, so an app
	// with a page asks for that alone rather than writing the Go half twice.
	// Whether there is a page is the page's to say, and not whether its
	// generator happens to be installed: without one, the Go half moved to
	// the new payday and the page's stayed the old one's. So a page whose
	// generator is not installed is refused here, before anything moves.
	have, page, err := Page(u.Layout)
	if err != nil {
		return err
	}
	gen := []string{"tool", "pd", "gen"}
	if page {
		if _, err := TsPlugin(u.Layout); err != nil {
			fix := "npm install --save-dev " + tsPluginPackage
			if _, ok := have[tsPluginPackage]; ok {
				fix = "npm ci"
			}
			return fmt.Errorf("%w: the page is generated with the rest, so its generator has to be installed first -- %s, in %s",
				ErrNoTsPlugin, fix, u.Layout.Rel(DirTs))
		}
		gen = append(gen, "--ts")
	}

	if err := u.exec(ctx, dir, "go", "get", version.Module+"@"+u.Payday); err != nil {
		return err
	}
	if err := u.exec(ctx, dir, "go", "mod", "tidy"); err != nil {
		return err
	}

	// The rest is the payday `go get` fetched, which is not the one running:
	// `go tool pd` is whatever go.mod now names. A dry run fetches nothing, so
	// it can say what will be run but not what that payday will move or
	// write, and it says so rather than pass `--dry-run` on to a step it does
	// not run either.
	if u.DryRun {
		u.say("pd: the next two are run by the payday `go get` fetches, not by this one, and a dry run fetches nothing: what they would move and write is that payday's, and is not shown here")
	} else {
		u.say("pd: the next two are run by the payday `go get` fetched: its floors, and its generator")
	}
	if err := u.exec(ctx, dir, "go", "tool", "pd", "upgrade", dir); err != nil {
		return err
	}
	if err := u.exec(ctx, dir, "go", append(gen, dir)...); err != nil {
		return err
	}

	if !u.DryRun {
		u.say("pd: nothing is committed; `git diff` says what moved")
	}

	return nil
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
