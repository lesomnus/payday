package grpcx

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// pgError is what pgx's PgError is to [unreachable]: an error with an SQLSTATE.
type pgError string

func (e pgError) Error() string    { return "FATAL: (SQLSTATE " + string(e) + ")" }
func (e pgError) SQLState() string { return string(e) }

type netSyscallError struct{ errno syscall.Errno }

func (e *netSyscallError) Error() string { return "connect: " + e.errno.Error() }
func (e *netSyscallError) Unwrap() error { return e.errno }

// TestWhatIsUnreachable is the line between Unavailable and Internal. The
// errors of a real driver are TestAStoreThatCannotBeReachedIsUnavailable's, in
// the test app; these are the shapes it reads.
func TestWhatIsUnreachable(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: &netSyscallError{syscall.ECONNREFUSED}}

	for _, v := range []struct {
		what string
		err  error
		is   bool
	}{
		{"a dial refused", refused, true},
		{"a dial refused, as pgx wraps it", fmt.Errorf("failed to connect to `user=app database=app`: %w", refused), true},
		{"too many clients already (53300)", fmt.Errorf("failed to connect: %w", pgError("53300")), true},
		{"a connection lost (08006)", pgError("08006"), true},
		{"a server shutting down (57P01)", pgError("57P01"), true},
		{"a server starting (57P03)", pgError("57P03"), true},
		{"several addresses, one full", errors.Join(errors.New("tls refused"), pgError("53300")), true},
		{"a connection database/sql threw away", fmt.Errorf("query: %w", driver.ErrBadConn), true},
		{"a reset, bare", fmt.Errorf("read: %w", syscall.ECONNRESET), true},

		{"a wrong password (28P01)", pgError("28P01"), false},
		{"a unique index (23505)", pgError("23505"), false},
		{"a file that is not there", syscall.ENOENT, false},
		{"anything else", errors.New("no"), false},
	} {
		t.Run(v.what, func(t *testing.T) {
			require.Equal(t, v.is, unreachable(v.err), "%v", v.err)
		})
	}
}

// ended is a context whose call is over: cancelled, as by a caller who hung
// up, or past a deadline the caller set.
func ended(t *testing.T, how codes.Code) context.Context {
	t.Helper()

	switch how {
	case codes.Canceled:
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		return ctx
	case codes.DeadlineExceeded:
		ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		t.Cleanup(cancel)
		return ctx
	}

	panic("no such end")
}

// written is what [answered] logged, through the logger a call is served with.
func written(ctx context.Context) (context.Context, *bytes.Buffer) {
	b := &bytes.Buffer{}
	return log.Into(ctx, slog.New(slog.NewTextHandler(b, nil))), b
}

// recorded is what [answered] wrote for a call that ended `code`: nothing for
// a caller who hung up, which is no fault of the server's, and for a deadline
// that passed the record that says why, which names a store that was slow.
func recorded(t *testing.T, code codes.Code, said string) {
	t.Helper()
	x := require.New(t)

	if code == codes.Canceled {
		x.Empty(said, "a caller who left is no fault of the server's")
		return
	}

	x.Equal(1, strings.Count(said, "\n"), said)
	x.Contains(said, "level=ERROR")
	x.Contains(said, "answered an error that carries no status")
	x.Contains(said, "rpc.grpc.status=DeadlineExceeded")
	x.Contains(said, "sqlite3: interrupted", "the store's own words are on the log")
}

// TestACallerWhoLeftIsAnsweredByTheContext is a read interrupted because the
// caller went away, in the words SQLite has for it: `interrupted`, which is no
// context's error. Whether the call is over is the context's to say, and a
// store's own words are no matter of it -- PostgreSQL's driver wraps the
// context's error and SQLite's does not, and the answer is the same on both.
func TestACallerWhoLeftIsAnsweredByTheContext(t *testing.T) {
	interrupted := errors.New("sqlite3: interrupted")
	unary := &grpc.UnaryServerInfo{FullMethod: "/app.RobotService/Get"}
	streamed := &grpc.StreamServerInfo{FullMethod: "/app.RobotService/Watch"}

	for _, code := range []codes.Code{codes.Canceled, codes.DeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			t.Run("a call", func(t *testing.T) {
				x := require.New(t)

				ctx, b := written(ended(t, code))
				_, err := answerUnary()(ctx, nil, unary, func(context.Context, any) (any, error) {
					return nil, fmt.Errorf("ent: query robots: %w", interrupted)
				})
				x.Equal(code, status.Code(err), err)
				x.Equal(ctx.Err().Error(), status.Convert(err).Message(), "in the context's words")
				recorded(t, code, b.String())
			})
			t.Run("a stream", func(t *testing.T) {
				x := require.New(t)

				ctx, b := written(ended(t, code))
				err := answerStream()(nil, serverStream{ctx: ctx}, streamed, func(any, grpc.ServerStream) error {
					return interrupted
				})
				x.Equal(code, status.Code(err), err)
				recorded(t, code, b.String())
			})
		})
	}

	// The same words with the call going on are the store failing, and the
	// record says so, as it did.
	t.Run("a live call is the store's failure", func(t *testing.T) {
		x := require.New(t)

		ctx, b := written(t.Context())
		_, err := answerUnary()(ctx, nil, unary, func(context.Context, any) (any, error) {
			return nil, interrupted
		})
		x.Equal(codes.Internal, status.Code(err), err)
		x.Contains(b.String(), "level=ERROR")
		x.Contains(b.String(), "answered an error that carries no status")
		x.Contains(b.String(), "sqlite3: interrupted")
	})

	// A status is the handler's answer whenever it is given.
	t.Run("a status passes as it is", func(t *testing.T) {
		x := require.New(t)

		said := status.Error(codes.FailedPrecondition, "a handler's own words")
		ctx, b := written(ended(t, codes.Canceled))
		_, err := answerUnary()(ctx, nil, unary, func(context.Context, any) (any, error) {
			return nil, said
		})
		x.Equal(codes.FailedPrecondition, status.Code(err))
		x.Empty(b.String())
	})
}
