package auth_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/web"
)

// TestPlainInProcessIsBelievedOnlyOverBufconn is one server behind
// `auth.PlainInProcess`, reached the three ways a server is: over `bufconn`,
// which is how an app calls another it runs inside itself, over a port, and
// through the HTTP transcoder a page reaches it by. The same name is believed
// of the first and refused of the other two -- which is what makes "only this
// process reaches it" something the handler checks rather than something the
// app that wired it promised.
func TestPlainInProcessIsBelievedOnlyOverBufconn(t *testing.T) {
	var who pdid.Id
	g := grpc.NewServer(grpc.ChainUnaryInterceptor(
		auth.InterceptorUnary(auth.PlainInProcess(), known(), nil),
		func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			if f, ok := frame.From(ctx); ok {
				who = f.Actor
			}

			return h(ctx, req)
		},
	))
	healthpb.RegisterHealthServer(g, health.NewServer())
	t.Cleanup(g.Stop)

	as := metadata.NewOutgoingContext(t.Context(), metadata.Pairs(auth.Header, auth.PlainScheme+" @acme/admin"))
	check := func(t *testing.T, c *grpc.ClientConn) error {
		t.Helper()
		t.Cleanup(func() { _ = c.Close() })

		_, err := healthpb.NewHealthClient(c).Check(as, &healthpb.HealthCheckRequest{})

		return err
	}

	t.Run("over bufconn the name is believed", func(t *testing.T) {
		x := require.New(t)
		who = pdid.Nil

		l := bufconn.Listen(1 << 20)
		go func() { _ = g.Serve(l) }()

		c, err := grpc.NewClient("passthrough:///bufconn",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return l.DialContext(ctx) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		x.NoError(err)

		x.NoError(check(t, c))
		x.Equal(adminId, who)
	})

	t.Run("over a port it is refused", func(t *testing.T) {
		x := require.New(t)
		who = pdid.Nil

		l, err := net.Listen("tcp", "127.0.0.1:0")
		x.NoError(err)
		go func() { _ = g.Serve(l) }()

		c, err := grpc.NewClient("passthrough:///"+l.Addr().String(),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		x.NoError(err)

		err = check(t, c)
		x.Equal(codes.Unauthenticated, status.Code(err), "%v", err)
		x.Contains(status.Convert(err).Message(), auth.ErrFromOutside.Error())
		x.Equal(pdid.Nil, who, "served as somebody")
	})

	t.Run("through the transcoder it is refused", func(t *testing.T) {
		x := require.New(t)
		who = pdid.Nil

		h, err := web.Transcode(g)
		x.NoError(err)
		s := httptest.NewServer(h)
		t.Cleanup(s.Close)

		r, err := http.NewRequest(http.MethodPost, s.URL+"/grpc.health.v1.Health/Check", strings.NewReader(`{}`))
		x.NoError(err)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Connect-Protocol-Version", "1")
		r.Header.Set("Authorization", auth.PlainScheme+" @acme/admin")

		res, err := http.DefaultClient.Do(r)
		x.NoError(err)
		t.Cleanup(func() { _ = res.Body.Close() })
		body, err := io.ReadAll(res.Body)
		x.NoError(err)

		x.Equal(http.StatusUnauthorized, res.StatusCode, "%s", body)
		x.Contains(string(body), auth.ErrFromOutside.Error())
		x.Equal(pdid.Nil, who, "served as somebody")
	})
}
