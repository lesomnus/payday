package authoidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/lesomnus/payday/auth"
)

// AccessClaims is the common part of an RFC 9068 access token. Scope is the
// issuer's vocabulary, not a payday grant: the app decides which operations
// those words permit. ClientId names the OAuth client, not necessarily a person.
type AccessClaims struct {
	Issuer            string
	Subject           string
	Audience          []string
	ClientId          string
	Scope             []string
	Id                string // jti, the token identifier, not the subject
	DateIssued        time.Time
	DateExpired       time.Time
	DateAuthenticated time.Time
}

// AccessVerifier is Verifier for an API accepting RFC 9068 access tokens.
// It verifies the signature first, then checks the protected type and required
// claims before calling the app's mapper. Audience is required even if the
// supplied verifier was configured to skip that check. Nil claims is Subject.
// The supplied verifier must still verify the issuer, signature and expiry.
func AccessVerifier(v *oidc.IDTokenVerifier, audience string, claims Claims) auth.Handler {
	if audience == "" {
		return auth.HandlerFunc(func(context.Context) (auth.Identity, error) {
			return auth.Identity{}, ErrNoAudience
		})
	}
	return verifier(v, claims, audience)
}

// ReadAccess reads claims from a token already verified by AccessVerifier or
// New with Config.AccessToken. It checks the audience and required claims, but
// does not verify a signature or the protected header: an IDToken alone does
// not retain that header. It must never replace the handler's verification.
// Missing auth_time remains zero; iat is not evidence of a fresh sign-in.
func ReadAccess(t *oidc.IDToken, audience string) (AccessClaims, error) {
	if audience == "" {
		return AccessClaims{}, ErrNoAudience
	}
	if t == nil || t.Issuer == "" || t.Subject == "" || !slices.Contains(t.Audience, audience) {
		return AccessClaims{}, fmt.Errorf("%s: invalid access-token issuer, subject or audience", Method)
	}
	var c struct {
		ClientId string          `json:"client_id"`
		Id       string          `json:"jti"`
		Scope    json.RawMessage `json:"scope"`
		Issued   json.RawMessage `json:"iat"`
		Expires  json.RawMessage `json:"exp"`
	}
	if err := t.Claims(&c); err != nil {
		return AccessClaims{}, fmt.Errorf("%s: invalid access-token claims: %w", Method, err)
	}
	if strings.TrimSpace(c.ClientId) == "" || strings.TrimSpace(c.Id) == "" {
		return AccessClaims{}, fmt.Errorf("%s: access token requires client_id and jti", Method)
	}
	issued, err := numericDate(c.Issued, "iat")
	if err != nil {
		return AccessClaims{}, err
	}
	expires, err := numericDate(c.Expires, "exp")
	if err != nil {
		return AccessClaims{}, err
	}
	date, err := authenticatedDate(t)
	if err != nil {
		return AccessClaims{}, err
	}
	var scopes []string
	if len(c.Scope) > 0 {
		var scope string
		if string(c.Scope) == "null" || json.Unmarshal(c.Scope, &scope) != nil {
			return AccessClaims{}, fmt.Errorf("%s: scope must be a space-separated string", Method)
		}
		scopes = strings.Split(scope, " ")
		for _, scope := range scopes {
			if scope == "" {
				return AccessClaims{}, fmt.Errorf("%s: empty scope", Method)
			}
			// OAuth scope-token is printable ASCII except quote and backslash.
			for _, ch := range scope {
				if ch < 0x21 || ch > 0x7e || ch == '"' || ch == '\\' {
					return AccessClaims{}, fmt.Errorf("%s: invalid scope", Method)
				}
			}
		}
	}
	return AccessClaims{
		Issuer: t.Issuer, Subject: t.Subject, Audience: slices.Clone(t.Audience),
		ClientId: c.ClientId, Id: c.Id, Scope: scopes,
		DateIssued: issued, DateExpired: expires, DateAuthenticated: date,
	}, nil
}

// Read only after the exact compact token has passed Verify. This is a check
// on signed metadata, not a second JWT signature implementation.
func accessType(raw string) error {
	first, _, ok := strings.Cut(raw, ".")
	var header struct {
		Type string `json:"typ"`
	}
	b, err := base64.RawURLEncoding.DecodeString(first)
	if !ok || err != nil || json.Unmarshal(b, &header) != nil ||
		(header.Type != "at+jwt" && header.Type != "application/at+jwt") {
		return fmt.Errorf("%s: access token requires protected typ at+jwt", Method)
	}
	return nil
}
