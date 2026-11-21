package authoidc_test

import (
	"context"
	"crypto"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authoidc"
	"github.com/lesomnus/payday/frame"
	"github.com/stretchr/testify/require"
)

func (p *idp) access(t *testing.T, typ string, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	require.NoError(t, err)
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key}, (&jose.SignerOptions{}).WithType(jose.ContentType(typ)))
	require.NoError(t, err)
	o, err := s.Sign(b)
	require.NoError(t, err)
	raw, err := o.CompactSerialize()
	require.NoError(t, err)
	return raw
}

func accessGood() map[string]any {
	c := good()
	c["client_id"], c["jti"], c["scope"] = "approvals-ui", "token-1", "jobs.read jobs.approve"
	c["aud"] = []string{"another-resource", audience}
	c["auth_time"] = time.Now().Add(-time.Hour).Unix()
	return c
}

func (p *idp) accessVerifier(aud string, claims authoidc.Claims) auth.Handler {
	keys := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&p.key.PublicKey}}
	// Deliberately skip the library's audience check: AccessVerifier itself
	// must require this resource even for a caller-supplied verifier.
	v := oidc.NewVerifier(issuer, keys, &oidc.Config{SkipClientIDCheck: true})
	return authoidc.AccessVerifier(v, aud, claims)
}

func TestAccessClaimsAndExternalIdentity(t *testing.T) {
	x := require.New(t)
	p := newIdp(t)
	for _, typ := range []string{"at+jwt", "application/at+jwt"} {
		c := accessGood()
		var got authoidc.AccessClaims
		h := p.accessVerifier(audience, func(_ context.Context, token *oidc.IDToken) (auth.Identity, error) {
			var err error
			got, err = authoidc.ReadAccess(token, audience)
			if err != nil {
				return auth.Identity{}, err
			}
			return auth.Identity{Authority: "company-sso", Id: got.Subject, Grant: frame.Whole()}, nil
		})
		id, err := h.Handle(with("Bearer " + p.access(t, typ, c)))
		x.NoError(err)
		x.Equal("company-sso", id.Authority)
		x.Equal(c["sub"], id.Id)
		x.Equal(issuer, got.Issuer)
		x.Equal([]string{"another-resource", audience}, got.Audience)
		x.Equal("approvals-ui", got.ClientId)
		x.Equal("token-1", got.Id)
		x.Equal([]string{"jobs.read", "jobs.approve"}, got.Scope)
		x.Equal(time.Unix(c["auth_time"].(int64), 0).UTC(), got.DateAuthenticated)
		x.Equal(got.DateAuthenticated, id.DateAuthenticated)
		x.Equal(time.Unix(c["iat"].(int64), 0).UTC(), got.DateIssued)
		x.Equal(time.Unix(c["exp"].(int64), 0).UTC(), got.DateExpires)
		x.True(id.Grant.IsWhole(), "scope meanings belong to the app")
	}
	// Authentication evidence and scope may both be absent, and aud may be a
	// single string. A client-credentials subject need not be a payday ID.
	c := accessGood()
	c["sub"], c["aud"] = "robot:issuer-opaque-42", audience
	delete(c, "auth_time")
	delete(c, "scope")
	h := p.accessVerifier(audience, func(_ context.Context, token *oidc.IDToken) (auth.Identity, error) {
		got, err := authoidc.ReadAccess(token, audience)
		x.NoError(err)
		x.Empty(got.Scope)
		x.True(got.DateAuthenticated.IsZero())
		return auth.Identity{Authority: issuer, Id: got.Subject}, nil
	})
	id, err := h.Handle(with("Bearer " + p.access(t, "at+jwt", c)))
	x.NoError(err)
	x.Equal("robot:issuer-opaque-42", id.Id)
	x.True(id.DateAuthenticated.IsZero())
}

func TestAccessTokenProfileRefusesSubstitutes(t *testing.T) {
	p := newIdp(t)
	for _, tc := range []struct {
		name string
		typ  string
		edit func(map[string]any)
	}{
		{"ID token even with client_id", "JWT", nil},
		{"missing type", "", nil},
		{"wrong resource", "at+jwt", func(c map[string]any) { c["aud"] = "elsewhere" }},
		{"missing subject", "at+jwt", func(c map[string]any) { delete(c, "sub") }},
		{"empty subject", "at+jwt", func(c map[string]any) { c["sub"] = "" }},
		{"missing client", "at+jwt", func(c map[string]any) { delete(c, "client_id") }},
		{"empty client", "at+jwt", func(c map[string]any) { c["client_id"] = "" }},
		{"numeric client", "at+jwt", func(c map[string]any) { c["client_id"] = 42 }},
		{"missing token id", "at+jwt", func(c map[string]any) { delete(c, "jti") }},
		{"missing issued", "at+jwt", func(c map[string]any) { delete(c, "iat") }},
		{"null issued", "at+jwt", func(c map[string]any) { c["iat"] = nil }},
		{"string issued", "at+jwt", func(c map[string]any) { c["iat"] = "1234" }},
		{"missing expires", "at+jwt", func(c map[string]any) { delete(c, "exp") }},
		{"expired", "at+jwt", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }},
		{"array scope", "at+jwt", func(c map[string]any) { c["scope"] = []string{"read"} }},
		{"null scope", "at+jwt", func(c map[string]any) { c["scope"] = nil }},
		{"tab scope", "at+jwt", func(c map[string]any) { c["scope"] = "read\twrite" }},
		{"empty scope", "at+jwt", func(c map[string]any) { c["scope"] = "read  write" }},
		{"invalid auth time", "at+jwt", func(c map[string]any) { c["auth_time"] = "now" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)
			c := accessGood()
			if tc.edit != nil {
				tc.edit(c)
			}
			called := false
			h := p.accessVerifier(audience, func(context.Context, *oidc.IDToken) (auth.Identity, error) {
				called = true
				return auth.Identity{Id: "invented"}, nil
			})
			_, err := h.Handle(with("Bearer " + p.access(t, tc.typ, c)))
			x.Error(err)
			x.NotErrorIs(err, auth.ErrNoCredential)
			x.False(called, "invalid tokens must not reach the mapper")
		})
	}
	_, err := p.accessVerifier("", nil).Handle(with("Bearer " + p.access(t, "at+jwt", accessGood())))
	require.ErrorIs(t, err, authoidc.ErrNoAudience)
	_, err = authoidc.ReadAccess(nil, audience)
	require.Error(t, err)
	_, err = authoidc.ReadAccess(nil, "")
	require.ErrorIs(t, err, authoidc.ErrNoAudience)
}

func TestEmptySubjectNeverFallsThrough(t *testing.T) {
	p := newIdp(t)
	c := good()
	c["sub"] = ""
	for _, mapper := range []authoidc.Claims{nil, func(context.Context, *oidc.IDToken) (auth.Identity, error) {
		t.Fatal("empty subject reached the mapper")
		return auth.Identity{}, nil
	}} {
		fallback := false
		h := auth.Seq(p.handler(mapper), auth.HandlerFunc(func(context.Context) (auth.Identity, error) {
			fallback = true
			return auth.Identity{Id: "somebody-else"}, nil
		}))
		_, err := h.Handle(with("Bearer " + p.mint(t, c)))
		require.Error(t, err)
		require.NotErrorIs(t, err, auth.ErrNoCredential)
		require.False(t, fallback)
	}
	_, err := authoidc.Subject(t.Context(), &oidc.IDToken{})
	require.Error(t, err)
	require.NotErrorIs(t, err, auth.ErrNoCredential)
}

func TestNewCanRequireAccessTokens(t *testing.T) {
	x := require.New(t)
	p := newIdp(t)
	var url string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": url, "jwks_uri": url + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, Use: "sig", Algorithm: "RS256"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	url = srv.URL
	h, err := authoidc.New(t.Context(), authoidc.Config{Issuer: url, Audience: audience, AccessToken: true})
	x.NoError(err)
	c := accessGood()
	c["iss"] = url
	_, err = h.Handle(with("Bearer " + p.access(t, "at+jwt", c)))
	x.NoError(err)
	_, err = h.Handle(with("Bearer " + p.access(t, "JWT", c)))
	x.ErrorContains(err, "typ")
}
