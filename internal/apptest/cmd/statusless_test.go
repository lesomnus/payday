package cmd_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/lesomnus/otx/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/payday/grpcx"
	"github.com/lesomnus/payday/pdtest"

	app "github.com/lesomnus/payday/internal/apptest"
)

// An error a handler returns that is no gRPC status is not the caller's to
// read: gRPC answers it `Unknown` in the error's own words, and a store's name
// its database, its user and its address. The outermost step of every server
// payday builds answers it as one of two codes in words of its own, and logs it.

// failing is the app with its robots answered by `robot`.
type failing struct {
	app.Server
	robot app.RobotServiceServer
}

func (f failing) Robot() app.RobotServiceServer { return f.robot }

// failingRobot answers a Get with what `fail` says, and a Watch with one
// message and then that.
type failingRobot struct {
	app.RobotServiceServer
	fail func(ctx context.Context) error
}

func (r failingRobot) Get(ctx context.Context, _ *app.RobotGetRequest) (*app.Robot, error) {
	return nil, r.fail(ctx)
}

func (r failingRobot) Watch(_ *app.RobotWatchRequest, ss grpc.ServerStreamingServer[app.RobotWatchResponse]) error {
	if err := ss.Send(app.RobotWatchResponse_builder{}.Build()); err != nil {
		return err
	}

	return r.fail(ss.Context())
}

// said is what a server logged, safe to read while it is still serving.
type said struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *said) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *said) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// serveFailing is the app's server, its robots failing with `fail`, and what it
// logged.
func serveFailing(t *testing.T, fail func(ctx context.Context) error) (app.RobotServiceClient, *said, context.Context) {
	t.Helper()

	b, ctx := build(t)
	b.Walled = failing{Server: b.Walled, robot: failingRobot{RobotServiceServer: b.Walled.Robot(), fail: fail}}

	w := &said{}
	l := slog.New(slog.NewTextHandler(w, nil))
	conn := pdtest.Serve(t, b.grpc(t, grpcx.Seed(func(ctx context.Context) context.Context {
		return log.Into(ctx, l)
	})))

	return app.NewRobotServiceClient(conn), w, b.travels(ctx)
}

func getRobot(ctx context.Context, c app.RobotServiceClient) error {
	_, err := c.Get(ctx, app.RobotGetRequest_builder{
		Ref: app.RobotRef_builder{Slug: app.RobotRefBySlug_builder{
			Alias:  new("arm-01"),
			Tenant: app.TenantRef_builder{Alias: new("acme")}.Build(),
		}.Build()}.Build(),
	}.Build())
	return err
}

// logged is the lines of `w` that say an error with no status was answered.
func logged(w *said) []string {
	vs := []string{}
	for _, l := range strings.Split(w.String(), "\n") {
		if strings.Contains(l, "answered an error that carries no status") {
			vs = append(vs, l)
		}
	}
	return vs
}

func TestAPlainErrorIsInternalAndOnTheLog(t *testing.T) {
	x := require.New(t)

	c, w, ctx := serveFailing(t, func(context.Context) error {
		return fmt.Errorf("ent: robot: sql/scan: column \"secret_hash\" of relation \"robots\": %w", errors.New("no"))
	})

	err := getRobot(ctx, c)
	s := status.Convert(err)
	x.Equal(codes.Internal, s.Code(), err)
	x.Equal("the server failed to answer this; it is on its log", s.Message())

	lines := logged(w)
	x.Len(lines, 1, "once")
	x.Contains(lines[0], "level=ERROR")
	x.Contains(lines[0], "/app.RobotService/Get")
	x.Contains(lines[0], `secret_hash`, "the original is on the log")
	x.Contains(lines[0], "Internal")
}

func TestAStatusPassesAsItIs(t *testing.T) {
	x := require.New(t)

	said := status.Error(codes.FailedPrecondition, "a handler's own words")
	c, w, ctx := serveFailing(t, func(context.Context) error { return fmt.Errorf("wrapped: %w", said) })

	err := getRobot(ctx, c)
	x.Equal(codes.FailedPrecondition, status.Code(err))
	x.Contains(status.Convert(err).Message(), "a handler's own words")
	x.Empty(logged(w), "a status is the handler's answer, and is not this one's to log")
}

func TestAContextsOwnErrorIsItsCode(t *testing.T) {
	for _, v := range []struct {
		err  error
		code codes.Code
	}{
		{context.Canceled, codes.Canceled},
		{context.DeadlineExceeded, codes.DeadlineExceeded},
	} {
		t.Run(v.code.String(), func(t *testing.T) {
			x := require.New(t)

			c, _, ctx := serveFailing(t, func(context.Context) error {
				return fmt.Errorf("ent: query robots: %w", v.err)
			})

			err := getRobot(ctx, c)
			x.Equal(v.code, status.Code(err))
			x.Equal(v.err.Error(), status.Convert(err).Message(), "in the context's words, not the wrapper's")
		})
	}
}

func TestAStreamThatFailsAfterItsFirstMessage(t *testing.T) {
	x := require.New(t)

	c, w, ctx := serveFailing(t, func(context.Context) error { return errors.New("the broker said something of its own") })

	s, err := c.Watch(ctx, app.RobotWatchRequest_builder{
		Filters: []*app.RobotFilter{app.RobotFilter_builder{
			Tenant: app.TenantRef_builder{Alias: new("acme")}.Build(),
		}.Build()},
	}.Build())
	x.NoError(err)

	_, err = s.Recv()
	x.NoError(err, "the first message arrives")

	_, err = s.Recv()
	x.Equal(codes.Internal, status.Code(err))
	x.NotContains(status.Convert(err).Message(), "broker")

	lines := logged(w)
	x.Len(lines, 1)
	x.Contains(lines[0], "/app.RobotService/Watch")
	x.Contains(lines[0], "the broker said something of its own")
}

// TestAStoreThatCannotBeReachedIsUnavailable is a handler's store refusing it a
// connection, in the driver's own error: on every run a PostgreSQL address
// nothing listens on, and with PDTEST_POSTGRES a real server that has no room
// for another client of the role (SQLSTATE 53300, which "too many clients
// already" is too).
func TestAStoreThatCannotBeReachedIsUnavailable(t *testing.T) {
	refused := func(t *testing.T) string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := l.Addr().String()
		require.NoError(t, l.Close())

		return "postgres://keeper:hunter2@" + addr + "/ledger?sslmode=disable"
	}

	full := func(t *testing.T) string {
		dsn := os.Getenv(pdtest.Postgres)
		if dsn == "" {
			t.Skipf("%s is not set", pdtest.Postgres)
		}

		db, err := sql.Open("pgx", dsn)
		require.NoError(t, err)
		defer db.Close()

		// A role allowed no connection at all: every one is past its limit,
		// and nothing about it is a matter of time.
		role := "p6_full_client"
		_, _ = db.Exec(fmt.Sprintf(`DROP ROLE IF EXISTS %q`, role))
		_, err = db.Exec(fmt.Sprintf(`CREATE ROLE %q LOGIN PASSWORD 'hunter2' CONNECTION LIMIT 0`, role))
		require.NoError(t, err)
		t.Cleanup(func() {
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				return
			}
			defer db.Close()
			_, _ = db.Exec(fmt.Sprintf(`DROP ROLE IF EXISTS %q`, role))
		})

		u, err := url.Parse(dsn)
		require.NoError(t, err)
		u.User = url.UserPassword(role, "hunter2")
		return u.String()
	}

	for _, v := range []struct {
		what string
		dsn  func(t *testing.T) string
		says string
	}{
		{"an address nothing listens on", refused, "connect"},
		{"a server with no room for the client", full, "53300"},
	} {
		t.Run(v.what, func(t *testing.T) {
			x := require.New(t)
			dsn := v.dsn(t)

			c, w, ctx := serveFailing(t, func(ctx context.Context) error {
				db, err := sql.Open("pgx", dsn)
				if err != nil {
					return err
				}
				defer db.Close()

				return db.PingContext(ctx)
			})

			err := getRobot(ctx, c)
			s := status.Convert(err)
			x.Equal(codes.Unavailable, s.Code(), err)
			x.Equal("the server could not reach what it stores in; try again", s.Message())

			u, _ := url.Parse(dsn)
			for _, secret := range []string{u.User.Username(), u.Host, u.Hostname(), "ledger", "database="} {
				x.NotContains(s.Message(), secret)
			}

			lines := logged(w)
			x.Len(lines, 1)
			x.Contains(lines[0], v.says, "the store's own words are on the log")
			x.Contains(lines[0], "Unavailable")
		})
	}
}
