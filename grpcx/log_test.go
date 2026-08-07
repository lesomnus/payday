package grpcx_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/otx/otxtest"
	"github.com/stretchr/testify/require"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// echo is a service of one method, so that a test has something to call that is
// nobody's idea of noise. Health is the only other thing served here and the
// log leaves it out, which is the thing under test.
//
// It is described by hand rather than generated because what it does is
// nothing: a name to call, and a reply to be answered with.
const echoMethod = "/grpcx.test.Echo/Echo"

var echo = grpc.ServiceDesc{
	ServiceName: "grpcx.test.Echo",
	// Anything implements it, and nothing is registered as the implementation
	// anyway -- gRPC only checks this against a non-nil one.
	HandlerType: (*any)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "Echo",
		Handler: func(_ any, _ context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
			v := &grpc_health_v1.HealthCheckRequest{}
			if err := dec(v); err != nil {
				return nil, err
			}

			return &grpc_health_v1.HealthCheckResponse{}, nil
		},
	}},
}

func serveEcho(g *grpc.Server) { g.RegisterService(&echo, nil) }

// wait waits until `f` holds, since a record is written from the event that
// ends the call and the client is answered a moment before that.
func wait(t *testing.T, f func() bool) {
	t.Helper()
	require.Eventually(t, f, time.Second, time.Millisecond)
}

// attr answers with the value of a record's attribute, and whether it has one.
func attr(r sdklog.Record, key string) (string, bool) {
	var (
		v     string
		found bool
	)
	r.WalkAttributes(func(kv otellog.KeyValue) bool {
		if kv.Key != key {
			return true
		}

		v, found = kv.Value.AsString(), true
		return false
	})

	return v, found
}

func TestLog(t *testing.T) {
	t.Run("a call is recorded as it arrives and as it is answered", func(t *testing.T) {
		x := require.New(t)

		h := otxtest.New(t)
		conn := serveConn(t, h.Into(t.Context()), serveEcho)

		err := conn.Invoke(t.Context(), echoMethod,
			&grpc_health_v1.HealthCheckRequest{}, &grpc_health_v1.HealthCheckResponse{})
		x.NoError(err)

		wait(t, func() bool { return len(h.Records()) == 2 })

		// Both say which RPC they were about, and they say it because the
		// handler put it on the logger every line of the call is written with
		// -- so a handler that logs on its own says it too, without being told.
		for _, r := range h.Records() {
			v, ok := attr(r, "rpc.method")
			x.True(ok)
			x.Equal("Echo", v)
		}
	})

	t.Run("a health check is not", func(t *testing.T) {
		x := require.New(t)

		h := otxtest.New(t)
		c := serve(t, h.Into(t.Context()))

		_, err := c.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
		x.NoError(err)

		// The span is ended by the handler registered in front of the log, from
		// the same event and in the same pass, so once it is here the log has
		// had its turn and declined to take it.
		wait(t, func() bool { return len(h.Ended()) == 1 })
		x.Empty(h.Records())
	})
}
