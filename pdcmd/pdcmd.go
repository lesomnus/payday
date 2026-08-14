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
// # The entity commands
//
// `get`, `ls`, `add`, `patch` and `erase`, for every entity the app has, are
// the same kind of thing and are here for the same reason: they need a
// connection to a running deployment, and which connection -- and who it is
// authenticated as -- is the app's to decide. See [Tree].
//
// They are built rather than generated. Everything they need is already in the
// process: an app's `.pb.go` files register their descriptors at init, and the
// `(payday.entity)` option travels with them, so what generation would add is a
// second copy of a list the binary already has. It also gets the hard part
// right for free -- an entity has a `List` only if it declared `list:`, and a
// tree built from the descriptors has `robot ls` and no `cell ls` without
// anybody deciding that twice.
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
	"fmt"
	"os"
	"sort"

	"github.com/goccy/go-yaml"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/xli/mode"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/version"
)

// Load reads the configuration, and belongs on the **root** command so that it
// has happened before any subcommand runs.
//
//	Handler: xli.Chain(pdcmd.Load(Loader, c), xli.RequireSubcommand()),
//
// It is a handler rather than something `serve` does, because every command
// that touches a deployment needs it: `config` prints what was read, `migrate`
// opens the database it names, and a command that loaded it for itself would be
// one more place for the order to be wrong.
//
// It runs when the root is passed **through** as well as when it is the command
// -- `mode.Run|mode.Pass` -- which is the whole of why this is a function
// rather than a line in each app: an `OnRun` here fires only for `<app>` with
// no subcommand, so the file is read when nothing needs it and not read when
// everything does.
//
// `--config` names a file; nothing named is [config.Loader.Paths] in order.
// Reading none is not a failure -- a deployment may say everything in the
// environment -- so what says the file was there is [config.Loaded.Path].
//
// A variable that starts with the app's prefix and that no field answers to is
// **reported**, because that is what a typo looks like and the alternative is a
// deployment that set something and was not served by it.
func Load[T any](l config.Loader, v *T) xli.Handler {
	return xli.On(mode.Run, func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
		// Read off the command rather than from a variable the flag was told
		// to write, because when that variable is written is the flag
		// machinery's business and this has to happen after it.
		p, _ := flg.Find[string](cmd, ConfigName)

		from, err := l.Load(v, p, os.Environ())
		if err != nil {
			return err
		}
		for _, name := range from.Unknown {
			fmt.Fprintf(cmd.ErrWriter, "%s: %s is set and nothing reads it\n", l.Name(), name)
		}

		return next(ctx)
	})
}

// ConfigName is the flag [Load] reads the path from.
const ConfigName = "config"

// ConfigFlag is `--config`, for the root command [Load] is on.
func ConfigFlag() *flg.String {
	return &flg.String{
		Name:  ConfigName,
		Brief: "the configuration file to read; the app's own by default",
	}
}

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
