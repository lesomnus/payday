// Command testsrv is the app, on a port, for the TypeScript half to talk to.
//
// It exists so that the client layer is tested against **the server** rather
// than against a mock of it. A mock would agree with whatever the TypeScript
// believes, which is the one thing there is no point checking: what is worth
// knowing is whether the descriptors protobuf-es generated and the descriptors
// protoc-gen-go generated are descriptions of the same thing.
//
// It prints the address it is listening on and then serves until it is killed.
package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"

	"github.com/ncruces/go-sqlite3/vfs/memdb"

	"github.com/lesomnus/payday/config"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cmd"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "testsrv:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// In memory and named, so nothing is left behind and two of these do not
	// share a database.
	memdb.Create("ts", nil)

	c := cmd.Config{
		// The general writes, open here and closed everywhere else. They are
		// how the servers write and not how a caller asks -- an app writes the
		// RPC it means and implements it with one -- but this app has no such
		// RPC, and a test that could not change a row could not say what a
		// `Watch` does when one changes, nor what a store does when a row it is
		// drawing moves under it. The Go tests open them for the same reason.
		Server: config.ServerConfig{AllowGeneralWrites: true},

		Db: config.DbConfig{
			Driver: "sqlite3",
			Dsn:    "file:/ts.db?vfs=memdb&" + url.Values{"_pragma": {"foreign_keys(1)"}}.Encode(),
		},
		Watch: config.WatchConfig{Broker: config.BrokerMemory},
	}

	s, err := cmd.Build(ctx, c)
	if err != nil {
		return err
	}
	defer s.Close()

	if err := s.Ent.Schema.Create(ctx); err != nil {
		return err
	}

	// The first tenant and the holder every call is made as. It is what a
	// deployment does through the ungated server before it serves anything.
	tenant, err := s.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	if err != nil {
		return err
	}
	if _, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: tenant.GetId()}.Build(),
		Alias:  "admin",
	}.Build()); err != nil {
		return err
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}

	// The one line the other side is waiting for.
	fmt.Println(l.Addr().String())
	os.Stdout.Sync()

	return s.Serve(ctx, c, l)
}
