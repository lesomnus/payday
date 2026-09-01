// Package authoidc reads the token an external identity provider issued.
//
// It is [auth.Bearer]'s counterpart for a deployment whose credentials come
// from somewhere else -- Hydra, Entra, Keycloak, a customer's own IdP. Where
// `Bearer` takes an opaque string and asks a store what it stands for, this
// takes a signed JWT and finds out by reading it.
//
// # Why it is its own package
//
// Verifying a JWT means fetching a key set over HTTP and tracking its
// rotation, and that is a dependency an app authenticating by certificate has
// no reason to carry. `config/dbpgx` is here for the same reason: a thing you
// import when you have decided to use it.
//
// # What it does not do
//
// Anything about logging in. Whoever issued the token asked for a password, ran
// an OIdC dance, checked a second factor, and decided the answer -- and none of
// that has a right answer a framework can pick. What arrives here is the
// conclusion, signed.
//
// It also does not decide who the token names. It reads claims into an
// [auth.Identity], which is text, and an app's `Resolver` looks that up against
// its own rows. That is the same boundary every handler in `auth` sits on.
//
// # What it verifies
//
//	signature   against the issuer's JWKS, following rotation
//	iss         the issuer it was configured with
//	aud         this app, which is the check most often left out
//	exp / nbf   the window it is good for
//	alg         the issuer's advertised algorithms, never "none"
//
// The verification is [oidc.IDTokenVerifier]'s. Writing it here would be
// writing an alg-confusion bug.
package authoidc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"google.golang.org/grpc/metadata"

	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/frame"
)

// Method is what this handler calls itself in [auth.Identity.Method].
const Method = "oidc"

// Claims is how a token's claims become an identity.
//
// It exists because the names are the deployment's. `sub` is standardised and
// nothing else is: which claim holds the tenant, whether there is a site claim,
// what a role is called -- all of that is what somebody configured their IdP to
// put there, and a framework that picked names would be picking somebody's IdP.
//
// What it must fill is enough for a `Resolver` to find somebody: either
// [auth.Identity.Id], or [auth.Identity.Tenant] and [auth.Identity.Alias]
// together.
//
// What it must not do is decide anything. A claim saying "admin" is a claim,
// and what an admin may do is `gate.Policy`'s to say against a row that was
// read. See [Subject] for the ordinary case.
type Claims func(ctx context.Context, t *oidc.IDToken) (auth.Identity, error)

// Subject is the [Claims] that reads `sub` as the actor's identifier, which is
// what an IdP holding the app's own user Ids looks like.
//
//	{"sub": "0199c3f4-2a10-8002-...", "exp": 1893456000}
//
// Nothing else is read. A tenant claim is deliberately not: the tenant a
// credential names is a claim to be disagreed with -- see
// [auth.Identity.TenantId] -- and the row the resolver reads is what actually
// says which tenant holds the actor. A deployment that wants the claim carried
// anyway, so that the interceptor can refuse a token that disagrees, writes its
// own [Claims] and fills `TenantId`.
func Subject(_ context.Context, t *oidc.IDToken) (auth.Identity, error) {
	if t.Subject == "" {
		return auth.Identity{}, fmt.Errorf("%w: the token names nobody", auth.ErrNoCredential)
	}

	return auth.Identity{
		Id: t.Subject,

		// Whole, because a token that says nothing about being narrowed is not
		// narrowed. An attenuated one is a [Claims] that reads whatever claim
		// the issuer was told to put it in and answers a narrower grant --
		// which is the direction that is safe to get wrong, since a grant only
		// ever takes away.
		Grant:   frame.Whole(),
		Expires: t.Expiry,
	}, nil
}

// Config is what a deployment has to say to read its provider's tokens.
type Config struct {
	// Issuer is the provider, as the `iss` claim spells it. Its metadata is
	// fetched from `<Issuer>/.well-known/openid-configuration`.
	Issuer string

	// Audience is what this app is called to the provider, and a token that
	// does not name it is refused.
	//
	// It is required, and that is the point. A verifier that skips the
	// audience accepts a token minted for **any** relying party of the same
	// issuer, which is a token somebody else's app can be talked into handing
	// over. It is the check most often left out and the one worth being unable
	// to leave out.
	Audience string

	// Claims turns a verified token into an identity, and nil is [Subject].
	Claims Claims
}

// ErrNoAudience is a configuration with no audience in it.
var ErrNoAudience = errors.New("authoidc: no audience; a token for any relying party would be accepted")

// New reads the provider's metadata and answers a handler for its tokens.
//
// It talks to the issuer, so it can fail and it takes as long as that takes.
// That is deliberate: a deployment that cannot reach its provider at startup
// cannot authenticate anybody, and finding out then is better than finding out
// on the first request. Retrying is the caller's -- it knows whether it would
// rather wait or exit.
//
//	h, err := authoidc.New(ctx, authoidc.Config{
//		Issuer:   "https://auth.example.com",
//		Audience: "widget",
//	})
//	...
//	chain.WithUnary(auth.InterceptorUnary(auth.Seq(h, auth.MTls()), Resolver(s.Ungated), auth.PublicDefault))
func New(ctx context.Context, c Config) (auth.Handler, error) {
	if c.Audience == "" {
		return nil, ErrNoAudience
	}

	p, err := oidc.NewProvider(ctx, c.Issuer)
	if err != nil {
		return nil, fmt.Errorf("authoidc: %s: %w", c.Issuer, err)
	}

	return Verifier(p.Verifier(&oidc.Config{ClientID: c.Audience}), c.Claims), nil
}

// Verifier is [New] for a verifier that was made some other way.
//
// It is here for the deployment that cannot do discovery -- an air-gapped one
// whose key set is a file, a test that mints its own tokens -- and for nothing
// else. Everything after the verifying is the same.
func Verifier(v *oidc.IDTokenVerifier, claims Claims) auth.Handler {
	if claims == nil {
		claims = Subject
	}

	return auth.HandlerFunc(func(ctx context.Context) (auth.Identity, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return auth.Identity{}, auth.ErrNoCredential
		}

		for _, s := range md.Get(auth.Header) {
			raw, ok := strings.CutPrefix(s, auth.BearerScheme+" ")
			if !ok {
				continue
			}

			t, err := v.Verify(ctx, strings.TrimSpace(raw))
			if err != nil {
				// A token that is present and does not verify stops the
				// search. Falling through would serve the caller as whatever
				// the next handler makes of them, which is a question nobody
				// asked -- see [auth.Seq].
				//
				// What it says is not passed on. Whether the signature was
				// wrong, the audience was somebody else's or it expired an hour
				// ago is for whoever reads the log, and a caller who is told
				// which one learns something about a token they do not hold.
				return auth.Identity{}, fmt.Errorf("%s: %w", Method, err)
			}

			id, err := claims(ctx, t)
			if err != nil {
				return auth.Identity{}, err
			}

			// Written here rather than trusted to the claims function, for the
			// reason [auth.TokenStore] gives: how somebody authenticated is not
			// the answer's to give.
			id.Method = Method
			if id.Expires.IsZero() && !t.Expiry.IsZero() {
				id.Expires = t.Expiry
			}

			return id, nil
		}

		return auth.Identity{}, auth.ErrNoCredential
	})
}

// Blocked wraps a handler so that a credential a deployment has taken away is
// refused before it is served.
//
// A signed token is good until it expires, which is what makes it cheap and
// what makes revoking one hard. The usual answers are short lifetimes and a
// list of what has been withdrawn; this is where the second one goes.
//
//	h = authoidc.Blocked(h, func(ctx context.Context, id auth.Identity) (bool, error) {
//		return revoked.Has(ctx, id.Id)
//	})
//
// `is` is asked on every call, so it must be a **local** answer -- a cached
// copy the provider pushes to, a table this deployment already reads. Asking
// the provider itself on every request is the anti-pattern this is meant to
// avoid: it undoes what a signed token is for and makes the provider a single
// point of failure for every call.
//
// An `is` that cannot answer should say so rather than guess. The error is
// passed on, and [auth.ErrUnavailable] is how it asks the caller to come back
// instead of telling them their credential is bad.
func Blocked(h auth.Handler, is func(ctx context.Context, id auth.Identity) (bool, error)) auth.Handler {
	return auth.HandlerFunc(func(ctx context.Context) (auth.Identity, error) {
		id, err := h.Handle(ctx)
		if err != nil {
			return auth.Identity{}, err
		}

		blocked, err := is(ctx, id)
		if err != nil {
			return auth.Identity{}, err
		}
		if blocked {
			return auth.Identity{}, fmt.Errorf("%s: withdrawn", Method)
		}

		return id, nil
	})
}
