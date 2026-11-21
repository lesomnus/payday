# Accepting an SSO access token

An API should require an access token intended for that API. An OIDC ID token
is the sign-in client's proof of authentication and should not substitute for
an API credential. Enable the [RFC 9068 profile](https://www.rfc-editor.org/rfc/rfc9068.html#section-4):

```go
const resource = "jobs-api"
h, err := authoidc.New(ctx, authoidc.Config{
    Issuer: "https://sso.example.com",
    Audience: resource,
    AccessToken: true,
    Claims: func(ctx context.Context, token *oidc.IDToken) (auth.Identity, error) {
        c, err := authoidc.ReadAccess(token, resource)
        if err != nil {
            return auth.Identity{}, err
        }
        var actions []string
        for _, scope := range c.Scope {
            switch scope {
            case "jobs.read":
                actions = append(actions, "/app.JobService/Get", "/app.JobService/List")
            case "jobs.approve":
                actions = append(actions, "/app.JobService/Approve")
            }
        }
        return auth.Identity{
            Authority: "company-sso",
            Id: c.Subject,
            Grant: frame.Whole().To(actions...),
        }, nil
    },
})
if err != nil {
    return err
}
```

The handler verifies the signature, issuer, audience and validity window, then
requires protected `typ` equal to `at+jwt` or `application/at+jwt`, a nonempty
subject, client ID and token ID, and numeric issuance and expiry dates. The
claims mapper runs only after those checks. `ReadAccess` exposes `ClientId`,
`Subject`, `Audience`, `Scope`, `Id` (`jti`), `DateIssued`, `DateExpires` and
`DateAuthenticated`. It accepts a verified token, not an untrusted JWT string.
`Scope` is an optional space-separated string; its meaning remains yours. An
unrecognized scope in the example grants no operation. The holder's policy and
tenant wall still apply even to an operation the token permits.

For a static or custom key set, use
`authoidc.AccessVerifier(verifier, resource, mapper)`. The supplied verifier must
check issuer, signature and expiry; the wrapper also requires the resource
audience. `authoidc.Verifier` and `Config.AccessToken: false` remain available
for an endpoint that intentionally consumes OIDC ID tokens. An issuer using a
proprietary access-token profile needs an explicit mapper and validation for
that profile; turning off this option is not equivalent protection.

## Start during an issuer outage and follow key rotation

Reuse one handler for the server's lifetime. `New` discovers the issuer at
startup by default; JWKS is fetched when the first token needs a key. Set
`Lazy: true` to defer discovery too, so public routes or another authentication
method can serve during an issuer outage:

```go
h, err := authoidc.New(ctx, authoidc.Config{
    Issuer: "https://sso.example.com",
    Audience: resource,
    AccessToken: true,
    Lazy: true,
    RefreshInterval: time.Minute,
    Claims: directoryClaims, // your mapper from the previous example
})
```

The default interval is one minute. Remote discovery and JWKS responses,
including failures, are reused during that interval; simultaneous misses share
one fetch. Random `kid` values and bad signatures under a known `kid` cannot
cause a remote fetch for every request. Known cached keys continue to verify
valid tokens during an outage. A newly published signing key can take up to the
interval to be fetched after the previous attempt, so publish replacement keys
before issuing tokens that need them. This is a refresh limit, not a token
revocation mechanism or a maximum age for already cached signing keys.

With lazy discovery, a request carrying no Bearer credential does not contact
the issuer. An unavailable discovery endpoint returns `auth.ErrUnavailable`
for token-bearing calls and can be retried after the interval. Canceling one
caller does not cancel shared discovery for others. `oidc.ClientContext` still
selects your HTTP client; payday copies it, preserves its settings, and supplies
a ten-second timeout only when it had none. `Verifier`/`AccessVerifier` with a
caller-supplied key set retain that key set's own fetching policy; these limits
belong to `New`.

## Map a directory subject to the local holder

`Authority` is a trusted namespace chosen in the verified claims mapper. Keep
it stable for an issuer, such as `company-sso`; do not copy a request header or
an arbitrary token claim into it. `Id` is the directory's original opaque
subject, which need not be a payday ID. Your `auth.Resolver` looks up the pair
`(Authority, Id)` in your directory-link table and resolves the resulting local
Holder through your ordinary holder resolver. Only that local actor and its
tenant enter `frame.Frame`. Two issuers can use the same subject text without
naming the same person.

The default `authoidc.Subject` assumes the issuer already uses this app's own
IDs. Use the explicit mapper above for an external directory. Never derive a
local payday ID by parsing or hashing an arbitrary external subject. A token
with an empty or absent `sub` is refused even with a custom mapper, and does not
fall through to another handler in `auth.Seq`.

## Approval, devices and refresh

- A person approves a Job: map the external subject to their Holder, apply the
  local approver policy, and require [a recent sign-in](fresh-authentication.md).
  `auth_time` is preserved as `DateAuthenticated`; missing evidence stays zero.
- A Robot uses client credentials: `ClientId` names the OAuth client and `sub`
  can name a service account. Map it to a local machine identity. Neither a
  client ID nor a scope establishes that a human approved anything.
- A refreshed token carries a new `iat` but an older `auth_time`: its validity
  can continue while an approval still requires another sign-in. Issuance is
  not substituted for authentication time.
