package cmd

import (
	"context"
	"database/sql"
	"net"

	entsql "entgo.io/ent/dialect/sql"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/grpcx"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/ent"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// Server is a built app: the database it runs on and the two stacks it answers
// through.
type Server struct {
	Db  *sql.DB
	Ent *ent.Client

	// Walled is what a caller reaches, and Ungated is what the deployment does
	// its own work through -- putting the first tenant there, working out who
	// is calling. Neither is a privilege anybody holds: the second is a server
	// instance somebody was handed, so going around the wall is a line of
	// wiring a reader can find rather than a rule that opens up whenever
	// nobody is asking.
	Walled  app.Server
	Ungated app.Server
}

// Build opens the database and stacks the servers.
//
// The two hooks are the whole of what payday puts in the write and read paths,
// and both come out of what the schema declared: [pd.Minter] stamps a new row
// with the domain of its entity and refuses one of another, [pd.Wall] narrows
// every read to the tenants the caller may see.
func Build(ctx context.Context, c Config) (*Server, error) {
	db, dialect, err := c.Db.Open(ctx)
	if err != nil {
		return nil, err
	}

	// The ent client is the app's to build, which is why config hands back a
	// *sql.DB and the dialect rather than a client: the client is generated
	// into this app from this app's schema, and payday has no name for it.
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect, db)))

	walled, err := bare.NewServer(client, bare.WithMinter(pd.Minter()), bare.WithScope(pd.Wall()))
	if err != nil {
		db.Close()
		return nil, err
	}

	ungated, err := bare.NewServer(client, bare.WithMinter(pd.Minter()))
	if err != nil {
		db.Close()
		return nil, err
	}

	return &Server{Db: db, Ent: client, Walled: walled, Ungated: ungated}, nil
}

func (s *Server) Close() error { return s.Db.Close() }

// Grpc builds the server every call arrives at.
//
// The chain is payday's and the order is this app's to read: what records a
// call is outside everything, then the recovery, then the deadline a call that
// named none is given, then how often one caller may ask, then what is closed
// to callers entirely.
//
// It is separate from [Server.Serve] so that a test can travel exactly this
// and answer on a listener that is a channel; see pdtest.
func (s *Server) Grpc(ctx context.Context, c Config, opts ...grpc.ServerOption) *grpc.Server {
	chain := grpcx.Serving(ctx, grpcx.WithDeadline(c.Server.CallTimeout())).
		WithUnary(grpcx.LimitUnary(c.Server.Limiter(), byPeer)).
		WithUnary(grpcx.ClosedUnary(c.Server.Closed()))

	os := append(opts, chain.ServerOptions()...)
	os = append(os, c.Server.GrpcOptions()...)

	g := grpc.NewServer(os...)
	app.RegisterServer(g, s.Walled)

	return g
}

// Serve answers on `l` until the context is done.
func (s *Server) Serve(ctx context.Context, c Config, l net.Listener) error {
	g := s.Grpc(ctx, c)

	go func() {
		<-ctx.Done()
		g.GracefulStop()
	}()

	return g.Serve(l)
}

// byPeer is what a call is counted against until this app has somebody to count
// against instead. Who a caller is arrives in CP3.
func byPeer(context.Context, string) string { return "" }
