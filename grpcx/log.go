package grpcx

import (
	"context"
	"strings"

	"github.com/lesomnus/otx"
	"github.com/lesomnus/otx/otxgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/stats"
)

// Log writes a record as a call arrives and another as it is answered, the
// second at a level worked out from the status code.
//
// It is a stats handler and not an interceptor, and that is what puts it
// outside everything else there is: a call that panicked is recorded like any
// other that failed, and so are one that ran out of the time it was given and
// one that was refused before a handler was reached. An interceptor can only be
// outside the interceptors installed after it.
//
// The same handler puts the service and the method on the logger the call is
// served with, so what a handler writes of its own accord says which Rpc it was
// written under without being told.
func Log(ctx context.Context) grpc.ServerOption {
	return grpc.StatsHandler(logHandler(ctx))
}

// logHandler is [Log] as the handler itself, for [Chain].
func logHandler(ctx context.Context) stats.Handler {
	return otxgrpc.NewServerLogger(otx.From(ctx), otxgrpc.WithFilter(worth))
}

// worth is [isNoise] in the shape a logger is told it in.
func worth(info *stats.RPCTagInfo) bool {
	return !isNoise(info.FullMethodName)
}

// isNoise tells whether a method is polled often enough that recording it says
// nothing. A readiness probe every few seconds, from every replica, is most of
// what a log holds and none of what anybody reads -- and the day it matters
// that a health check failed, the thing that noticed is the prober.
func isNoise(method string) bool {
	return strings.HasPrefix(method, "/grpc.health.v1.Health/")
}
