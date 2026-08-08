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
	"log"

	drpc "github.com/lesomnus/grpc-dgram"
	"github.com/lesomnus/grpc-dgram/transport/jsport"

	"github.com/lesomnus/payday/config"

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

	// A server that is not gRPC's, taking the same services. That it can be
	// handed them at all is one widened signature upstream: RegisterServer
	// takes a grpc.ServiceRegistrar rather than a *grpc.Server.
	gw := jsport.NewGateway()
	srv := drpc.NewServer(gw)
	app.RegisterServer(srv, s.Walled)

	// Publishing the entry point is the readiness signal, so nothing may be
	// published before the registration above is done -- and it blocks, because
	// a main that returns takes the instance down and the page sees its calls
	// start failing.
	log.Fatal(gw.Serve(ctx, srv))
}
