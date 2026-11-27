package grpcx

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"syscall"

	"github.com/lesomnus/otx/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// What a caller is told of an error that carries no status, in place of the
// error's own words.
//
// gRPC answers such an error `Unknown`, with its text as the message, and the
// text is whoever made it's: a store's says the database's user, its name and
// its address (`failed to connect to user=... database=...: ... too many
// clients already`), and an ORM's names a table. Neither is the caller's to
// read, and `Unknown` does not tell a page the server failing from a refusal.
// So the code says which of two it was and the words say nothing of it; the
// error itself is on the log ([answered]).
var (
	errUnreachable = status.Error(codes.Unavailable,
		"the server could not reach what it stores in; try again")
	errFailed = status.Error(codes.Internal,
		"the server failed to answer this; it is on its log")
)

// answerUnary is the outermost interceptor of every server [Serving] builds:
// an error that is not a status is answered as one. See [answered].
//
// Outermost, so that it reads what every other interceptor let through, and an
// interceptor's own error that is not a status is answered the same as a
// handler's. Not a stats handler, which is outside it: a stats handler sees
// what was answered and cannot change it, and the log's record of the call
// ([Log]) is one, so that record says what the caller was told.
func answerUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		v, err := handler(ctx, req)
		if err != nil {
			return v, answered(ctx, info.FullMethod, err)
		}

		return v, nil
	}
}

// answerStream is [answerUnary] for a stream: what it ended with, whether
// before its first message or after its hundredth.
func answerStream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := handler(srv, ss); err != nil {
			return answered(ss.Context(), info.FullMethod, err)
		}

		return nil
	}
}

// answered is what a caller is told of `err`.
//
//   - A status passes as it is, wrapped or not, as gRPC reads it: it is what a
//     handler meant to say, and nothing here rewrites that.
//   - A call whose context has ended -- its caller hung up, or the deadline
//     the caller set passed -- is the context's: `Canceled` or
//     `DeadlineExceeded`, in the context's words, whatever the error says.
//     `Canceled` is logged by nothing here; `DeadlineExceeded` is, as below.
//   - A context's own error is its code, in the same words, as gRPC says them
//     of a call it ended itself.
//   - What the server leans on not being reachable, or not taking more, is
//     `Unavailable` ([unreachable]): asked again, it may answer.
//   - Anything else is `Internal`.
//
// The last two say nothing of the error, and the error of those and of a
// deadline that passed is logged once, at
// ERROR, with the method, the code it was answered with and the peer, beside
// what the logger the call is served with already carries. The record of the
// call that follows says the code and the sentence the caller was told, so
// this is the one line that says why.
func answered(ctx context.Context, method string, err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}

	// A call whose context has ended is the context's: `Canceled` for a
	// caller who hung up, `DeadlineExceeded` for a deadline that passed.
	//
	// Asked of the context and never of the error, because what a store says
	// of a read the context interrupted is the driver's: PostgreSQL's wraps
	// the context's error, and SQLite's says `interrupted`, which is nothing
	// of the context's. Read from the error, one call would be `Canceled` on
	// one database and `Internal` at ERROR on the other, beside a record of a
	// store that failed, from a store that was fine. And not by matching
	// `interrupted` either: a live call that SQLite says it of is a store
	// that failed, as it always was, and the context is what tells the two
	// apart.
	//
	// The two are told apart for the record and not for the answer. A caller
	// who hung up is no fault of the server's, so nothing is written here and
	// the call log's record says the call and its code: one line. A deadline
	// that passed keeps the record that says why, because a read that did
	// not come back in time is a slow store, this line is the one that names
	// it, and the call log's says only the call.
	if ended := ctx.Err(); ended != nil {
		answer := status.FromContextError(ended).Err()
		if errors.Is(ended, context.DeadlineExceeded) {
			recordAnswered(ctx, method, answer, err)
		}

		return answer
	}

	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, context.Canceled.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, context.DeadlineExceeded.Error())
	}

	answer := errFailed
	if unreachable(err) {
		answer = errUnreachable
	}
	recordAnswered(ctx, method, answer, err)

	return answer
}

// recordAnswered is [answered]'s record of `err`, answered with `answer`, at
// ERROR.
func recordAnswered(ctx context.Context, method string, answer, err error) {
	// The method as [recovered] says it, beside what the logger the call is
	// served with carries already, and the rest under the names the record
	// of the call ([Log]) gives them, so that the two lines are read alike.
	attrs := []any{
		slog.String("grpc.method", method),
		slog.Int("rpc.grpc.status_code", int(status.Code(answer))),
		slog.String("rpc.grpc.status", status.Code(answer).String()),
		slog.Any("err", err),
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		attrs = append(attrs, slog.String("network.peer.address", p.Addr.String()))
	}
	log.From(ctx).ErrorContext(ctx, "answered an error that carries no status", attrs...)
}

// unreachable is whether `err` is that something the server leans on could
// not be reached, or would not take more: a connection that could not be made
// or was lost, a database with too many clients or out of room, one shutting
// down or starting up.
//
// Read without importing a driver, which would link it into every app: what
// PostgreSQL said is any error with an SQLSTATE (pgx's `PgError` has one), and
// what the network said is the standard library's. `config`'s `unreachable` is
// the same question asked at start, of a first connection, and answers less:
// there a database that answered anything at all is a configuration to fix,
// where here it is a call that may be answered if asked again.
func unreachable(err error) bool {
	for err != nil {
		switch e := err.(type) {
		case interface{ Unwrap() []error }:
			// One is enough: pgx tries each address of a name, and a
			// connection any of them refused for being full is that.
			return slices.ContainsFunc(e.Unwrap(), unreachable)

		case interface{ SQLState() string }:
			return unavailable(e.SQLState())

		case *net.OpError, *net.DNSError:
			return true

		case syscall.Errno:
			return slices.Contains(refusals, e)
		}

		switch err {
		case driver.ErrBadConn, sql.ErrConnDone, io.ErrUnexpectedEOF:
			return true
		}

		err = errors.Unwrap(err)
	}

	return false
}

// unavailable is whether an SQLSTATE is PostgreSQL not reaching or not taking
// more: class 08, a connection exception; class 53, insufficient resources --
// 53300 is "too many clients already"; and 57P01 to 57P03, an administrator's
// shutdown, a crash, a server not yet taking connections.
func unavailable(state string) bool {
	switch {
	case strings.HasPrefix(state, "08"), strings.HasPrefix(state, "53"):
		return true
	}

	return slices.Contains([]string{"57P01", "57P02", "57P03"}, state)
}

// refusals are what the operating system says of a connection that was not
// made or was lost.
var refusals = []syscall.Errno{
	syscall.ECONNREFUSED,
	syscall.ECONNRESET,
	syscall.ECONNABORTED,
	syscall.EPIPE,
	syscall.ETIMEDOUT,
	syscall.EHOSTUNREACH,
	syscall.ENETUNREACH,
	syscall.ENETDOWN,
}
