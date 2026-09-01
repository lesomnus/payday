package cmd_test

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/proto"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/web"

	app "github.com/lesomnus/payday/internal/apptest"
)

// The browser half, checked the way the TypeScript half is: against the server
// rather than against a description of it.
//
// A page cannot speak gRpc -- it is not a missing library, it is frames the
// platform does not let anything write -- so a Ui in front of a payday app
// reaches `web`, which answers the protocols a browser does speak and hands the
// call to the same handlers. What is worth checking is that "the same" is true:
// the wall, the credential and the errors are the ones a gRpc client gets.

// serving is the app on an Http handler, the way a deployment with an address
// in `server.http` serves it.
func (b *built) serving(t *testing.T) *httptest.Server {
	t.Helper()
	x := require.New(t)

	h, err := web.New(config.HttpConfig{
		AllowWeb: true,
		Origins:  []string{"http://localhost:5173"},
	}, b.grpc(t))
	x.NoError(err)

	// The app's own, which is the half payday cannot fill: `auth` reads a
	// credential and does not issue one, and issuing is an Http endpoint.
	h.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"token":"nope"}`)
	})

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return srv
}

// call makes one Connect call: a POST with a Json body, which is all a browser
// needs and all `curl` needs either.
func call(t *testing.T, srv *httptest.Server, method string, body string, as string) (int, string) {
	t.Helper()
	x := require.New(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.Url+method, strings.NewReader(body))
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
// protobuf and handed to the handler a gRpc client would have reached, so it
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

// TestBothWiresABrowserCanSpeak.
//
// Connect is the one worth reaching for -- a POST with a Json body, which a
// `curl` reproduces exactly -- and gRpc-Web is served as well, because it is
// what infrastructure that only understands gRpc framing wants and because
// somebody's client library will speak it.
//
// The frame is written out here rather than left to a library: one byte of
// flags, four of length, then the message. That is the whole of what makes it a
// different wire from Connect, and it is worth having in the repository once so
// that "both are served" is a thing this test says rather than a thing a
// dependency says on its behalf.
func TestBothWiresABrowserCanSpeak(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	srv := b.serving(t)

	// Connect, in Json, which is what the pages in front of a payday app use.
	code, body := call(t, srv, "/app.RobotService/List",
		`{"filters":[{"ref":{"slug":{"alias":"arm-01","tenant":{"alias":"acme"}}}}]}`, "@acme/admin")
	x.Equal(http.StatusOK, code, body)
	x.Contains(body, "arm-01")

	// And gRpc-Web, in protobuf.
	msg, err := proto.Marshal(app.RobotListRequest_builder{
		Filters: []*app.RobotFilter{
			app.RobotFilter_builder{
				Ref: app.RobotRef_builder{
					Slug: app.RobotRefBySlug_builder{
						Alias:  proto.String("arm-01"),
						Tenant: app.TenantRef_builder{Alias: proto.String("acme")}.Build(),
					}.Build(),
				}.Build(),
			}.Build(),
		},
	}.Build())
	x.NoError(err)

	frame := make([]byte, 5, 5+len(msg))
	frame[0] = 0 // data, as against the 0x80 that marks the trailers
	binary.BigEndian.PutUint32(frame[1:], uint32(len(msg)))
	frame = append(frame, msg...)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		srv.Url+"/app.RobotService/List", bytes.NewReader(frame))
	x.NoError(err)
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set(auth.Header, auth.PlainScheme+" @acme/admin")

	res, err := http.DefaultClient.Do(req)
	x.NoError(err)
	defer res.Body.Close()

	out, err := io.ReadAll(res.Body)
	x.NoError(err)

	x.Equal(http.StatusOK, res.StatusCode, string(out))
	x.Contains(res.Header.Get("Content-Type"), "grpc-web")

	// The alias is in there as protobuf rather than as Json, which is the whole
	// point of asking for this wire.
	x.Contains(string(out), "arm-01")
	x.NotContains(string(out), `"alias"`)
}

// TestAPageIsToldWhatIsWrongInItsOwnTerms.
//
// A gRpc status is a Connect error with the same code in it, so the client
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
			srv.Url+"/app.RobotService/List", nil)
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

// TestTheAppsOwnRoutesGetTheSameAnswer, which is why the cross-origin answer is
// on the mux rather than on the transcoder.
//
// With it on the transcoder alone, an app mounts `/login` and finds that **only
// that route** is refused by the browser while every Rpc works -- and nothing a
// reader can see tells the two apart. This is the assertion that arrangement
// would fail.
func TestTheAppsOwnRoutesGetTheSameAnswer(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	srv := b.serving(t)

	for _, path := range []string{"/app.RobotService/List", "/login"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodOptions, srv.Url+path, nil)
		x.NoError(err)
		req.Header.Set("Origin", "http://localhost:5173")
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)

		res, err := http.DefaultClient.Do(req)
		x.NoError(err)
		res.Body.Close()

		x.Equal("http://localhost:5173", res.Header.Get("Access-Control-Allow-Origin"), path)
	}

	// And that the route is actually served, so the check above is not agreeing
	// with a 404.
	res, err := http.Post(srv.Url+"/login", "application/json", strings.NewReader("{}"))
	x.NoError(err)
	defer res.Body.Close()

	out, err := io.ReadAll(res.Body)
	x.NoError(err)
	x.Equal(http.StatusOK, res.StatusCode)
	x.Contains(string(out), "token")
}

// TestAnAddressWithNothingConfiguredIsStillAMux.
//
// It used to be refused, and that was right only while payday owned the whole
// mux. This listener is also where an app puts what it serves over Http, and
// payday has no way to know it has routes -- a deployment serving a login
// endpoint and no browser Rpcs is a real one.
func TestAnAddressWithNothingConfiguredIsStillAMux(t *testing.T) {
	x := require.New(t)
	b, _ := build(t)

	h, err := web.New(config.HttpConfig{Addr: ":8080"}, b.grpc(t))
	x.NoError(err)
	x.NotNil(h)

	h.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

	srv := httptest.NewServer(h)
	defer srv.Close()

	res, err := http.Get(srv.Url + "/login")
	x.NoError(err)
	res.Body.Close()
	x.Equal(http.StatusTeapot, res.StatusCode)
}

// TestTheBrowserPortCarriesGrpcToo.
//
// The two listeners are a decision about the transport gRpc brings -- Go's
// Http/2 server is not grpc-go's, and grpc-go says so about its own `ServeHTTP`
// -- and **not** about what is reachable where. Under Tls this one takes Http/2
// by ALPN and the transcoder takes gRpc as an incoming protocol like any other,
// so a `grpcurl` reaches it.
//
// Worth pinning because it is the sort of thing that gets asserted from memory:
// somebody reads "a browser cannot speak cleartext Http/2" and concludes the
// port is browsers-only.
func TestTheBrowserPortCarriesGrpcToo(t *testing.T) {
	x := require.New(t)
	b, ctx := build(t)

	_, err := b.Ungated.Robot().Add(ctx, app.RobotAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: b.Tenant[:]}.Build(),
		Alias:  "arm-01",
	}.Build())
	x.NoError(err)

	h, err := web.New(config.HttpConfig{AllowWeb: true}, b.grpc(t))
	x.NoError(err)

	// Cleartext would be Http/1.1, which is the whole reason this is its own
	// listener; Tls is what makes it Http/2 without an upgrade dance.
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHttp2 = true
	srv.StartTls()
	defer srv.Close()

	cc, err := grpc.NewClient(srv.Listener.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			RootCAs: srv.Client().Transport.(*http.Transport).TlsClientConfig.RootCAs,
		})))
	x.NoError(err)
	defer cc.Close()

	res, err := app.NewRobotServiceClient(cc).List(
		auth.PlainProvider("@acme/admin").Provide(ctx),
		app.RobotListRequest_builder{
			Filters: []*app.RobotFilter{
				app.RobotFilter_builder{
					Ref: app.RobotRef_builder{
						Slug: app.RobotRefBySlug_builder{
							Alias:  proto.String("arm-01"),
							Tenant: app.TenantRef_builder{Alias: proto.String("acme")}.Build(),
						}.Build(),
					}.Build(),
				}.Build(),
			},
		}.Build())
	x.NoError(err)
	x.Len(res.GetItems(), 1)
	x.Equal("arm-01", res.GetItems()[0].GetAlias())
}
