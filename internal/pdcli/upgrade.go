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

	// One install for each section, so that a pair is resolved together, and
	// in the section the page already has each package in: `npm install`
	// without `--save-dev` would move a dev dependency into the bundle's.
	for _, section := range []string{sectionDeps, sectionDev} {
		args := []string{"install"}
		if section == sectionDev {
			args = append(args, "--save-dev")
		}
		n := len(args)
		for _, v := range vs {
			if v.Section == section {
				args = append(args, v.Name+"@"+v.To)
			}
		}
		if len(args) == n {
			continue
		}
		if err := u.exec(ctx, u.Layout.Path(DirTs), "npm", args...); err != nil {
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
	if err := u.exec(ctx, dir, "go", "get", version.Module+"@"+u.Payday); err != nil {
		return err
	}
	if err := u.exec(ctx, dir, "go", "mod", "tidy"); err != nil {
		return err
	}

	args := []string{"tool", "pd", "upgrade"}
	if u.DryRun {
		args = append(args, "--dry-run")
	}
	if err := u.exec(ctx, dir, "go", append(args, dir)...); err != nil {
		return err
	}
	if err := u.exec(ctx, dir, "go", "tool", "pd", "gen", dir); err != nil {
		return err
	}
	if _, err := TsPlugin(u.Layout); err == nil {
		if err := u.exec(ctx, dir, "go", "tool", "pd", "gen", "--ts", dir); err != nil {
			return err
		}
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
