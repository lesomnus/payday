package cmd_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/bare"
	"github.com/lesomnus/payday/internal/apptest/server/core"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
)

// A recorder runs inside the write's transaction, so the database may abort
// the transaction on the recorder's statement as readily as on the entity's.
// An app that tries again a transaction the store aborted reads the driver's
// error with errors.As, and it reads it whichever statement was aborted: the
// write's answer keeps what the recorder returned beneath this server's own
// words (payday#325). What a client is told does not move.

// aborted is a driver's error for a transaction the database gave up on, as a
// type of its own, which is how an app recognises one.
type aborted struct{ state string }

func (e *aborted) Error() string {
	return "ERROR: deadlock detected (SQLSTATE " + e.state + ")"
}

// recording is a recorder that answers what `do` says, after the trail's.
type recording func(ctx context.Context) error

func (r recording) Record(ctx context.Context, _ bare.Server, _ bare.Change) error { return r(ctx) }

// seen is what an app's own interceptor made of a call's error.
type seen struct {
	mu   sync.Mutex
	errs map[string]error
}

func (s *seen) interceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		v, err := next(ctx, req)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.errs == nil {
			s.errs = map[string]error{}
		}
		s.errs[info.FullMethod] = err
		return v, err
	}
}

func (s *seen) of(method string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errs[method]
}

// serveRecording is the app's stack in the order `cmd.Build` assembles it,
// with `rec` told of every write after the trail, served through the app's
// whole interceptor chain. An app's own code is in it twice: an interceptor
// layer right beneath the gate (`inner`), and an interceptor on the gRPC
// server outside everything payday puts there, grpcx's answer included
// (`outer`).
func serveRecording(t *testing.T, rec bare.Recorder) (c app.RobotServiceClient, w *said, ctx context.Context, b *built, inner, outer *seen) {
	t.Helper()

	b, ctx = build(t)
	walled, err := pd.NewSink(b.Ent,
		bare.WithMinter(pd.Minter()),
		bare.WithRecorder(bare.Recorders{pd.Recorder(b.Contents), rec}),
		bare.WithScope(pd.Wall()),
	)
	require.NoError(t, err)

	inner, outer = &seen{}, &seen{}
	stacked, err := app.Build(walled.WithWatch(b.Watch),
		core.Build(b.Drv, b.Contents),
		pd.AuditBuild(),
		pd.SecretBuild(),
		pd.InterceptBuild([]grpc.UnaryServerInterceptor{inner.interceptor()}, nil),
		pd.GateBuild())
	require.NoError(t, err)
	b.Walled = stacked

	w = &said{}
	l := slog.New(slog.NewTextHandler(w, nil))
	conn := pdtest.Serve(t, b.grpc(t,
		grpc.ChainUnaryInterceptor(outer.interceptor()),
		grpcx.Seed(func(ctx context.Context) context.Context { return log.Into(ctx, l) }),
	))

	return app.NewRobotServiceClient(conn), w, b.travels(ctx), b, inner, outer
}

func addRobot(ctx context.Context, b *built, c app.RobotServiceClient) error {
	_, err := c.Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant.Bytes()}.Build(),
		Alias:  "arm-cause",
	}.Build())
	return err
}

// callLog is the lines of `w` that are the call log's record of a call.
func callLog(w *said) []string {
	vs := []string{}
	for _, l := range strings.Split(w.String(), "\n") {
		if strings.Contains(l, `msg="gRPC out"`) && strings.Contains(l, app.RobotService_Add_FullMethodName) {
			vs = append(vs, l)
		}
	}
	return vs
}

func TestARecordersFailureKeepsItsCause(t *testing.T) {
	// Read off `main` before the cause was kept, and held byte for byte.
	const words = "record the write by /app.RobotService/Add: ERROR: deadlock detected (SQLSTATE 40P01)"

	t.Run("a driver's error is Internal in the same words, and an app reaches it", func(t *testing.T) {
		x := require.New(t)
		c, w, ctx, b, inner, outer := serveRecording(t, recording(func(context.Context) error {
			return &aborted{state: "40P01"}
		}))

		err := addRobot(ctx, b, c)
		s, ok := status.FromError(err)
		x.True(ok)
		x.Equal(codes.Internal, s.Code(), err)
		x.Equal(words, s.Message(), "a client reads what it read before")

		for name, v := range map[string]*seen{"beneath the gate": inner, "outside the whole chain": outer} {
			err := v.of(app.RobotService_Add_FullMethodName)
			x.Equal(codes.Internal, status.Code(err), name)
			x.Equal(words, status.Convert(err).Message(), name)

			var cause *aborted
			x.True(errors.As(err, &cause), "%s: the driver's error is reachable", name)
			x.Equal("40P01", cause.state, name)
		}

		x.Eventually(func() bool { return len(callLog(w)) > 0 }, 10*time.Second, 10*time.Millisecond)
		x.Contains(callLog(w)[0], "rpc.grpc.status=Internal")
	})

	t.Run("a caller who left while it was written is Canceled, and nothing blames the store", func(t *testing.T) {
		x := require.New(t)

		writing := make(chan struct{})
		c, w, ctx, b, inner, outer := serveRecording(t, recording(func(ctx context.Context) error {
			close(writing)
			<-ctx.Done()

			// As a driver says it of a statement the context interrupted:
			// its own words, the context's error beneath them.
			return fmt.Errorf("ERROR: canceling statement due to user request: %w", ctx.Err())
		}))

		ctx, cancel := context.WithCancel(ctx)
		go func() {
			<-writing
			cancel()
		}()

		err := addRobot(ctx, b, c)
		x.Equal(codes.Canceled, status.Code(err), err)

		x.Eventually(func() bool { return len(callLog(w)) > 0 }, 10*time.Second, 10*time.Millisecond)

		for name, v := range map[string]*seen{"beneath the gate": inner, "outside the whole chain": outer} {
			err := v.of(app.RobotService_Add_FullMethodName)
			x.Equal(codes.Canceled, status.Code(err), name)
			x.ErrorIs(err, context.Canceled, name)
		}

		// What #322 made of a caller who left: one line, the call log's,
		// Canceled at WARN, and nothing at ERROR.
		lines := callLog(w)
		x.Len(lines, 1)
		x.Contains(lines[0], "rpc.grpc.status=Canceled")
		x.Contains(lines[0], "level=WARN")
		x.Empty(logged(w), "a caller who left is no fault of the server's")
		x.NotContains(w.String(), "level=ERROR")
	})
}
