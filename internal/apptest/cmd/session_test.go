package cmd_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/web"

	app "github.com/lesomnus/payday/internal/apptest"
	"github.com/lesomnus/payday/internal/apptest/server/pd"
	"github.com/lesomnus/payday/pdid"
)

// A browser signing in, all the way through.
//
// The pieces are checked apart elsewhere -- `web` proves a cookie survives the
// transcoder as metadata, and `auth/authsession` proves a cookie it minted is
// one it reads. What is only true if they are joined is this: somebody posts a
// form and the next call is served **as them**, behind the wall, with a frame
// that came from the database.
//
// It is here rather than in payday because that is what this module is for: the
// resolver is generated from this app's schema, and payday cannot name it.

// browsing is the app with a sign-in endpoint on it, and a jar, the way a
// browser has one.
func (b *built) browsing(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	x := require.New(t)

	// Over plain Http, which is what `httptest` serves -- a browser refuses a
	// `__Host-` cookie there, and so does `http.Client`.
	sessions := authsession.New(authsession.NewMemStore(), authsession.Insecure())

	// Installed before the chain is built, since that is what reads it.
	b.Auth = sessions.Handler()

	h, err := web.New(config.HttpConfig{AllowWeb: true}, b.grpc(t))
	x.NoError(err)

	// The app's half: what a form contains and what checking it means. Here it
	// is "say who you are and be believed", which is `auth.Plain` in the shape
	// of a login form -- a real one asks something that keeps secrets.
	login := func(ctx context.Context, r *http.Request) (authsession.Session, error) {
		var body struct{ Id string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return authsession.Session{}, err
		}

		return authsession.Session{Id: body.Id, Grant: frame.Whole()}, nil
	}

	h.Handle("POST /session", sessions.Serve(login))
	h.Handle("DELETE /session", sessions.Serve(login))

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	x.NoError(err)

	return srv, &http.Client{Jar: jar}
}

// signedIn posts the form and answers with the status.
func signedIn(t *testing.T, c *http.Client, srv *httptest.Server, id string) int {
	t.Helper()

	res, err := c.Post(srv.Url+"/session", "application/json",
		strings.NewReader(`{"id":"`+id+`"}`))
	require.NoError(t, err)
	defer res.Body.Close()

	return res.StatusCode
}

// calls makes one Connect call with whatever the jar is holding, which is what
// a page does.
func calls(t *testing.T, c *http.Client, srv *httptest.Server, method, body string) (int, string) {
	t.Helper()
	x := require.New(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		srv.Url+method, strings.NewReader(body))
	x.NoError(err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")

	res, err := c.Do(req)
	x.NoError(err)
	defer res.Body.Close()

	out, err := io.ReadAll(res.Body)
	x.NoError(err)

	return res.StatusCode, string(out)
}

// TestAPageSignsInAndIsServedAsSomebody is the claim, joined up.
func TestAPageSignsInAndIsServedAsSomebody(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	srv, c := b.browsing(t)

	// Anonymous first, so that what changes is the sign-in and not the setup.
	code, _ := calls(t, c, srv, "/app.RobotService/List", `{}`)
	x.Equal(http.StatusUnauthorized, code, "the wall was open before anybody signed in")

	x.Equal(http.StatusNoContent, signedIn(t, c, srv, b.Holder.String()))

	// The jar has it now, and nothing in this test ever named the cookie.
	code, out := calls(t, c, srv, "/app.RobotService/List", `{}`)
	x.Equal(http.StatusOK, code, out)
	x.Contains(out, "arm-01")
}

// TestSigningOutStopsTheNextCall, which is what a server-side session buys over
// a token: the key is dead at once rather than when it expires.
func TestSigningOutStopsTheNextCall(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	srv, c := b.browsing(t)
	x.Equal(http.StatusNoContent, signedIn(t, c, srv, b.Holder.String()))

	code, _ := calls(t, c, srv, "/app.RobotService/List", `{}`)
	x.Equal(http.StatusOK, code)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, srv.Url+"/session", nil)
	x.NoError(err)
	res, err := c.Do(req)
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusNoContent, res.StatusCode)

	code, _ = calls(t, c, srv, "/app.RobotService/List", `{}`)
	x.Equal(http.StatusUnauthorized, code, "a signed-out browser was still served")
}

// TestACookieForSomebodyWhoIsNotThereIsRefused.
//
// The session is real and the person is not, which is the case a store cannot
// catch: what resolves somebody is a query against this app's schema, and it
// runs on every call rather than once at sign-in. That is also why a session
// carries no copy of anybody.
func TestACookieForSomebodyWhoIsNotThereIsRefused(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	srv, c := b.browsing(t)

	// A well-formed identifier that names nobody.
	x.Equal(http.StatusNoContent, signedIn(t, c, srv, pdid.New(pd.HolderDomain).String()))

	code, _ := calls(t, c, srv, "/app.RobotService/List", `{}`)
	x.Equal(http.StatusUnauthorized, code)
}
