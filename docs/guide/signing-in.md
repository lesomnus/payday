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

	// Whatever checking a secret means in this deployment.
	v, err := roster.Verify(ctx, body.Tenant, body.Alias, body.Password)
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

## Do I need Hydra, or an OIDC provider at all?

**One app: no.** The app checks the secret, sets its own cookie, and reads it
back. Nothing else is involved and nothing is signed.

**Several apps, one sign-in: yes.** App A's cookie means nothing to app B, and
the thing that fixes it — a credential with an issuer, a JWKS endpoint, expiry,
refresh and revocation — *is* OIDC. Writing it yourself is writing Hydra.

So the question is never "password or OIDC". It is **one relying party or
many**. An air-gapped single app needs neither an IdP nor a token.

When you do have one, the cookie does not go away: the provider hands your
backend a token, and the browser still carries a session your app set. Those are
two different cookies for two different jobs, and only the second one is this
package's.

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

## Sessions do not slide

`DefaultLifetime` is twelve hours, absolute. A session extended by use is one a
thief keeps alive forever by using it — the legitimate owner signing in again
does not disturb it, and nothing ever expires.

If you want longer, say so:

```go
authsession.New(store, authsession.WithLifetime(24*time.Hour))
```

If you want renewal, issue a new session. That way it is a decision somebody
made rather than a side effect of traffic.

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

`Seq` takes the first handler that finds anything. A request with no cookie
falls through to the certificate; a request with a cookie that names **nothing**
does not — it is a credential that is there and wrong, and serving it as
whatever the next handler makes of it would be answering a question nobody
asked.

## See also

- [permissions.md](permissions.md) — what a caller may do once you know who they are
- [server.md](server.md) — the chain the handler goes in
- [`auth/authsession`](../../auth/authsession) — the package comment is the detail
