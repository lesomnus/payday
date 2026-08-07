package grpcx

import (
	"context"
	"time"

	"google.golang.org/grpc"
)

// DefaultTimeout is how long a call that named no deadline of its own is
// given, unless the configuration says otherwise.
const DefaultTimeout = 30 * time.Second

// Deadline caps a call that arrived without one. A timeout that is not
// positive caps nothing.
//
// A deadline is the caller's to set and most callers set one, but a call that
// names none runs for as long as it takes -- holding a connection, a
// transaction and whatever it locked, while whoever asked for it may have
// hung up long ago. One such call is nothing. A client that retries them is
// how a server runs out of connections without any single request looking
// wrong, and the calls that pile up are exactly the ones nobody is waiting
// for.
//
// A call that named a deadline is left alone, even one further away than this.
// The caller said how long the answer is worth waiting for, and a server that
// overrules that is answering a question nobody asked. What is capped here is
// only the absence of an answer to it.
//
// Streams are not capped, and that is on purpose: a stream is long-lived by
// design, and a default deadline would be a server that hangs up on a
// subscription every thirty seconds for no reason anybody could see. A stream
// that should not run forever says so itself.
func Deadline(d time.Duration) []grpc.ServerOption {
	if d <= 0 {
		return nil
	}

	return []grpc.ServerOption{grpc.ChainUnaryInterceptor(DeadlineUnary(d))}
}

func DeadlineUnary(d time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Not a shortcut. A timeout of zero handed to context.WithTimeout is a
		// deadline that has already passed, so a cap of "none" written the
		// obvious way would be a server that fails every call it is given.
		if d <= 0 {
			return handler(ctx, req)
		}
		if _, ok := ctx.Deadline(); ok {
			return handler(ctx, req)
		}

		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()

		return handler(ctx, req)
	}
}
