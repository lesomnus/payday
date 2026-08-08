package cmd

import (
	"context"
	"database/sql"
	"net"

	entsql "entgo.io/ent/dialect/sql"
	"google.golang.org/grpc"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/gate"
	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/watch"

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

	// Watch is what a change is published to once the call that made it has
	// answered. The broker is named rather than defaulted: the one that
	// publishes in this process is right for one replica and **silently wrong**
	// for two, since a subscriber on one never hears about a write on another.
	Watch *watch.Watch

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

	// The server that talks to the database, twice: once as it is, and once
	// with the wall on it.
	//
	// Two things are said to it rather than to the stack, and for the same
	// reason -- both are about the statement that runs. The trail is kept by
	// the servers that do the writing, since every RPC that changes anything
	// has to report itself from inside the transaction that changes it. The
	// wall is a predicate and a predicate belongs in the WHERE.
	w := watch.New(watch.Memory())

	sink, err := pd.NewSink(client,
		bare.WithMinter(pd.Minter()),
		bare.WithRecorder(pd.Recorder()),
		bare.WithRecorder(pd.WatchRecorder(w)),
	)
	if err != nil {
		db.Close()
		return nil, err
	}

	walled, err := pd.NewSink(client,
		bare.WithMinter(pd.Minter()),
		bare.WithRecorder(pd.Recorder()),
		bare.WithRecorder(pd.WatchRecorder(w)),
		bare.WithScope(pd.Wall()),
	)
	if err != nil {
		db.Close()
		return nil, err
	}

	// The stack a caller reaches. `pd.Gate` is outermost, so nothing behind it
	// asks again.
	stacked, err := app.Build(walled.WithWatch(w), pd.AuditBuild(), pd.GateBuild())
	if err != nil {
		db.Close()
		return nil, err
	}

	// And the same servers with no wall and no gate, which is what the
	// deployment does its own work through. It is not a privilege anybody
	// holds: it is an instance somebody was handed, so going around the wall
	// is a line of wiring a reader can find rather than a rule that opens up
	// whenever nobody is asking.
	ungated, err := app.Build(sink.WithWatch(w), pd.AuditBuild())
	if err != nil {
		db.Close()
		return nil, err
	}

	return &Server{Db: db, Ent: client, Watch: w, Walled: stacked, Ungated: ungated}, nil
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
	// Who is calling comes first, since everything after it reads the frame.
	// `Plain` believes what the caller writes, which is right for a sandbox
	// and for tests and is not something to serve where anyone can reach it.
	chain := grpcx.Serving(ctx, grpcx.WithDeadline(c.Server.CallTimeout())).
		WithUnary(auth.InterceptorUnary(auth.Plain(), Resolver(s.Ungated), auth.PublicDefault)).
		WithStream(auth.InterceptorStream(auth.Plain(), Resolver(s.Ungated), auth.PublicDefault)).
		WithUnary(grpcx.LimitUnary(c.Server.Limiter(), gate.ByTenant())).
		With(gate.Interceptor(nil)).
		With(s.Watch.Interceptor()).
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
