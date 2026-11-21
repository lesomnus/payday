# Requiring a recent sign-in

A valid credential and a recent sign-in answer different questions. A layer or
admission policy reads the verified facts from `frame.From(ctx)`:

```go
f, ok := frame.From(ctx)
if !ok {
    return status.Error(codes.Unauthenticated, "sign in")
}
method := f.Authentication.Method
date := f.Authentication.Date
```

`Method` identifies the handler that admitted the call, such as `plain`, `oidc`,
`session`, `bearer` or `mtls`. `Date` is the original authentication time the
verifier knows. Zero means unknown. Neither field widens the grant or changes
the tenant wall.

## An approval needs a recent sign-in

Run your existing approver/person policy first. The following additional check
requires a direct call with authentication within five minutes:

```go
func requireRecentSignIn(ctx context.Context) error {
    f, ok := frame.From(ctx)
    if !ok {
        return status.Error(codes.Unauthenticated, "sign in")
    }
    if f.Caller != nil {
        return status.Error(codes.PermissionDenied, "approve directly")
    }
    switch f.Authentication.Method {
    case "oidc", "session": // the methods this deployment permits for approvals
    default:
        return status.Error(codes.PermissionDenied, "use the approval sign-in flow")
    }
    if !f.Authentication.Fresh(time.Now(), 5*time.Minute) {
        return status.Error(codes.FailedPrecondition, "sign in again before approving")
    }
    return nil
}
```

`Fresh` refuses unknown and future times, and a non-positive window. Its clock
is explicit, so the application's tests can check the exact boundary. For a
transactional write rule, make this check in the layer or
[admission policy](admission.md) that decides whether to commit the approval.

When writing the approval record, copy the actor ID, authentication method and
date alongside the content hash and policy version. Those are facts about that
approval; reading the holder's current state later cannot reconstruct them.
Payday's general audit row does not automatically add these application fields.

## A browser session must preserve the verification time

When your sign-in verifier has just checked a password or another proof, it can
supply that time in the session:

```go
// Run only after verifying the person's proof.
session := authsession.Session{
    Id: who.String(),
    TenantId: tenant.String(),
    Grant: frame.Whole(),
    DateAuthenticated: time.Now().UTC(),
}
_, cookie, err := sessions.Mint(ctx, session)
```

If a trusted verifier supplies the original time, copy that instead of using
`time.Now()`. `Mint` does not invent a time when it is absent. Reading a session,
renewing its idle deadline and calling `Inspect` or `Recheck` preserve it. Both
the memory and sealed-cookie stores retain it; a custom `authsession.Store`
must preserve `DateAuthenticated` too.

For OIDC, `auth/authoidc` reads `auth_time` from the verified token. It does not
substitute `iat`, which describes token issuance. A refreshed token can carry
an old authentication time; a token without `auth_time` carries unknown time.
Ask the provider for `max_age` or the appropriate essential claim when the
flow needs that evidence. This follows the [OIDC ID Token contract](https://openid.net/specs/openid-connect-core-1_0.html#IDToken).

Opaque token stores can supply `auth.Identity.DateAuthenticated` when they know
it. `auth.Introspection`, `TokenService` and `auth.IdentityFrom` preserve that
time across the service boundary. The receiving handler still determines
`Method`; an introspection response cannot pretend the caller used OIDC.

## A device records work for a person

In a [represented call](representing.md), the two parties keep separate
facts:

```go
personProof := f.Authentication
deviceProof := f.Caller.Authentication // when Caller is non-nil
```

The presence verifier supplies the person's `Authentication` on the frame it
returns. Payday supplies the device's facts from its credential. A recent
device authentication or the request's recorded timestamp never refreshes the
person's time. A queued approval therefore retains the evidence for its
original person; an application's late-write policy must explicitly decide
whether that evidence suffices.

## A machine may record results but may not approve

A deployment can permit a `bearer` or `mtls` credential to write test results
and require its approval sign-in flow for approvals. Keep that rule alongside
the app's verified actor/person policy. The method is a credential hint:
`session` and `oidc` alone do not prove a human, MFA or physical presence, and
`bearer` alone does not prove a machine. There is no inferred machine flag.

`Plain` trusts a name and records no authentication time. Long-lived keys and
certificates also have no new human sign-in time merely because they are used.
An app that needs MFA assurance must verify and record its own proof-level
facts in addition to method and time.
