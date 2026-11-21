package authoidc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authoidc"
	"github.com/stretchr/testify/require"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type remoteIssuer struct {
	mu        sync.Mutex
	keys      []jose.JSONWebKey
	failure   bool
	discovery int
	fetches   int
}

func (p *remoteIssuer) roundTrip(r *http.Request) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var body any
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		p.discovery++
		body = map[string]any{"issuer": issuer, "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}}
	case "/keys":
		p.fetches++
		body = jose.JSONWebKeySet{Keys: p.keys}
	default:
		return nil, fmt.Errorf("unexpected URL: %s", r.URL)
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	code := http.StatusOK
	if p.failure {
		code, b = http.StatusServiceUnavailable, []byte("issuer unavailable")
	}
	return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b))), Request: r}, nil
}

func (p *remoteIssuer) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.discovery, p.fetches
}

func (p *idp) jwk(kid string) jose.JSONWebKey {
	return jose.JSONWebKey{Key: &p.key.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"}
}

func (p *idp) keyed(t *testing.T, kid string) string {
	t.Helper()
	b, err := json.Marshal(good())
	require.NoError(t, err)
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key}, (&jose.SignerOptions{}).WithHeader("kid", kid))
	require.NoError(t, err)
	o, err := s.Sign(b)
	require.NoError(t, err)
	raw, err := o.CompactSerialize()
	require.NoError(t, err)
	return raw
}

func TestRemoteMissesAreLimitedAndRotationRecovers(t *testing.T) {
	one, two := newIdp(t), newIdp(t)
	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		remote := &remoteIssuer{keys: []jose.JSONWebKey{one.jwk("one")}}
		client := &http.Client{Transport: roundTrip(remote.roundTrip)}
		h, err := authoidc.New(oidc.ClientContext(t.Context(), client), authoidc.Config{Issuer: issuer, Audience: audience})
		x.NoError(err)
		d, k := remote.counts()
		x.Equal(1, d)
		x.Zero(k, "JWKS is already lazy even when discovery is eager")
		x.IsType(roundTrip(nil), client.Transport, "the caller's client must not be mutated")
		valid := one.keyed(t, "one")
		_, err = h.Handle(with("Bearer " + valid))
		x.NoError(err)

		// Distinct unknown kids and invalid signatures under the known kid
		// must both be bounded, including simultaneous calls.
		var tokens []string
		for i := range 24 {
			tokens = append(tokens, two.keyed(t, fmt.Sprintf("unknown-%d", i)))
		}
		tokens = append(tokens, two.keyed(t, "one"))
		burst := func() {
			var wg sync.WaitGroup
			for _, token := range tokens {
				wg.Go(func() {
					_, err := h.Handle(with("Bearer " + token))
					if err == nil {
						t.Error("unknown key or bad signature admitted")
					}
				})
			}
			wg.Wait()
		}
		burst()
		_, k = remote.counts()
		x.Equal(1, k)
		time.Sleep(time.Minute)
		burst()
		_, k = remote.counts()
		x.Equal(2, k, "one remote attempt per interval even with concurrent misses")
		_, err = h.Handle(with("Bearer " + valid))
		x.NoError(err)

		remote.mu.Lock()
		remote.keys = []jose.JSONWebKey{two.jwk("two")}
		remote.mu.Unlock()
		rotated := two.keyed(t, "two")
		_, err = h.Handle(with("Bearer " + rotated))
		x.Error(err, "new keys wait until the refresh interval")
		time.Sleep(time.Minute)
		_, err = h.Handle(with("Bearer " + rotated))
		x.NoError(err)
		_, k = remote.counts()
		x.Equal(3, k)

		remote.mu.Lock()
		remote.failure = true
		remote.mu.Unlock()
		time.Sleep(time.Minute)
		burst()
		burst()
		_, k = remote.counts()
		x.Equal(4, k, "a failed fetch is throttled too")
		_, err = h.Handle(with("Bearer " + rotated))
		x.NoError(err, "a known valid key remains usable during an issuer outage")
	})
}

func TestLazyDiscoveryStartsOfflineAndRecovers(t *testing.T) {
	p := newIdp(t)
	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		remote := &remoteIssuer{keys: []jose.JSONWebKey{p.jwk("one")}, failure: true}
		ctx := oidc.ClientContext(t.Context(), &http.Client{Transport: roundTrip(remote.roundTrip)})
		c := authoidc.Config{Issuer: issuer, Audience: audience, Lazy: true, RefreshInterval: 2 * time.Minute}
		h, err := authoidc.New(ctx, c)
		x.NoError(err)
		_, err = h.Handle(t.Context())
		x.ErrorIs(err, auth.ErrNoCredential)
		_, err = h.Handle(with("Plain @acme/admin"))
		x.ErrorIs(err, auth.ErrNoCredential)
		d, k := remote.counts()
		x.Zero(d)
		x.Zero(k)
		token := p.keyed(t, "one")
		for range 5 {
			_, err = h.Handle(with("Bearer " + token))
			x.ErrorIs(err, auth.ErrUnavailable)
		}
		d, k = remote.counts()
		x.Equal(1, d)
		x.Zero(k)
		remote.mu.Lock()
		remote.failure = false
		remote.mu.Unlock()
		time.Sleep(2 * time.Minute)
		_, err = h.Handle(with("Bearer " + token))
		x.NoError(err)
		d, k = remote.counts()
		x.Equal(2, d)
		x.Equal(1, k)
	})
	// Eager discovery still reports an outage to startup; configuration errors
	// are returned immediately in either mode.
	remote := &remoteIssuer{failure: true}
	ctx := oidc.ClientContext(t.Context(), &http.Client{Transport: roundTrip(remote.roundTrip)})
	_, err := authoidc.New(ctx, authoidc.Config{Issuer: issuer, Audience: audience})
	require.Error(t, err)
	_, err = authoidc.New(ctx, authoidc.Config{Issuer: issuer, Audience: audience, Lazy: true, RefreshInterval: -time.Second})
	require.ErrorContains(t, err, "negative")
	_, err = authoidc.New(ctx, authoidc.Config{Issuer: issuer, Lazy: true})
	require.ErrorIs(t, err, authoidc.ErrNoAudience)
}

func TestCanceledLazyCallerDoesNotCancelSharedDiscovery(t *testing.T) {
	p := newIdp(t)
	synctest.Test(t, func(t *testing.T) {
		x := require.New(t)
		remote := &remoteIssuer{keys: []jose.JSONWebKey{p.jwk("one")}}
		started, release := make(chan struct{}), make(chan struct{})
		client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/.well-known/openid-configuration" {
				close(started)
				select {
				case <-release:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			return remote.roundTrip(r)
		})}
		h, err := authoidc.New(oidc.ClientContext(t.Context(), client), authoidc.Config{Issuer: issuer, Audience: audience, Lazy: true})
		x.NoError(err)
		ctx := with("Bearer " + p.keyed(t, "one"))
		first, cancel := context.WithCancel(ctx)
		ended := make(chan error, 1)
		go func() { _, err := h.Handle(first); ended <- err }()
		<-started
		cancel()
		x.ErrorIs(<-ended, context.Canceled)
		go func() { _, err := h.Handle(ctx); ended <- err }()
		synctest.Wait()
		close(release)
		x.NoError(<-ended)
		d, k := remote.counts()
		x.Equal(1, d)
		x.Equal(1, k)
	})
}

func TestRemoteFetchHasABoundedTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, 2 * time.Second} {
		synctest.Test(t, func(t *testing.T) {
			x := require.New(t)
			client := &http.Client{Timeout: timeout, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				<-r.Context().Done()
				return nil, r.Context().Err()
			})}
			h, err := authoidc.New(oidc.ClientContext(t.Context(), client), authoidc.Config{Issuer: issuer, Audience: audience, Lazy: true})
			x.NoError(err)
			start := time.Now()
			_, err = h.Handle(with("Bearer token"))
			x.ErrorIs(err, auth.ErrUnavailable)
			want := timeout
			if want == 0 {
				want = 10 * time.Second
			}
			x.Equal(want, time.Since(start))
			x.Equal(timeout, client.Timeout)
		})
	}
}
