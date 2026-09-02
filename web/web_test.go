package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/web"
)

// served is a server with one real service on it, and whatever metadata the
// last call arrived with.
func served(t *testing.T, c config.HttpConfig) (*httptest.Server, *metadata.MD) {
	t.Helper()

	seen := &metadata.MD{}

	g := grpc.NewServer(grpc.UnaryInterceptor(
		func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			if md, ok := metadata.FromIncomingContext(ctx); ok {
				*seen = md
			}

			return h(ctx, req)
		}))
	healthpb.RegisterHealthServer(g, health.NewServer())

	m, err := web.New(c, g)
	require.NoError(t, err)

	s := httptest.NewServer(m)
	t.Cleanup(s.Close)

	return s, seen
}

// call is what a page makes: a Connect POST with a Json body.
func call(t *testing.T, s *httptest.Server, f func(*http.Request)) *http.Response {
	t.Helper()

	r, err := http.NewRequest(http.MethodPost,
		s.URL+"/grpc.health.v1.Health/Check", strings.NewReader(`{}`))
	require.NoError(t, err)

	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Connect-Protocol-Version", "1")
	if f != nil {
		f(r)
	}

	res, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	t.Cleanup(func() { res.Body.Close() })

	return res
}

// TestACookieReachesTheInterceptor is what `auth/authsession` rests on.
//
// A browser cannot speak gRPC, so its call comes through the transcoder, and a
// session cookie is only useful if it survives that hop -- as `cookie`, in the
// ordinary metadata an [auth.Handler] reads. Nothing in payday would fail to
// compile if it did not; every request would simply be anonymous.
func TestACookieReachesTheInterceptor(t *testing.T) {
	x := require.New(t)

	s, seen := served(t, config.HttpConfig{AllowWeb: true})

	res := call(t, s, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "__Host-pd_session", Value: "abc123"})
	})
	x.Equal(http.StatusOK, res.StatusCode)

	x.Equal([]string{"__Host-pd_session=abc123"}, seen.Get("cookie"))
}

// TestTheAuthorizationHeaderReachesItToo, which is the path every other handler
// in `auth` reads.
func TestTheAuthorizationHeaderReachesItToo(t *testing.T) {
	x := require.New(t)

	s, seen := served(t, config.HttpConfig{AllowWeb: true})

	call(t, s, func(r *http.Request) { r.Header.Set("Authorization", "Plain @acme/someone") })

	x.Equal([]string{"Plain @acme/someone"}, seen.Get("authorization"))
}

// TestANamedOriginMayUseCookies.
//
// Without `Access-Control-Allow-Credentials` a page on another origin -- every
// `npm run dev` -- neither receives the cookie a sign-in sets nor sends it
// back, and the symptom is a login that works in `curl` and does nothing in a
// browser.
func TestANamedOriginMayUseCookies(t *testing.T) {
	x := require.New(t)

	s, _ := served(t, config.HttpConfig{
		AllowWeb: true,
		Origins:  []string{"http://localhost:5173"},
	})

	res := call(t, s, func(r *http.Request) {
		r.Header.Set("Origin", "http://localhost:5173")
	})

	x.Equal("http://localhost:5173", res.Header.Get("Access-Control-Allow-Origin"))
	x.Equal("true", res.Header.Get("Access-Control-Allow-Credentials"))

	// Never `*`, which a browser refuses to combine with credentials -- and
	// which is why echoing the origin is what makes this safe to send at all.
	x.NotEqual("*", res.Header.Get("Access-Control-Allow-Origin"))
}

// TestThePreflightSaysItTooBecauseThatIsWhatTheBrowserAsks.
//
// A credentialed cross-origin request is refused at the preflight, before the
// POST is ever made, so the header has to be on both answers.
func TestThePreflightSaysItTooBecauseThatIsWhatTheBrowserAsks(t *testing.T) {
	x := require.New(t)

	s, _ := served(t, config.HttpConfig{
		AllowWeb: true,
		Origins:  []string{"http://localhost:5173"},
	})

	r, err := http.NewRequest(http.MethodOptions, s.URL+"/grpc.health.v1.Health/Check", nil)
	x.NoError(err)
	r.Header.Set("Origin", "http://localhost:5173")
	r.Header.Set("Access-Control-Request-Method", "POST")

	res, err := http.DefaultClient.Do(r)
	x.NoError(err)
	defer res.Body.Close()

	x.Equal(http.StatusNoContent, res.StatusCode)
	x.Equal("true", res.Header.Get("Access-Control-Allow-Credentials"))
}

// TestAnOriginNobodyNamedGetsNothing, which is the trust decision this rests
// on: credentials are allowed for origins somebody wrote down, and there is no
// other kind.
func TestAnOriginNobodyNamedGetsNothing(t *testing.T) {
	x := require.New(t)

	s, _ := served(t, config.HttpConfig{
		AllowWeb: true,
		Origins:  []string{"http://localhost:5173"},
	})

	res := call(t, s, func(r *http.Request) {
		r.Header.Set("Origin", "https://elsewhere.example")
	})

	x.Empty(res.Header.Get("Access-Control-Allow-Origin"))
	x.Empty(res.Header.Get("Access-Control-Allow-Credentials"))
}

// TestAnAppMountsItsOwnRoutes, which is where a sign-in endpoint goes.
//
// A gRPC path is `/<service>/<method>`, so an ordinary route cannot collide
// with one -- and this asserts the transcoder mounted at `/` does not swallow
// it.
func TestAnAppMountsItsOwnRoutes(t *testing.T) {
	x := require.New(t)

	g := grpc.NewServer()
	healthpb.RegisterHealthServer(g, health.NewServer())

	m, err := web.New(config.HttpConfig{AllowWeb: true}, g)
	x.NoError(err)

	m.Handle("POST /session", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	s := httptest.NewServer(m)
	defer s.Close()

	res, err := http.Post(s.URL+"/session", "application/json", nil)
	x.NoError(err)
	defer res.Body.Close()

	x.Equal(http.StatusNoContent, res.StatusCode)
}
