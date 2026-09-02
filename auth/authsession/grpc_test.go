package authsession_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/web"
)

// signer is a sign-in served as an **Rpc**, which is what [Sessions.Mint] is
// for.
//
// It borrows `TokenService` rather than declaring one, because what is being
// checked is the mechanism and not the contract: an app defines its own, so
// that every language gets the same sign-in from generated code rather than one
// language reading a document.
type signer struct {
	pdpb.UnimplementedTokenServiceServer

	sessions *authsession.Sessions
	who      pdid.Id
}

func (s signer) Introspect(ctx context.Context, req *pdpb.TokenIntrospectRequest) (*pdpb.TokenIntrospectResponse, error) {
	if req.GetToken() == "out" {
		var was string
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			was = s.sessions.KeyOf(md.Get("cookie"))
		}

		return &pdpb.TokenIntrospectResponse{}, grpc.SetHeader(ctx,
			metadata.Pairs("set-cookie", s.sessions.End(ctx, was).String()))
	}

	v, c, err := s.sessions.Mint(ctx, authsession.Session{
		Id:    s.who.String(),
		Grant: frame.Whole(),
	})
	if err != nil {
		return nil, err
	}

	// The one line the whole arrangement rests on: response metadata named
	// `set-cookie`, which `web.Transcode` hands to the browser as a header like
	// any other.
	if err := grpc.SetHeader(ctx, metadata.Pairs("set-cookie", c.String())); err != nil {
		return nil, err
	}

	return pdpb.TokenIntrospectResponse_builder{Alias: v.Id}.Build(), nil
}

// TestASignInCanBeAnRpc is why [Sessions.Mint] is exported.
//
// It sounds as though it should not work -- a cookie is an HTTP response header
// and a gRPC handler has no response writer -- and it does: `set-cookie` as
// response metadata reaches the browser through the transcoder, and the cookie
// it sends back arrives as request metadata, which is where
// [Sessions.Handler] already reads one.
//
// So an app can define its own sign-in as a service and serve it, rather than
// leaving one endpoint in a different protocol from everything else it offers.
func TestASignInCanBeAnRpc(t *testing.T) {
	x := require.New(t)
	ctx := t.Context()

	who := pdid.New(1)
	sessions := authsession.New(authsession.NewMemStore(), authsession.Insecure())

	g := grpc.NewServer()
	pdpb.RegisterTokenServiceServer(g, signer{sessions: sessions, who: who})

	h, err := web.Transcode(g)
	x.NoError(err)

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	x.NoError(err)
	c := &http.Client{Jar: jar}

	call := func(body string) *http.Response {
		t.Helper()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			srv.URL+"/payday.TokenService/Introspect", strings.NewReader(body))
		x.NoError(err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connect-Protocol-Version", "1")

		res, err := c.Do(req)
		x.NoError(err)
		t.Cleanup(func() { _ = res.Body.Close() })

		return res
	}

	res := call(`{"token":"in"}`)
	x.Equal(http.StatusOK, res.StatusCode)

	// The browser has it, which is the half a page never sees.
	cs := jar.Cookies(mustUrl(t, srv.URL))
	x.Len(cs, 1, "the browser was not given a session")
	x.Equal("pd_session", cs[0].Name)
	x.NotEmpty(cs[0].Value)

	// And the server reads it back where it already read one: request metadata.
	t.Run("and the handler reads it back", func(t *testing.T) {
		x := require.New(t)

		id, err := sessions.Handler().Handle(metadata.NewIncomingContext(ctx,
			metadata.Pairs("cookie", "pd_session="+cs[0].Value)))
		x.NoError(err)
		x.Equal(who.String(), id.Id)
	})

	t.Run("and signing out ends it", func(t *testing.T) {
		x := require.New(t)

		res := call(`{"token":"out"}`)
		x.Equal(http.StatusOK, res.StatusCode)

		_, err := sessions.Handler().Handle(metadata.NewIncomingContext(ctx,
			metadata.Pairs("cookie", "pd_session="+cs[0].Value)))
		x.Error(err, "the session outlived the sign-out")
		x.False(errorsIsNoCredential(err), "a dead cookie fell through as a missing one")
	})
}

func errorsIsNoCredential(err error) bool {
	return err != nil && err == auth.ErrNoCredential
}

func mustUrl(t *testing.T, v string) *url.URL {
	t.Helper()

	u, err := url.Parse(v)
	require.NoError(t, err)

	return u
}
