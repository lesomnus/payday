package authoidc_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authoidc"
)

const (
	issuer   = "https://auth.example.test"
	audience = "widget"
)

// idp is an identity provider: a key, and a way to mint tokens with it.
type idp struct {
	key *rsa.PrivateKey
}

func newIdp(t *testing.T) *idp {
	t.Helper()

	// 2048 because 1024 is refused by go-jose, and because a test that signs
	// with a key nobody would deploy is testing something else.
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	return &idp{key: k}
}

// mint signs the given claims.
func (p *idp) mint(t *testing.T, claims map[string]any) string {
	t.Helper()

	b, err := json.Marshal(claims)
	require.NoError(t, err)

	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key}, nil)
	require.NoError(t, err)

	o, err := s.Sign(b)
	require.NoError(t, err)

	v, err := o.CompactSerialize()
	require.NoError(t, err)

	return v
}

// handler is this provider's tokens read as identities.
//
// A static key set rather than discovery: what is under test is the reading and
// the refusing, and an HTTP round trip to a fake well-known endpoint would only
// be testing `oidc.NewProvider`.
func (p *idp) handler(claims authoidc.Claims) auth.Handler {
	ks := &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&p.key.PublicKey}}

	return authoidc.Verifier(oidc.NewVerifier(issuer, ks, &oidc.Config{ClientID: audience}), claims)
}

// with is a context carrying an authorization header.
func with(v string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(auth.Header, v))
}

func good() map[string]any {
	return map[string]any{
		"iss": issuer,
		"aud": audience,
		"sub": "0199c3f4-2a10-8002-8a03-9f2e1c4d5b02",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
}

func TestAVerifiedTokenIsAnIdentity(t *testing.T) {
	x := require.New(t)
	p := newIdp(t)

	id, err := p.handler(nil).Handle(with("Bearer " + p.mint(t, good())))
	x.NoError(err)

	x.Equal("0199c3f4-2a10-8002-8a03-9f2e1c4d5b02", id.Id)
	x.Equal(authoidc.Method, id.Method)
	x.False(id.Expires.IsZero(), "the expiry the token carried was dropped")

	// Nothing was narrowed, because the token said nothing about being
	// narrowed. A grant only ever takes away, so "said nothing" has to mean
	// "takes nothing away".
	x.True(id.Grant.IsWhole())
}

// TestATokenForSomebodyElseIsRefused is the check that is most often left out,
// and the reason `Audience` is not optional.
//
// A token minted by the same issuer for a different relying party is validly
// signed, unexpired, and about a real person. Without the audience check it is
// accepted -- which means any app that shares an issuer can be talked into
// handing over a credential that works here.
func TestATokenForSomebodyElseIsRefused(t *testing.T) {
	x := require.New(t)
	p := newIdp(t)

	claims := good()
	claims["aud"] = "somebody-else"

	_, err := p.handler(nil).Handle(with("Bearer " + p.mint(t, claims)))
	x.Error(err)
	x.NotErrorIs(err, auth.ErrNoCredential, "a token that is present and wrong is not 'nobody asked'")
}

func TestATokenThatIsNotGoodIsRefused(t *testing.T) {
	p := newIdp(t)

	t.Run("expired", func(t *testing.T) {
		x := require.New(t)

		claims := good()
		claims["exp"] = time.Now().Add(-time.Minute).Unix()

		_, err := p.handler(nil).Handle(with("Bearer " + p.mint(t, claims)))
		x.Error(err)
	})

	t.Run("from another issuer", func(t *testing.T) {
		x := require.New(t)

		claims := good()
		claims["iss"] = "https://auth.somewhere-else.test"

		_, err := p.handler(nil).Handle(with("Bearer " + p.mint(t, claims)))
		x.Error(err)
	})

	// Signed by a key this deployment has never heard of, which is the whole
	// of what a signature is for.
	t.Run("signed by somebody else", func(t *testing.T) {
		x := require.New(t)

		other := newIdp(t)

		_, err := p.handler(nil).Handle(with("Bearer " + other.mint(t, good())))
		x.Error(err)
	})

	t.Run("not a token at all", func(t *testing.T) {
		x := require.New(t)

		_, err := p.handler(nil).Handle(with("Bearer not.a.token"))
		x.Error(err)
	})
}

// TestNothingSaidIsNotARefusal, so that another handler gets asked. A
// deployment that reads both a token and a certificate depends on it.
func TestNothingSaidIsNotARefusal(t *testing.T) {
	p := newIdp(t)

	t.Run("no metadata", func(t *testing.T) {
		x := require.New(t)

		_, err := p.handler(nil).Handle(context.Background())
		x.ErrorIs(err, auth.ErrNoCredential)
	})

	t.Run("another scheme", func(t *testing.T) {
		x := require.New(t)

		_, err := p.handler(nil).Handle(with("Plain @acme/admin"))
		x.ErrorIs(err, auth.ErrNoCredential)
	})
}

// TestTheClaimsAreTheDeploymentsToName, because `sub` is the only one that is
// standard and every deployment spells the rest differently.
func TestTheClaimsAreTheDeploymentsToName(t *testing.T) {
	x := require.New(t)
	p := newIdp(t)

	claims := good()
	claims["org"] = "acme"
	claims["preferred_username"] = "admin"

	h := p.handler(func(_ context.Context, t *oidc.IDToken) (auth.Identity, error) {
		var c struct {
			Org  string `json:"org"`
			Name string `json:"preferred_username"`
		}
		if err := t.Claims(&c); err != nil {
			return auth.Identity{}, err
		}

		return auth.Identity{Tenant: c.Org, Alias: c.Name}, nil
	})

	id, err := h.Handle(with("Bearer " + p.mint(t, claims)))
	x.NoError(err)
	x.Equal("acme", id.Tenant)
	x.Equal("admin", id.Alias)

	// Filled in even though the claims function did not, because the token
	// carried one and a stream has to be cut when it runs out.
	x.False(id.Expires.IsZero())

	// And the method is this package's answer rather than the claims
	// function's, for the reason a token store does not get to say it either.
	x.Equal(authoidc.Method, id.Method)
}

// TestAWithdrawnCredentialIsRefused is the other half of a signed token: it is
// good until it expires unless somebody says otherwise, and this is where
// somebody says otherwise.
func TestAWithdrawnCredentialIsRefused(t *testing.T) {
	x := require.New(t)
	p := newIdp(t)

	var asked int
	h := authoidc.Blocked(p.handler(nil), func(_ context.Context, id auth.Identity) (bool, error) {
		asked++

		return id.Id == "0199c3f4-2a10-8002-8a03-9f2e1c4d5b02", nil
	})

	_, err := h.Handle(with("Bearer " + p.mint(t, good())))
	x.Error(err)
	x.Equal(1, asked, "the list was not consulted")

	// A token that does not verify never reaches the list, which is what keeps
	// an unauthenticated caller from making this deployment do work.
	asked = 0
	_, err = h.Handle(with("Bearer not.a.token"))
	x.Error(err)
	x.Equal(0, asked)
}

func TestAConfigurationWithNoAudienceIsRefused(t *testing.T) {
	x := require.New(t)

	// Refused before anything is fetched, so this reaches no network.
	_, err := authoidc.New(t.Context(), authoidc.Config{Issuer: issuer})
	x.ErrorIs(err, authoidc.ErrNoAudience)
}
