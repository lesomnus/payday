//go:build !js

package cmd

import (
	"context"
	"log/slog"
	"net"
	"slices"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	entschema "github.com/protobuf-orm/ent/dialect/sql/schema"
	"golang.org/x/sync/errgroup"

	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/spin"

	entmigrate "github.com/lesomnus/payday/internal/apptest/internal/ent/migrate"
)

// This app's command line, in a file the sandbox does not compile.
//
// `Build` is in `serve.go` and is what both entry points call. What is here is
// everything that only a **process** does: parsing arguments, and deciding what
// to do about the shape of a database that was already there.
//
// It is tagged for the same reason `driver.go` is, and it is the second half of
// the same discovery. `entschema.Check` is ent's migration engine, which is
// Atlas -- its diff planner, its three SQL dialects, and the HCL parser they
// import. `wasm/main.go` imports this package for `Build`, so all of that was
// linked into the page: 10.6 MB, measured, to answer a question the page never
// asks. There is nothing for a migration to decide in a database that did not
// exist a moment ago and will not exist after a reload; the page executes
// `sandbox.Script` instead.
//
// So the rule this file is an instance of: what a process does with the
// database's **shape** belongs on this side of the tag. What builds the server
// belongs in `serve.go`, where both can reach it.

// Cmd is this app's own command line: what payday supplies, plus whatever the
// app has of its own.
//
// `config`, `config env` and `version` are payday's -- they are the commands
// that run against a **deployment** rather than against a checkout, and every
// one of them needs something only the app can hand over. `config env` is the
// clearest: listing the variables a deployment can set means walking this
// struct, and the struct is the app's.
//
// `serve` is not among them and will not be. It is the one command whose body
// is the stack -- which layers, in which order, with the wall on which server
// -- and that is the most important thing a reader of an app can see.
func Cmd(c *Config) *xli.Command {
	return &xli.Command{
		Name:  Name,
		Brief: "the app payday is tried against",

		Flags: flg.Flags{pdcmd.ConfigFlag()},

		Commands: []*xli.Command{
			pdcmd.NewCmdVersion(),
			pdcmd.NewCmdConfig(Loader, c),
			NewCmdServe(c),
		},

		Handler: xli.Chain(pdcmd.Load(Loader, c), xli.RequireSubcommand()),
	}
}

// NewCmdServe is `<app> serve`.
//
// It is the app's own and not payday's, for the reason at the top of
// `cmd/config.go`: the body of this command is the stack, and a framework that
// supplied it would be hiding the one thing a reader of an app most needs to
// see.
func NewCmdServe(c *Config) *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "answer requests",

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			s, err := Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// The database, before anything is served on it.
			//
			// payday owns some of this app's schema, so a field added to a
			// holder there arrives in `internal/ent` the next time this app
			// generates -- and nothing about that is loud. It compiles, the
			// tests pass against a database the tests just created, and the
			// first sign of trouble is a column that is not there in the one
			// handler that reads it.
			//
			// So one of the two happens here, and which one is the operator's
			// to say. `db.migrate: true` hands the serving process the right to
			// alter tables, which is right for development and is a thing to
			// decide on purpose; anything else and the shapes have to agree
			// already.
			if c.Db.Migrate {
				if err := s.Ent.Schema.Create(ctx); err != nil {
					return err
				}
			} else if err := entschema.Check(ctx, s.Db, s.Dialect, entmigrate.Tables); err != nil {
				return err
			}

			l, err := net.Listen("tcp", c.Server.ListenAddr())
			if err != nil {
				return err
			}

			log.From(ctx).InfoContext(ctx, "grpc", slog.String("addr", l.Addr().String()))

			// The background work and the server, together: whichever stops
			// first stops the other. A loop that keeps running under a server
			// that is going down is a process that will not exit, and a server
			// that goes on answering after its outbox drain has died is one
			// that accepts writes it will never publish.
			g, ctx := errgroup.WithContext(ctx)
			g.Go(func() error { return spin.Run(ctx, slices.Values(s.Spin)) })
			g.Go(func() error { return s.Serve(ctx, *c, l) })

			return g.Wait()
		}),
	}
}
