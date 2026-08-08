package cmd_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/web"

	app "github.com/lesomnus/payday/internal/apptest"
)

// The browser half, checked the way the TypeScript half is: against the server
// rather than against a description of it.
//
// A page cannot speak gRPC -- it is not a missing library, it is frames the
// platform does not let anything write -- so a UI in front of a payday app
// reaches `web`, which answers the protocols a browser does speak and hands the
// call to the same handlers. What is worth checking is that "the same" is true:
// the wall, the credential and the errors are the ones a gRPC client gets.

// serving is the app on an HTTP handler, the way a deployment with an address
// in `server.http` serves it.
func (b *built) serving(t *testing.T) *httptest.Server {
	t.Helper()
	x := require.New(t)

	h, err := web.Handler(config.HttpConfig{
		AllowWeb: true,
		Origins:  []string{"http://localhost:5173"},
	}, b.Grpc(t.Context(), cmd0))
	x.NoError(err)
	x.NotNil(h)

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return srv
}

// call makes one Connect call: a POST with a JSON body, which is all a browser
// needs and all `curl` needs either.
func call(t *testing.T, srv *httptest.Server, method string, body string, as string) (int, string) {
	t.Helper()
	x := require.New(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+method, strings.NewReader(body))
	x.NoError(err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if as != "" {
		req.Header.Set(auth.Header, auth.PlainScheme+" "+as)
	}

	res, err := http.DefaultClient.Do(req)
	x.NoError(err)
	defer res.Body.Close()

	out, err := io.ReadAll(res.Body)
	x.NoError(err)

	return res.StatusCode, string(out)
}

// TestAPageReachesTheSameServer, which is the whole claim.
//
// It is not a second implementation of anything: the request is re-encoded as
// protobuf and handed to the handler a gRPC client would have reached, so it
// goes through the same interceptors, the same credential handling and the same
// wall.
func TestAPageReachesTheSameServer(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	// A row to find, put there the way a deployment puts the first one there.
	_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	srv := b.serving(t)

	code, body := call(t, srv, "/app.RobotService/List", `{"filters":[{"ref":{"slug":{"alias":"arm-01","tenant":{"alias":"acme"}}}}]}`, "@acme/admin")
	x.Equal(http.StatusOK, code, body)

	var res struct {
		Items []struct {
			Alias string `json:"alias"`
		} `json:"items"`
	}
	x.NoError(json.Unmarshal([]byte(body), &res))
	x.Len(res.Items, 1)
	x.Equal("arm-01", res.Items[0].Alias)
}

// TestAPageIsBehindTheSameWall.
//
// Another tenant's holder asks for a row by name and is answered with an empty
// page rather than with an error, which is what the wall does everywhere: a row
// that is not yours is a row that is not there, and saying "forbidden" would be
// saying it exists.
//
// This is the one worth having. Everything else here could be got right by a
// second implementation that forgot the wall; nothing about a transcoded call
// looks different until somebody who should not see a row asks for it.
func TestAPageIsBehindTheSameWall(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	// Somebody else entirely, put there the same way the first tenant was.
	other, err := b.Ungated.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "other"}.Build())
	x.NoError(err)
	_, err = b.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: other.GetId()}.Build(),
		Alias:  "admin",
	}.Build())
	x.NoError(err)

	srv := b.serving(t)

	const filter = `{"filters":[{"ref":{"slug":{"alias":"arm-01","tenant":{"alias":"acme"}}}}]}`

	code, body := call(t, srv, "/app.RobotService/List", filter, "@other/admin")
	x.Equal(http.StatusOK, code, body)
	x.NotContains(body, "arm-01")

	// And the same call from the tenant that holds it, so that the emptiness
	// above is the wall rather than the filter naming nothing.
	code, body = call(t, srv, "/app.RobotService/List", filter, "@acme/admin")
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, "arm-01")
}

// TestAPageIsToldWhatIsWrongInItsOwnTerms.
//
// A gRPC status is a Connect error with the same code in it, so the client
// library a page uses reads the same `NotFound` that a Go client reads. Nothing
// here translates errors: they are the handler's, carried over.
func TestAPageIsToldWhatIsWrongInItsOwnTerms(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	srv := b.serving(t)

	code, body := call(t, srv,
		"/app.RobotService/Get", `{"ref":{"slug":{"alias":"nope","tenant":{"alias":"acme"}}}}`,
		"@acme/admin")
	x.Equal(http.StatusNotFound, code, body)
	x.Contains(body, `"not_found"`)
}

// TestAnOriginNobodyNamedIsNotAnsweredFor.
//
// The preflight is answered either way -- a browser that gets a 404 there says
// the endpoint is missing, which sends whoever reads the console looking in the
// wrong place -- and what decides it is whether the allow header comes back.
func TestAnOriginNobodyNamedIsNotAnsweredFor(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	srv := b.serving(t)

	preflight := func(origin string) *http.Response {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodOptions,
			srv.URL+"/app.RobotService/List", nil)
		x.NoError(err)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)

		res, err := http.DefaultClient.Do(req)
		x.NoError(err)
		res.Body.Close()

		return res
	}

	named := preflight("http://localhost:5173")
	x.Equal("http://localhost:5173", named.Header.Get("Access-Control-Allow-Origin"))
	x.Contains(named.Header.Get("Access-Control-Allow-Headers"), auth.Header)

	other := preflight("http://evil.example.com")
	x.Empty(other.Header.Get("Access-Control-Allow-Origin"))
}

// TestAnAddressWithNothingBehindItIsRefused.
//
// `server.http.addr` with neither `allow_web` nor `allow_pprof` is a port that
// accepts a connection and answers 404, which reads as a deployment problem. It
// is a configuration to be told about instead.
func TestAnAddressWithNothingBehindItIsRefused(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	h, err := web.Handler(config.HttpConfig{Addr: ":8080"}, b.Grpc(t.Context(), cmd0))
	x.NoError(err)
	x.Nil(h)
}
