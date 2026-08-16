package pdcmd

import (
	"context"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/mode"
	"google.golang.org/grpc"
)

// Conn is what the app hands over, and the whole of what these commands need.
//
// A `grpc.ClientConnInterface` and not a generated client, for the reason this
// package exists at all: an app has more than one way in -- a dialed socket, an
// in-process server over `bufconn`, an admin port with a different policy in
// front of it -- and which one a command should use is the deployment's
// decision, not payday's. Nothing here dials, authenticates, or reads a
// configuration file. The caller has already decided all three by the time
// [Opener] is asked.
//
// It is also what makes an embedded server work unchanged: `bufconn` hands back
// a `*grpc.ClientConn` like any other.
type Conn = grpc.ClientConnInterface

// Opener answers with the connection a command should use, and how to let it
// go.
//
// # Why a function and not a connection
//
// Because of *when*. A command tree is built while an app is assembling its
// commands -- before a flag has been parsed and before a configuration file has
// been read -- and the address to dial is in that file. An app that took a
// connection here would have to open one before it knew where to.
//
// `xli` reads configuration in a handler on the root, so by the time a leaf
// runs the answer is there. This runs then. The shape is the one an app already
// uses for everything else it needs late: `pdcmd.Load` puts the configuration
// in place on the way down, and this reads it on the way to doing the work.
//
// It also settles two things a stored connection cannot:
//
//   - **`--help` opens nothing.** It is asked only when a command is actually
//     running; see [mode.Run]. A tree holding a socket would hold one for every
//     invocation, including the ones that print text and exit.
//   - **Something closes it.** The second answer is called when the command is
//     done. A connection stored in a tree belongs to whoever built the tree, and
//     there is no moment that says when they are finished with it.
//
// The close may be nil, which is what [Static] answers: a connection somebody
// else owns is not this package's to close.
type Opener func(ctx context.Context) (Conn, func(), error)

// Static is the [Opener] for a connection that is already open, and it does not
// close it.
//
// It is right for a test and for an embedded deployment -- `bufconn` is dialed
// before there is a command tree, and it lives as long as the process. It is
// wrong for anything that reads an address out of a configuration file, which
// is every deployment that connects to something: see [Opener].
func Static(c Conn) Opener {
	return func(context.Context) (Conn, func(), error) { return c, nil, nil }
}

type connKey struct{}

// ConnFrom is the connection the command running in ctx was given.
//
// It is exported for a command an app wrote itself and added to the tree with
// [Tree.Add] or [Tree.Replace]. Such a command has to reach the same server the
// generated ones do, and opening a second connection to do it would be a second
// socket, a second credential to get right, and -- for an [Opener] that hands
// out an in-process server -- a second server.
//
// Chain [Tree.WithConn] in front of the command that reads this. Without it
// there is nothing in the context and this answers false, which is the same
// mistake as forgetting to install an interceptor and reads the same way.
func ConnFrom(ctx context.Context) (Conn, bool) {
	v, ok := ctx.Value(connKey{}).(Conn)
	return v, ok
}

// MustConn is [ConnFrom] for a command that is only reachable behind
// [Tree.WithConn], so a missing connection is a wiring mistake rather than
// something to report to whoever typed the command.
func MustConn(ctx context.Context) Conn {
	v, ok := ConnFrom(ctx)
	if !ok {
		panic("pdcmd: no connection in context; chain Tree.WithConn in front of this command")
	}

	return v
}

func connInto(ctx context.Context, c Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

// withConn opens the connection, puts it in the context and closes it after.
//
// Idempotent on purpose: a command that already has one -- because a parent
// installed it, or because a test did -- reuses it rather than opening a
// second. That is what makes it free to chain in front of every command in the
// tree instead of reasoning about which ones need it.
func (r *runner) withConn() xli.Handler {
	return xli.OnF(
		// Run and not Help or Tab. Printing a usage message must not dial, and
		// neither must completing a word -- both are things somebody types
		// while working out what to type next, and neither should be able to
		// hang on a server that is not there.
		func(m mode.Mode) bool { return m.Is(mode.Run) },
		func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			if _, ok := ConnFrom(ctx); ok {
				return next(ctx)
			}

			c, done, err := r.open(ctx)
			if err != nil {
				return err
			}
			if done != nil {
				defer done()
			}

			return next(connInto(ctx, c))
		},
	)
}
