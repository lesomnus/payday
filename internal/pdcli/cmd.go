package pdcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
			NewCmdEntity(),
			NewCmdNew(),
			NewCmdSandbox(),
		},

		Handler: xli.RequireSubcommand(),
	}
}

// NewCmdGen is `pd gen`.
func NewCmdGen() *xli.Command {
	return &xli.Command{
		Name:  "gen",
		Brief: "generate everything the schema says",
		Synop: "pd gen [--check] [--ts] [DIR]",

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
				Name:     "DIR",
				Brief:    "the app; the working directory by default",
				Optional: true,
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
				Name:     "DIR",
				Brief:    "the app; the working directory by default",
				Optional: true,
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

// NewCmdNew is `pd new`: an app to start from.
//
// What it writes is what a person writes, and nothing generated -- `pd gen`
// fills the rest in. That is not laziness: a template with generated code in it
// is a template carrying the output of one version of the generators, and the
// first thing anybody does with it is regenerate.
func NewCmdNew() *xli.Command {
	return &xli.Command{
		Name:  "new",
		Brief: "write an app to start from",
		Synop: "pd new [--name NAME] [--setup] MODULE [DIR]",

		Flags: flg.Flags{
			&flg.String{
				Name:  "name",
				Brief: "what the app is called; the last segment of the module by default",
			},
			&flg.Switch{
				Name:  "setup",
				Brief: "also fetch the generators and buf's dependencies, which needs the network",
			},
		},
		Args: arg.Args{
			&arg.String{Name: "MODULE", Brief: "the Go module path, e.g. github.com/acme/thing"},
			&arg.String{Name: "DIR", Brief: "where to write it; named after the app by default", Optional: true},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			module, _ := arg.Get[string](cmd, "MODULE")
			dir, _ := arg.Get[string](cmd, "DIR")
			name, _ := flg.Find[string](cmd, "name")

			n := New{Dir: dir, Module: module, Name: name}
			if err := n.Write(); err != nil {
				return err
			}

			cmd.Printf("pd: %s is in %s\n\n", module, n.Where())

			if setup, _ := flg.Find[bool](cmd, "setup"); setup {
				if err := n.Setup(ctx, cmd); err != nil {
					return err
				}

				cmd.Printf("\n    cd %s\n", n.Where())
				cmd.Println("    go tool pd gen .")
				cmd.Println("    go mod tidy")
				cmd.Printf("    go run ./cmd/%s serve\n", n.Called())

				return nil
			}

			// The order is not guessable and getting it wrong is a confusing
			// failure: `go mod tidy` cannot run until the generated packages
			// exist, and they cannot be generated until the tools are there --
			// so a tidy first fails trying to fetch this app's own packages
			// from a repository nobody has pushed.
			for _, v := range n.Steps() {
				cmd.Printf("    %s\n", v)
			}

			return nil
		}),
	}
}

// NewCmdSandbox is `pd sandbox`.
func NewCmdSandbox() *xli.Command {
	return &xli.Command{
		Name:  "sandbox",
		Brief: "run this app inside the page it serves",
		Synop: "pd sandbox <command>",

		Commands: []*xli.Command{NewCmdSandboxInit()},

		Handler: xli.RequireSubcommand(),
	}
}

// NewCmdSandboxInit is `pd sandbox init`.
//
// It is not part of `pd new`, and that is a decision rather than an omission.
// The sandbox is only worth having to somebody building the page as well, and
// it is not free to everybody else: `wasm/main.go` makes
// `github.com/lesomnus/grpc-dgram` a direct requirement of the app, which is a
// module payday itself does not depend on. `pd new` writing `ts/` to everybody
// is not the same imposition -- an unused directory costs nothing, and an
// unused module requirement is in `go.mod`, in the sums, and in every audit of
// what this app depends on.
//
// So there is one path to a sandbox and it is this, which also means it cannot
// rot the way a second one would: an app that started without a page and grew
// one runs exactly what a new app would have run.
func NewCmdSandboxInit() *xli.Command {
	return &xli.Command{
		Name:  "init",
		Brief: "write the second entry point and the page's half of it",
		Synop: "pd sandbox init [DIR]",

		Args: arg.Args{
			&arg.String{Name: "DIR", Brief: "the app; the working directory by default", Optional: true},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			l, err := discover(cmd)
			if err != nil {
				return err
			}

			vs, err := (Sandbox{Layout: l}).Init()
			if err != nil {
				return err
			}

			cmd.Printf("pd: the sandbox is in %s and %s\n\n", l.Rel(DirWasm), l.Rel("ts", "src"))
			for _, v := range vs {
				cmd.Printf("    %s\n", v)
			}

			return nil
		}),
	}
}

// NewCmdEntity is `pd entity`.
func NewCmdEntity() *xli.Command {
	return &xli.Command{
		Name:  "entity",
		Brief: "add an entity to the schema, or say what is there",
		Synop: "pd entity <command>",

		Commands: []*xli.Command{NewCmdEntityAdd(), NewCmdEntityList()},

		Handler: xli.RequireSubcommand(),
	}
}

// NewCmdEntityAdd is `pd entity add`.
//
// It saves almost no typing, and that is not what it is for. What it does is
// the two things a person adding the fourth entity gets wrong -- picking a
// domain nothing else has, and saying which side of the wall it is on -- and
// both of those fail in ways that are cheap here and expensive later.
func NewCmdEntityAdd() *xli.Command {
	return &xli.Command{
		Name:  "add",
		Brief: "write a new entity into the schema",
		// Flags first, because that is the shape the parser takes: a flag
		// written after an argument is refused. The tenancy group is bracketed
		// for the same reason the scaffold assumes one -- saying nothing is
		// behind the wall, not an omission.
		//
		// A synopsis is what may be written, so `--tenant` is not in it: the
		// parser takes it and the scaffold refuses it, and offering it as one
		// of the tenancies would be the help promising a schema `pd gen` will
		// not accept.
		Synop: "pd entity add [--tenanted|--global] [--watch] [--file FILE] NAME [DIR]",

		Flags: flg.Flags{
			&flg.Switch{Name: "tenanted", Brief: "its rows belong to a tenant, and the wall narrows every read"},
			// Taken so that asking for it is answered. payday ships the tenant
			// and copies it into every app, so there is no schema where a
			// second one generates -- and `unknown flag` sends the same person
			// to write `tenant: {}` by hand, which is the same refusal
			// arriving later from a generator with nowhere to go next.
			&flg.Switch{Name: "tenant", Brief: "refused: payday ships the tenant, and an app does not declare a second"},
			&flg.Switch{Name: "global", Brief: "not behind the wall, and said on purpose"},
			&flg.Switch{Name: "watch", Brief: "also a List and a Watch, with the version field a Watch needs"},
			&flg.String{Name: "file", Brief: "the .proto to write into; one named after the entity by default"},
		},
		Args: arg.Args{
			&arg.String{Name: "NAME", Brief: "the message, e.g. Robot"},
			&arg.String{Name: "DIR", Brief: "the app; the working directory by default", Optional: true},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			l, err := discover(cmd)
			if err != nil {
				return err
			}

			name, _ := arg.Get[string](cmd, "NAME")
			if name == "" {
				return fmt.Errorf("say what to call it")
			}

			e := Entity{Layout: l, Name: name}
			e.Tenanted, _ = flg.Find[bool](cmd, "tenanted")
			e.Tenant, _ = flg.Find[bool](cmd, "tenant")
			e.Global, _ = flg.Find[bool](cmd, "global")
			e.Watch, _ = flg.Find[bool](cmd, "watch")
			e.File, _ = flg.Find[string](cmd, "file")

			p, err := e.Add()
			if err != nil {
				return err
			}

			// The path `Add` actually wrote, rather than one rebuilt from the
			// pieces it was given. Rebuilding it is what printed
			// `proto/app/fleet.proto` for a file that went to
			// `proto/telemetry/` -- a second answer to a question already
			// answered, and the one a person reads.
			where := p
			if v, err := filepath.Rel(l.Work, p); err == nil {
				where = filepath.ToSlash(v)
			}

			cmd.Printf("pd: %s is in %s\n", name, where)
			cmd.Println("    write its fields, then `pd gen`")

			return nil
		}),
	}
}

// NewCmdEntityList is `pd entity list`: what this schema has, by domain.
//
// It is the reading half of the same problem. A domain is one byte inside every
// identifier this app hands out, and it is not written anywhere a person
// browses -- so "what is domain 9" is a question with no easy answer and a bad
// one is an identifier that names the wrong kind of thing.
func NewCmdEntityList() *xli.Command {
	return &xli.Command{
		Name:  "list",
		Brief: "say which entity holds which domain",
		Synop: "pd entity list [DIR]",

		Args: arg.Args{
			&arg.String{Name: "DIR", Brief: "the app; the working directory by default", Optional: true},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			l, err := discover(cmd)
			if err != nil {
				return err
			}

			vs, err := Domains(l)
			if err != nil {
				return err
			}

			for _, n := range SortedDomains(vs) {
				cmd.Printf("%3d  %s\n", n, vs[n])
			}

			return nil
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
