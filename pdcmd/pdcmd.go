// Package pdcmd is the commands every payday app has, ready to be mounted on
// the app's own binary.
//
// # Why they are here and not in `pd`
//
// `pd` is the generator, and it runs against a checkout: it reads a schema and
// writes code, and it never has the app's types. These commands are the other
// kind -- they run against a **deployment**, and every one of them needs
// something only the app can hand over.
//
// `config env` is the clearest case and the one that settled the boundary.
// Listing the environment variables a deployment can set means walking the
// app's configuration struct, and payday's whole position on configuration is
// that the struct is the app's: what an app can be told is what it declared,
// and a framework that owned that would be a framework every app fights. So
// `config.Loader` walks whatever it is handed, and this mounts a command over
// it -- the app supplies the struct, payday supplies the command.
//
// The rest follow the same rule. `version` reads the build information of the
// binary it is in; `migrate` needs the database the app configured.
//
// # What is not here
//
// `serve`. It is the one command whose body is the app's stack -- which layers,
// in which order, with the wall on which server -- and that is the single most
// important thing a reader of an app can see. A `payday.Serve(cfg)` would hide
// exactly the decisions worth reading, and the template writes it out.
package pdcmd

import (
	"context"
	"os"
	"sort"

	"github.com/goccy/go-yaml"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/version"
)

// NewCmdConfig is `<app> config`: what this deployment is configured with, as
// it was read.
//
// It prints the **loaded** configuration rather than the file, which is the
// point of having it: a file, then the environment over the top of it, then
// whatever a default answers -- and what a deployment wants to know is what
// came out, not what any one of those said.
//
//	Commands: []*xli.Command{ pdcmd.NewCmdConfig(loader, &cfg) },
func NewCmdConfig[T any](l config.Loader, v *T) *xli.Command {
	return &xli.Command{
		Name:  "config",
		Brief: "print this deployment's configuration, as it was read",

		Commands: []*xli.Command{NewCmdConfigEnv(l, v)},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			return yaml.NewEncoder(cmd).Encode(v)
		}),
	}
}

// NewCmdConfigEnv is `<app> config env`: every environment variable this app
// reads.
//
// It answers the question a deployment actually has -- "what can I set" -- and
// it answers it from the struct rather than from a list somebody maintains,
// which is the whole reason it is worth being a command. A documented list of
// environment variables goes out of date on the commit that adds a field, and
// the way that is found out is somebody setting one that does nothing.
//
// Nothing about the values is printed. What a variable is *set* to is
// [NewCmdConfig]'s answer, and printing both here would put secrets in the
// output of a command whose whole use is to be pasted into a ticket.
func NewCmdConfigEnv[T any](l config.Loader, v *T) *xli.Command {
	return &xli.Command{
		Name:  "env",
		Brief: "print every environment variable this app reads",

		Flags: flg.Flags{
			&flg.Switch{
				Name:  "set",
				Brief: "say which of them are set, without saying what to",
			},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			vs := l.EnvNames(v)
			sort.Strings(vs)

			set, _ := flg.Find[bool](cmd, "set")
			for _, name := range vs {
				if !set {
					cmd.Println(name)
					continue
				}

				// Whether, and never what. A command that prints the value of
				// every variable it knows about is one whose output nobody can
				// paste anywhere.
				if _, ok := lookup(name); ok {
					cmd.Printf("%s\tset\n", name)
				} else {
					cmd.Printf("%s\t-\n", name)
				}
			}

			return nil
		}),
	}
}

// NewCmdVersion is `<app> version`.
//
// The build information comes from the binary this is compiled into, so an app
// that mounts this gets the version of *itself* rather than of payday. That is
// what makes it mountable at all.
func NewCmdVersion() *xli.Command {
	return &xli.Command{
		Name:  "version",
		Brief: "print what this binary is",

		Flags: flg.Flags{
			&flg.Switch{Name: "short", Brief: "the version and nothing else"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			v := version.Get()
			if short, _ := flg.Find[bool](cmd, "short"); short {
				cmd.Println(v.Short())
				return nil
			}

			cmd.Println(v.String())

			return nil
		}),
	}
}

// lookup is `os.LookupEnv`, behind a name, so that a test can say what the
// environment holds without setting it.
var lookup = os.LookupEnv
