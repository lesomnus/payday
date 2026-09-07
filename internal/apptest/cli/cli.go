// Package cli is this app's command line, and what only a process does.
//
// `cmd` is the wiring both entry points share -- `Build` is there, and the
// sandbox calls it to stand up the same server the process stands up. This
// package is everything on the other side of that: parsing arguments, the
// database engine a process opens, and deciding what to do about the shape of a
// database that was already there.
//
// # Why a package and not a build tag
//
// Because the linker follows imports, and a blank import or a migration engine
// named in `cmd` is linked into the page whether the page can use it or not.
// Measured on this app: the wazero SQLite engine was 15 MB of the sandbox
// module and Atlas, which `entschema.Check` and `Schema.Create` reach, another
// 10.5 MB -- 26 MB of a 72 MB download, to answer questions a page never asks.
//
// Both were first fixed with `//go:build !js`, which worked and was wrong: a
// tag refuses to compile what would compile, and says nothing about why. What
// is actually true is that a process and a page need different things, and a
// package boundary is how Go says that. Nothing here is excluded from a build;
// the sandbox simply does not import it.
package cli

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

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/pdcmd"
	"github.com/lesomnus/payday/spin"

	"github.com/lesomnus/payday/internal/apptest/cmd"
	entmigrate "github.com/lesomnus/payday/internal/apptest/internal/ent/migrate"
)

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
func Cmd(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  cmd.Name,
		Brief: "the app payday is tried against",

		Flags: flg.Flags{pdcmd.ConfigFlag()},

		Commands: []*xli.Command{
			pdcmd.NewCmdVersion(),
			pdcmd.NewCmdConfig(cmd.Loader, c),
			NewCmdServe(c),
		},

		Handler: xli.Chain(pdcmd.Load(cmd.Loader, c), xli.RequireSubcommand()),
	}
}

// NewCmdServe is `<app> serve`.
//
// It is the app's own and not payday's, for the reason at the top of
// `cmd/config.go`: the body of this command is the stack, and a framework that
// supplied it would be hiding the one thing a reader of an app most needs to
// see.
func NewCmdServe(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "serve",
		Brief: "answer requests",

		Handler: xli.OnRun(func(ctx context.Context, _ *xli.Command, next xli.Next) error {
			// Telemetry first, because everything after it logs. What
			// `Build` answers with is a context carrying the providers, and
			// what reads them is `grpcx` -- so a server built on a context
			// this did not touch traces and logs into providers that discard.
			ctx, done, err := Telemetry(ctx, c)
			if err != nil {
				return err
			}
			defer done()

			s, err := cmd.Build(ctx, *c)
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
				if err := Migrate(ctx, s); err != nil {
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

// Migrate brings the database s runs on into the shape this app's schema says.
//
// It is here rather than on `cmd.Server` because this is the call that links
// the migration engine, and `cmd` is what the sandbox imports. The page does
// not migrate: it executes a script that is already the answer, which is what
// `sandbox` is for.
func Migrate(ctx context.Context, s *cmd.Server) error {
	return entmigrate.NewSchema(s.Drv).Create(ctx)
}

// Telemetry builds what this app's `otel:` describes and puts it on the
// context, answering with that context and what shuts it down.
//
// It is written here rather than done by payday, and that is the same decision
// `cmd/serve.go` makes about the stack: the order is load-bearing and belongs
// where it can be read. `Build` answers with a **new context**, and everything
// that logs, traces or measures reads the providers off it -- so this has to
// happen before `cmd.Build`, and a reader has to be able to see that it does.
//
// What it costs to leave out is silence. `grpcx` puts a request logger on every
// server from `otx.From(ctx)`, and `otx.From` answers a context carrying
// nothing with the OpenTelemetry globals, which discard. The app serves, the
// calls succeed, and there is no log. `grpcx` says so once on stderr now, which
// is the only place left that still works.
func Telemetry(ctx context.Context, c *cmd.Config) (context.Context, func(), error) {
	ctx, o, err := c.Otel.Build(ctx, config.Service{
		Name: cmd.Name,

		// The Go package instruments are created from. Without it an
		// instrument is attributed to whichever library happened to make it,
		// and a dashboard groups by the name of a dependency.
		Scope: "github.com/lesomnus/payday/internal/apptest",
	})
	if err != nil {
		return nil, nil, err
	}

	if err := o.Start(ctx); err != nil {
		return nil, nil, err
	}

	// Shutdown and not ForceFlush: shutting the providers down is what flushes
	// the last batch, and a process that exits without it loses whatever that
	// batch held.
	return ctx, func() { o.Shutdown(ctx) }, nil
}
