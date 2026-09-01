// Package pdtest is what a test of a payday app is given: assertions that know
// about gRpc, a connection that never leaves the process, and a database of
// this test's own.
//
// It holds the half of a test harness that does not know what app it is for.
// The other half -- the schema inside that database, the server stack, who a
// test travels as -- is the app's, because those are the things an app decides.
// What is here is what every app would otherwise write again: a `require` that
// can say which status code came back, a listener that is a channel rather than
// a port, somewhere to put the rows that needs no server to be there -- see
// [DB] -- and a way to get what a server logged attached to the test that ran
// it.
//
// # Why an in-process connection rather than calling the server
//
// A test could hold a service server and call its methods, and it would be
// faster. It would also travel none of what a request travels: no interceptor,
// no codec, no status code turned into an error and back. The things a payday
// app puts in a caller's way -- who they are, what they may see, how often they
// may ask -- all live in that path, so a test that skips it is testing the half
// that was never in doubt.
package pdtest

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// X is what a test asserts with: a [require.Assertions] that knows about gRpc.
type X struct {
	tb testing.TB
	*require.Assertions
}

func NewX(tb testing.TB) *X {
	return &X{tb: tb, Assertions: require.New(tb)}
}

func (x *X) TB() testing.TB { return x.tb }

// ErrCode asserts that `err` is a gRpc error of the given code.
//
// It reports both codes by name and the error beside them, because the number
// alone is the least useful part: a test that expected NotFound and got
// InvalidArgument is usually looking at a request it built wrong, and the
// message says which field.
func (x *X) ErrCode(expected codes.Code, err error) {
	x.tb.Helper()

	actual := status.Code(err)
	if expected == actual {
		return
	}

	x.Fail(fmt.Sprintf(""+
		"Code not equal: \n"+
		"expected: %2d %s\n"+
		"actual  : %2d %s\n"+
		"error   : %v\n",
		expected, expected,
		actual, actual,
		err,
	))
}

// bufSize is the buffer of the in-memory listener; large enough that a message
// never blocks on it.
const bufSize = 1 << 20

// Serve runs `s` on a listener that is a channel and answers with a connection
// to it.
//
// Both are closed when the test is done, and the server is stopped rather than
// left to be collected -- a test that leaves one running leaves its goroutines
// running too, and the failure that causes lands in whatever test happens to be
// next.
func Serve(tb testing.TB, s *grpc.Server) *grpc.ClientConn {
	tb.Helper()
	x := require.New(tb)

	l := bufconn.Listen(bufSize)
	go func() {
		// A closed listener is how this ends, and is not a failure.
		_ = s.Serve(l)
	}()

	conn, err := grpc.NewClient(
		"passthrough://bufconn",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return l.DialContext(ctx)
		}),
	)
	x.NoError(err)

	tb.Cleanup(func() {
		conn.Close()
		s.Stop()
		l.Close()
	})

	return conn
}
