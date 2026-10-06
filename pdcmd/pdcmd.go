// Package pdcmd is the commands every payday app has, ready to be mounted on
// the app's own binary.
//
// # Why they are here and not in `pd`
//
// `pd` is the generator, and it runs against a checkout: it reads a schema and
// writes code, and it never has the app's types. These commands are the other
// kind -- they run against a **deployment**, and every one of them needs
// something only the app can hand over. `version` reads the build information
// of the binary it is in; `migrate` needs the database the app configured.
//
// `config` and `config env` were here, and settled the boundary: listing the
// variables a deployment can set means walking the app's struct, and payday's
// whole position on configuration is that the struct is the app's. They are
// xli's now, with the reading they print -- `cfg.NewCmdConfig` beside
// `cfg.Load` -- because what reads a struct from a file, the environment and
// the flags is nothing to do with RPC, and the half that binds a flag to a
// field belongs beside what parses the flag. The rule they settled is the same
// one: the app supplies the struct, and the command is mounted over it.
//
// # The entity commands
//
// `get`, `ls`, `watch`, `add`, `patch` and `erase`, for every entity the app
// has, are the same kind of thing and are here for the same reason: they need a
// connection to a running deployment, and which connection -- and who it is
// authenticated as -- is the app's to decide. See [Tree].
//
// They are built rather than generated. Everything they need is already in the
// process: an app's `.pb.go` files register their descriptors at init, and the
// `(payday.entity)` option travels with them, so what generation would add is a
// second copy of a list the binary already has. It also gets the hard part
// right for free -- an entity has a `List` only if it declared `list:`, and a
// tree built from the descriptors has `robot ls` and no `cell ls` without
// anybody deciding that twice. `watch` is the same rule read off `watch:`,
// which is why `Tenant` has a list and no watch and nobody wrote that down.
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

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/payday/version"
)

// NewCmdVersion is `<app> version`.
//
// The build information comes from the binary this is compiled into, so an app
// that mounts this gets the version of *itself* rather than of payday. That is
// what makes it mountable at all.
//
// It needs no configuration, and is the command somebody runs to ask a
// deployment whose configuration is wrong what build it is -- so an app that
// reads its configuration on the root says so: `cfg.Load(l, version)`.
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
