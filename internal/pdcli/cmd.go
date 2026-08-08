package pdcli

import (
	"context"
	"fmt"
	"os"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"
)

// NewCmdRoot is `pd`.
func NewCmdRoot() *xli.Command {
	return &xli.Command{
		Name:  "pd",
		Brief: "generate and check a payday app",
		Synop: "pd <command>",

		Commands: []*xli.Command{
			NewCmdGen(),
			NewCmdDoctor(),
		},

		Handler: xli.RequireSubcommand(),
	}
}

// NewCmdGen is `pd gen`.
func NewCmdGen() *xli.Command {
	return &xli.Command{
		Name:  "gen",
		Brief: "generate everything the schema says",
		Synop: "pd gen [--check] [DIR]",

		Flags: flg.Flags{
			&flg.Switch{
				Name:  "check",
				Brief: "answer with what was not already in step, and fail if anything was",
			},
			&flg.Switch{
				Name:  "ts",
				Brief: "also write the TypeScript half: messages and service descriptors",
			},
		},
		Args: arg.Args{
			&arg.String{
				Name:  "DIR",
				Brief: "the app; the working directory by default",
			},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			l, err := discover(cmd)
			if err != nil {
				return err
			}

			ts, _ := flg.Find[bool](cmd, "ts")

			g := Gen{Layout: l, Log: cmd}
			if v, ok := flg.Find[bool](cmd, "check"); !ok || !v {
				if err := g.Run(ctx); err != nil {
					return err
				}
				if ts {
					return g.Ts(ctx)
				}

				return nil
			}

			// Quiet, since the steps are not the answer here.
			g.Log = nil

			vs, err := g.Check(ctx)
			if err != nil {
				return err
			}
			if ts {
				// The TypeScript is checked the same way and by the same run:
				// generated, then compared. It is folded into one answer rather
				// than reported apart, because what a caller wants to know is
				// whether the tree is in step, not which half of it was not.
				more, err := g.CheckTs(ctx)
				if err != nil {
					return err
				}

				vs = append(vs, more...)
			}
			if len(vs) == 0 {
				cmd.Println("pd: the generated code is what the schema says")
				return nil
			}

			for _, v := range vs {
				cmd.Printf("  %-14s %s\n", v.How, v.Path)
			}

			// It has been written, so this is a report rather than a warning of
			// something still to do. What it fails is the build that asked.
			return fmt.Errorf("%d files were not in step with the schema, and are now", len(vs))
		}),
	}
}

// NewCmdDoctor is `pd doctor`.
func NewCmdDoctor() *xli.Command {
	return &xli.Command{
		Name:  "doctor",
		Brief: "say what would go wrong before it does",
		Synop: "pd doctor [DIR]",

		Args: arg.Args{
			&arg.String{
				Name:  "DIR",
				Brief: "the app; the working directory by default",
			},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			l, err := discover(cmd)
			if err != nil {
				return err
			}

			vs := Doctor(ctx, l)
			if len(vs) == 0 {
				cmd.Printf("pd: %s looks like an app that generates\n", l.Module)
				return nil
			}

			var fatal int
			for _, v := range vs {
				if v.Fatal {
					fatal++
				}

				cmd.Printf("\n%s\n", v)
			}
			if fatal == 0 {
				return nil
			}

			return fmt.Errorf("%d of %d would stop a generation", fatal, len(vs))
		}),
	}
}

func discover(cmd *xli.Command) (Layout, error) {
	dir := "."
	if v, ok := arg.Get[string](cmd, "DIR"); ok && v != "" {
		dir = v
	}

	l, err := Discover(dir)
	if err != nil {
		return Layout{}, z.Err(err, "find the app")
	}

	return l, nil
}

// Main is `pd`, for a `main` that is one line.
func Main() {
	if err := NewCmdRoot().Run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pd:", err)
		os.Exit(1)
	}
}
