// Command testsrv is the app, on a port, for the TypeScript half to talk to.
//
// It exists so that the client layer is tested against **the server** rather
// than against a mock of it. A mock would agree with whatever the TypeScript
// believes, which is the one thing there is no point checking: what is worth
// knowing is whether the descriptors protobuf-es generated and the descriptors
// protoc-gen-go generated are descriptions of the same thing.
//
// It prints the address it serves gRPC on, then the one a page signs in at, and
// then serves until it is killed.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"

	"github.com/ncruces/go-sqlite3/vfs/memdb"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/cli"
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

	if err := cli.Migrate(ctx, s); err != nil {
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

	// A call is whoever its session cookie names, and failing that whoever its
	// `authorization` header says -- so a page signed in by cookie is tried
	// against the handler that reads one, and everything else is `Plain` as it
	// was. Over plain HTTP, which is what this serves.
	sessions := authsession.New(authsession.NewMemStore(), authsession.Insecure())
	s.Auth = auth.Seq(sessions.Handler(), auth.Plain())

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	h, err := signingIn(ctx, sessions)
	if err != nil {
		return err
	}

	// The two lines the other side is waiting for.
	fmt.Println(l.Addr().String())
	fmt.Println(h)
	os.Stdout.Sync()

	return s.Serve(ctx, c, l)
}

// signingIn serves the session endpoint, and answers with where.
//
// What a form contains is the app's, and here it is the holder's identifier,
// believed -- `auth.Plain` in the shape of a sign-in, which is what a test
// needs and nothing should be served with.
func signingIn(ctx context.Context, sessions *authsession.Sessions) (string, error) {
	login := func(_ context.Context, r *http.Request) (authsession.Session, error) {
		var body struct{ Id string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return authsession.Session{}, err
		}

		return authsession.Session{Id: body.Id, Grant: frame.Whole()}, nil
	}

	mux := http.NewServeMux()
	mux.Handle("/session", sessions.Serve(login))

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}

	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "testsrv: http:", err)
		}
	}()

	return l.Addr().String(), nil
}
