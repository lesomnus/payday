package cmd

import (
	"context"
	"log/slog"
	"time"

	"github.com/lesomnus/otx/log"
	"google.golang.org/grpc"
)

// slowCall is how long a call may take between two layers before it is worth a
// line in the log.
//
// A number rather than a setting because it is a diagnostic and not a policy:
// nothing behaves differently either side of it, so a deployment that wants a
// different one is a deployment that has already read this file.
const slowCall = 250 * time.Millisecond

// Slow logs a call that took longer than `d` to cross the layer it is stacked
// at. Answering nothing but what the call answered, it is a diagnostic and
// never a refusal.
//
// # It is an ordinary gRPC interceptor
//
// The type is `grpc.UnaryServerInterceptor` -- what `grpc.NewServer` takes --
// and nothing here knows it is not on the wire. `pd.InterceptBuild` is what
// puts it between two layers instead, and the same function could be handed to
// `grpcx.Chain` without a line changing.
//
// # Why between two layers rather than on the wire
//
// Because the wire cannot say which part was slow. One `Add` is the tenant read
// the gate does before it admits the write, and then the write; on the wire
// both are one call and the answer to "what took 40ms" is `Add`. Stacked
// beneath the gate, this sees them apart and names each by the method it is --
// so the log says the read was 35ms of it, which is a different bug from the
// insert being slow.
//
// That is also the thing to understand before writing one: what it sees is
// every call that crosses this seam, which includes calls layers make of each
// other and not only calls a client made. Counting them as requests would
// count reads nobody asked for. See the server guide, "An interceptor between
// two layers".
func Slow(d time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		at := time.Now()
		v, err := next(ctx, req)

		if took := time.Since(at); took >= d {
			// At the level a slow call is: something to look at, and not
			// something that failed. The method is the one that crossed this
			// seam rather than the one the caller asked for, which is the
			// whole reason the line is worth writing.
			log.From(ctx).WarnContext(ctx, "slow",
				slog.String("rpc.method", info.FullMethod),
				slog.Duration("elapsed", took),
			)
		}

		return v, err
	}
}
