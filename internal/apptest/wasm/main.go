//go:build js && wasm

// Command wasm is this app served from inside the page it serves.
//
// It is the same server the process runs: the same generated services, the same
// stack, the same wall generated from the same schema. Two things differ, and
// both are one line -- the database is SQLite in a Web Worker instead of a file
// or a socket, and calls arrive over a message port instead of HTTP/2.
//
// What that buys is why it exists at all. A browser reload restarts the whole
// server: new instance, new database, nothing left over. Somebody working on
// the front end does not start a backend, does not migrate anything, and does
// not have to remember what state they left it in.
//
//	GOOS=js GOARCH=wasm go build -o web/app.wasm ./wasm
//
// # Why this is a second entry point and not a flag
//
// Nothing in payday's runtime is allowed to assume a file system, a listener or
// a network, and this file is what makes that a fact rather than an intention:
// it is built for a platform where none of the three exist. `cmd` is the other
// entry point, and the two assemble the same parts differently -- which is the
// whole reason the wiring is left visible in an app rather than hidden behind a
// Serve(cfg).
package main

import (
	"context"
	"fmt"
	"log"

	drpc "github.com/lesomnus/grpc-dgram"
	"github.com/lesomnus/grpc-dgram/transport/jsport"

	pdauth "github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/gate"

	// SQLite in a worker of its own. The other driver runs the engine on
	// wazero, which is a wasm runtime written in Go, so here it would be wasm
	// inside wasm.
	_ "github.com/lesomnus/payday/config/dbsqlite3wasm"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
)

func main() {
	ctx := context.Background()

	// Held in memory rather than in OPFS, which is the decision that makes a
	// reload a fresh server. A sandbox that remembered would be a sandbox
	// somebody has to clear.
	s, err := cmd.Build(ctx, cmd.Config{
		Db: config.DbConfig{Driver: "sqlite3-wasm", Dsn: "file:sandbox?vfs=memdb"},

		// Named, because payday refuses a deployment that leaves it unsaid --
		// `memory` is right for one replica and silently wrong for two, so the
		// answer has to be written rather than defaulted. Here it is right by
		// construction: there is exactly one of this server and it is inside
		// the page.
		//
		// It was missing until this file was first loaded in a browser, and
		// nothing said so before then: the refusal happens at Build, which
		// nothing in CI reached -- `GOOS=js GOARCH=wasm go build` compiles a
		// main that is never run.
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	// The schema is created rather than migrated. In a process that would be
	// the wrong way round -- versioned migrations are what a deployment runs --
	// but there is no database here that outlives the page, so there is nothing
	// for a migration to move.
	if err := s.Ent.Schema.Create(ctx); err != nil {
		log.Fatal(err)
	}

	// The first rows, through the server the wall was never installed on.
	//
	// A tenant cannot be put up from inside one -- the Gate layer refuses it to
	// everybody, which is the same answer a real deployment gives -- so a
	// sandbox that seeded nothing would be one where the first thing anybody
	// tries is refused, correctly, and there is no way round it from the page.
	//
	// This was missing until the page was first loaded in a browser. Nothing
	// said so: the refusal is served rather than logged, and the only reader is
	// whoever is looking at the screen.
	if err := seed(ctx, s.Ungated); err != nil {
		log.Fatal(err)
	}

	// A server that is not gRPC's, taking the same services. That it can be
	// handed them at all is one widened signature upstream: RegisterServer
	// takes a grpc.ServiceRegistrar rather than a *grpc.Server.
	gw := jsport.NewGateway()

	// The same two interceptors the process serves with, because the stack
	// behind them is the same stack: `s.Walled` reads a frame and refuses a
	// request that has none, so a server registered without these answers
	// "who is asking?" to everything.
	//
	// This was missing until the page was first loaded in a browser, and it is
	// the sharpest of the three things that were: the sandbox compiled, linked,
	// started, and refused every call it was given.
	//
	// `Plain` believes what the caller writes, which is what a sandbox is --
	// there is nobody else in the page to lie to.
	srv := drpc.NewServer(gw,
		drpc.ChainUnaryInterceptors(
			pdauth.InterceptorUnary(pdauth.Plain(), cmd.Resolver(s.Ungated), pdauth.PublicDefault),
			gate.Unary(nil),
		),
		drpc.ChainStreamInterceptors(
			pdauth.InterceptorStream(pdauth.Plain(), cmd.Resolver(s.Ungated), pdauth.PublicDefault),
			gate.Stream(nil),
		),
	)
	app.RegisterServer(srv, s.Walled)

	// Publishing the entry point is the readiness signal, so nothing may be
	// published before the registration above is done -- and it blocks, because
	// a main that returns takes the instance down and the page sees its calls
	// start failing.
	log.Fatal(gw.Serve(ctx, srv))
}

// seed puts a tenant and somebody in it, so the page has an app to look at.
//
// The same two rows `custody init` writes, and for the same reason: the page
// signs in as `@acme/admin` and there has to be an `@acme/admin` to be.
func seed(ctx context.Context, s app.Server) error {
	t, err := s.Tenant().Add(ctx, app.TenantAddRequest_builder{
		Alias: "acme",
		Name:  "Acme",
	}.Build())
	if err != nil {
		return fmt.Errorf("the tenant: %w", err)
	}

	if _, err := s.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: t.GetId()}.Build(),
		Alias:  "admin",
	}.Build()); err != nil {
		return fmt.Errorf("the holder: %w", err)
	}

	return nil
}
