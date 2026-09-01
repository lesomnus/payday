package grpcx_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/lesomnus/otx"
	"github.com/lesomnus/otx/otxtest"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/lesomnus/payday/grpcx"
)

// serve puts a health server behind the options the app is served with and
// returns a client of it. Health is used because it is a service that is
// already here; what is under test is what every call goes through.
func serve(t *testing.T, ctx context.Context) grpc_health_v1.HealthClient {
	t.Helper()
	return grpc_health_v1.NewHealthClient(serveConn(t, ctx))
}

// serveConn is [serve] for a test that has something of its own to register or
// to call.
func serveConn(t *testing.T, ctx context.Context, also ...func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	x := require.New(t)

	l := bufconn.Listen(1 << 20)
	g := grpc.NewServer(grpcx.ServerOptions(ctx)...)
	grpc_health_v1.RegisterHealthServer(g, health.NewServer())
	for _, f := range also {
		f(g)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := g.Serve(l); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("serve: %s", err)
		}
	}()

	conn, err := grpc.NewClient(
		"passthrough://bufconn",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return l.DialContext(ctx)
		}),
	)
	x.NoError(err)

	t.Cleanup(func() {
		conn.Close()
		g.GracefulStop()
		<-done
	})

	return conn
}

func TestOtel(t *testing.T) {
	t.Run("the trace a caller started is continued", func(t *testing.T) {
		x := require.New(t)

		h := otxtest.New(t)
		c := serve(t, h.Into(t.Context()))

		// What a caller that is already tracing sends along.
		const (
			traceId = "4bf92f3577b34da6a3ce929d0e0e4736"
			spanId  = "00f067aa0ba902b7"
		)
		ctx := metadata.AppendToOutgoingContext(t.Context(),
			"traceparent", "00-"+traceId+"-"+spanId+"-01",
		)

		_, err := c.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
		x.NoError(err)

		spans := h.Ended()
		x.Len(spans, 1)

		// The same trace, and the caller's span is the parent of ours.
		x.Equal(traceId, spans[0].SpanContext().TraceID().String())
		x.Equal(spanId, spans[0].Parent().SpanID().String())
		x.True(spans[0].Parent().IsRemote())
	})
	t.Run("a call that says nothing starts a trace of its own", func(t *testing.T) {
		x := require.New(t)

		h := otxtest.New(t)
		c := serve(t, h.Into(t.Context()))

		_, err := c.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
		x.NoError(err)

		spans := h.Ended()
		x.Len(spans, 1)
		x.True(spans[0].SpanContext().TraceID().IsValid())
		x.False(spans[0].Parent().IsValid())
	})
	t.Run("the propagator is the one of the context", func(t *testing.T) {
		x := require.New(t)

		// Not what otx falls back to, and not the empty global either.
		h := otxtest.New(t, otx.WithPropagator(nothing{}))
		c := serve(t, h.Into(t.Context()))

		ctx := metadata.AppendToOutgoingContext(t.Context(),
			"traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		)

		_, err := c.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
		x.NoError(err)

		spans := h.Ended()
		x.Len(spans, 1)
		x.False(spans[0].Parent().IsValid())
	})
}

// nothing is a propagator that reads and writes no context at all.
type nothing struct{}

func (nothing) Inject(context.Context, propagation.TextMapCarrier) {}

func (nothing) Extract(ctx context.Context, _ propagation.TextMapCarrier) context.Context {
	return ctx
}

func (nothing) Fields() []string { return nil }

var _ propagation.TextMapPropagator = nothing{}
