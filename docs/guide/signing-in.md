# Putting a login in front of an app

A browser has nowhere safe to keep a credential. Script that can read a token is
script that can send it somewhere else, so what a browser gets is an opaque
cookie it cannot read, naming a session this server keeps.

`auth/authsession` is both halves of that: the endpoint that sets the cookie and
the handler that reads it back. You supply two things — how to check a secret,
and where sessions live.

## The short version

```go
sessions := authsession.New(store)

chain := grpcx.Serving(ctx).
	WithUnary(auth.InterceptorUnary(sessions.Handler(), Resolver(s.Ungated), public)).
	WithStream(auth.InterceptorStream(sessions.Handler(), Resolver(s.Ungated), public))

// ... and on the HTTP listener, which is the mux `web.New` answered:
h.Handle("POST /session", sessions.Serve(login))
h.Handle("DELETE /session", sessions.Serve(login))
```

`login` is yours:

```go
func login(ctx context.Context, r *http.Request) (authsession.Session, error) {
	var body struct{ Tenant, Alias, Password string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return authsession.Session{}, err
	}

	// Whatever checking a secret means in this deployment. Here, an identity
	// service the deployment already runs.
	v, err := directory.Verify(ctx, body.Tenant, body.Alias, body.Password)
	if err != nil || !v.GetOk() {
		return authsession.Session{}, errors.New("no")
	}

	who, err := pdid.From(v.GetHolder())
	if err != nil {
		return authsession.Session{}, err
	}
	tenant, err := pdid.From(v.GetTenant())
	if err != nil {
		return authsession.Session{}, err
	}

	return authsession.Session{
		Id:       who.String(),
		TenantId: tenant.String(),
		Grant:    frame.Whole(),
	}, nil
}
```

That is the whole of it. What follows is the parts people get wrong.

## Or serve it as an RPC

`Serve` is a wrapper. What it wraps is exported, so a sign-in does not have to
be the one endpoint in a different protocol from everything else your app
offers:

```go
func (s server) SignIn(ctx context.Context, req *app.SignInRequest) (*app.SignInResponse, error) {
	v, err := verify(ctx, req)   // whatever checking a secret means here
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "no")
	}

	_, c, err := sessions.Mint(ctx, v)
	if err != nil {
		return nil, err
	}

	return &app.SignInResponse{}, grpc.SetHeader(ctx,
		metadata.Pairs("set-cookie", c.String()))
}
```

It sounds as though it should not work — a cookie is an HTTP response header
and a gRPC handler has no response writer — and it does. `set-cookie` as
response metadata reaches the browser through [`web.Transcode`](../../web) as a
header like any other, and the cookie it sends back arrives as request
metadata, which is where `Sessions.Handler` already reads one.

`sessions.End(ctx, key)` is the other half, and returns the cookie that clears
it.

**Why you might want this.** A schema is one definition every language
generates from, so a sign-in declared there has the same shape in Go, in
TypeScript and in anything else — rather than one call somebody implements by
reading a document. In an app whose every other operation is an RPC, the odd
one out is the one that gets implemented differently in each client.

**What you give up.** `Serve` maps failures to statuses for you — 401 for a
refusal, 503 for a store that would not take it, 204 and no body. An RPC maps
them to whatever its own contract says, and `Mint` answers `ErrNobody` for a
session that names nobody so you can tell the two apart.

## Do I need Hydra, or an OIDC provider at all?

**One app: no.** The app checks the secret, sets its own cookie, and reads it
back. Nothing else is involved and nothing is signed.

**Several apps, one browser sign-in: yes.** App A's cookie means nothing to app
B, and the thing that fixes it for a *browser* — a redirect flow, an issuer, a
JWKS endpoint, refresh — *is* OIDC. Writing it yourself is writing Hydra.

When you do have one, the cookie does not go away: the provider hands your
backend a token, and the browser still carries a session your app set. Those are
two different cookies for two different jobs, and only the second one is this
package's.

**Several apps, one API token: no, and see below.** A token somebody pastes into
a script is not a browser sign-in and needs none of the redirect flow. What it
needs is for the app receiving it to find out what it means, which is
[`payday.TokenService`](#accepting-a-token-another-server-issued).

So the question is not "password or OIDC" and not even "one relying party or
many". It is **who is holding the credential** — a browser being sent somewhere
to sign in, or a caller presenting a string.

## Accepting a token another server issued

An opaque token carries nothing. That is what makes it revocable and it is also
why the server it arrives at cannot read it: the string means something only to
whoever issued it, so that issuer has to be asked.

`payday.TokenService` is the asking, and `auth.Remote` is the client half:

```go
conn, err := grpc.NewClient(addr, creds, /* this app's own credential */)
h := auth.Bearer(auth.Remote(pdpb.NewTokenServiceClient(conn)))
```

That is the whole of the wiring. Everything downstream already works: the
interceptor checks `Grant.Allows(method)` against the method gRPC dispatched,
`gate` meets the grant with whatever your policy answered, and your resolver
looks the actor up in your own rows exactly as it does for every other
credential.

**The connection carries your app's credential, not the bearer's.** That is what
makes the service safe to serve — the issuer answers only apps it knows, and an
operator decides which of them may ask.

### What crosses, and what does not

| | |
| --- | --- |
| who the bearer is | an identifier, or a `tenant`/`alias` pair |
| what the token was narrowed to | the three axes of a `frame.Grant` |
| when it stops working | so that a stream is cut by it |
| **what they may do in your app** | **does not cross.** Your policy decides, unchanged |

The method names in a grant are *your* app's, and the issuer stores them without
checking them — it would need every app's service descriptors, and a token would
stop working the day you added an RPC it had not heard of. What makes that safe
is that a grant only ever takes away: a method named in one that the actor
cannot call is still refused.

### Implementing the other half

An identity store implements `Introspect` and answers with `auth.Introspection`:

```go
func (s server) Introspect(ctx context.Context, req *pdpb.TokenIntrospectRequest) (*pdpb.TokenIntrospectResponse, error) {
	id, err := s.store.Lookup(ctx, req.GetToken())
	if err != nil {
		return nil, status.Error(codes.NotFound, "no such token")
	}

	return auth.Introspection(id)
}
```

Use the encoder rather than filling the message out by hand. A `Grant` writes a
flag beside each list because "every tenant" and "no tenant at all" are the same
empty list on the wire, and the encoder is the one place that rule is applied.

`NotFound` for every refusal about the token — unknown, expired, revoked. Told
apart they are an oracle for "this string was a real token once".

### It asks on every request

There is no cache, deliberately: a token revoked a second ago stops working now,
which is the whole reason to carry an opaque one. A deployment that cannot pay a
round trip per request should wrap `auth.Remote` in a store where the window it
is accepting is written down.

## Where sessions live

`authsession.MemStore` keeps them in the process. It is right for one replica
and **silently wrong** for two — a browser is signed in or out depending on
which one the load balancer picked, per request, with nothing in any log saying
so. It is the same trap `watch`'s memory broker carries.

A real store is a table with the key indexed, or a cache with one. Implement
three methods:

```go
type Store interface {
	Put(ctx context.Context, v Session) error
	Get(ctx context.Context, key string) (Session, error) // ErrNoSession if absent
	Del(ctx context.Context, key string) error
}
```

A store may forget a session whenever it likes. Expiry is checked by the handler
regardless, so a cache with a TTL is a legitimate store and a table you never
sweep is too.

### Or in the cookie

`authsession.NewSealed(key)` is a store with nothing to store: the session is
encrypted into the cookie under a key every replica holds, and reading it back
is opening it. The contract with the browser does not change — a key goes out
at sign-in and comes back on every call — only who makes the key does, which
is why it is a `Store` that also answers `Seal`.

```go
sessions := authsession.New(sealed)   // sealed, _ := authsession.NewSealed(key, older...)
```

What it gives up is the sentence the package comment leans on: nothing on the
server can end a sealed session before its clock does. `End` clears the cookie
in that browser and nothing else, and there is one clock rather than two,
since an idle deadline moves by being written back to a store. So it is right
for an app whose session is a **handle to something ended elsewhere** — a front
door holding a delegation an identity store can revoke — and wrong for one
where the session is the thing to revoke. The handle rides in `Session.Held`,
which every store either keeps or refuses with `ErrCannotHold`; `Sessions.Read`
is how the app gets it back from a cookie's value.

The first key seals and every key opens, which is rotation: put the new one
first, keep the old ones until what they sealed has expired, then drop them.

## Serving over plain HTTP

The cookie is `Secure` and named `__Host-pd_session`, which a browser refuses to
store over `http://`. That is deliberate: a session cookie without `Secure` is
one anybody on the network can read and then be.

For a checkout:

```go
authsession.New(store, authsession.Insecure())
```

It logs a warning once per process and renames the cookie to `pd_session`, so a
deployment that left it on is visibly different rather than quietly weaker.

## A page on another origin

Every `npm run dev` is one. Two things have to line up:

- **The server** names the origin, in `server.http.origins`. payday then sends
  `Access-Control-Allow-Credentials` for it. Never `*` — a browser refuses to
  combine that with credentials, which is why the origin is echoed.
- **The page** asks for it, on every call:

  ```js
  fetch(url, { credentials: "include" })
  ```

  A connect-es transport takes the same option. Leaving it out is a login that
  works in `curl` and does nothing in a browser, with no error anywhere.

## CSRF

The cookie is `SameSite=Lax`, and every RPC payday serves is a POST, so a
cross-site POST carries no cookie. That is the defence, and it needs no token.

What `Lax` still allows is a top-level GET navigation, which is what keeps a
link in an email from landing somebody signed out. `Strict` gives that up and
buys nothing here, since there is no GET to protect. `None` gives up the defence
entirely and needs a real anti-CSRF token; take it only for an app deliberately
embedded cross-site.

## Two clocks

```
DefaultIdle       30 minutes   how long an unused session survives
DefaultLifetime   12 hours     how long any session survives, used or not
```

The idle one moves forward as the session is used and never past the absolute
one. That is what makes a short idle window usable: somebody working does not
sign in again every half hour, and somebody who walked away is gone in one.

```go
authsession.New(store,
    authsession.WithIdle(15*time.Minute),
    authsession.WithLifetime(24*time.Hour))
```

`WithIdle(0)` turns the first one off, for a deployment that wants only the cap.

**Why both.** A sliding expiry alone is a stolen key that works for as long as
somebody keeps using it; the absolute cap is what closes that, and the pair is
the standard arrangement. An absolute-only session has to be long enough to be
usable, which makes ending one early a separate problem to solve -- and solving
it separately is how an app grows a push channel that becomes the only thing
between somebody leaving and their access ending.

**What it costs.** The idle deadline is a write, so it moves only once a session
is more than halfway to stale: at most one write per half-window per session,
and a deadline accurate to that half.

## Signing out

`DELETE /session` deletes the row and clears the cookie. Deleting the row is the
part that matters: the key is dead in every browser that had it, immediately,
which is the thing a self-contained token cannot do.

It answers 204 even when the store refuses, because the caller asked to be
signed out and the cookie is cleared either way — a 503 leaves somebody looking
at a page that says they are still signed in.

## What the endpoint answers

204 and no body. What a page needs about the person is a request it should make,
against the same server, behind the same wall. An answer composed at sign-in is
a second place that has to change when the app's idea of a person does.

## Mixing with other credentials

A deployment with a browser in front of it usually has services behind it:

```go
auth.Seq(sessions.Handler(), auth.MTLS())
```

`Seq` takes the first handler that finds anything, and one that finds something
**wrong** — or that cannot tell — stops the search rather than falling through;
the rule is [permissions §4](permissions.md#4-who-is-asking). For a cookie that
means a request carrying none reaches the certificate, while one naming a
session that has expired, been signed out, or lives in a store that is down
does not.

An app that also takes API tokens adds the third:

```go
auth.Seq(sessions.Handler(), auth.Bearer(auth.Remote(client)), auth.MTLS())
```

Each reads a different place — a cookie, an `authorization` scheme, the peer's
certificate — so ordering decides only what happens to a request that carries
more than one, and a caller sending two has not decided what it is.

## See also

- [permissions.md](permissions.md) — what a caller may do once you know who they are
- [server.md](server.md) — the chain the handler goes in
- [`auth/authsession`](../../auth/authsession) — the package comment is the detail
- [`auth.Remote`](../../auth/remote.go) — the client half of `payday.TokenService`
