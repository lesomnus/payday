# Read-only method policies

`rbac` supplies roles, groups and bindings without owning an application's
database. A `Source` returns grants for a canonical `@tenant/subject` key;
`Union` composes independent sources and returns lookup failures as errors.
The application decides how authenticated identities become these keys and how
tenant and object visibility are enforced.

```yaml
roles:
  '@operator/reader':
    methods:
      - /app.ManifestService/Get
      - /app.ManifestService/List
      - /payday.ResourceService/Watch
groups:
  '@operator/readers':
    members: ['@operator/kamino', '@operator/catalogue']
bindings:
  - role: '@operator/reader'
    group: '@operator/readers'
```

A binding names exactly one holder or group. Groups have explicit members and
cannot nest. References stay within one tenant; unknown roles or groups and
invalid method patterns reject the entire document. An empty policy denies
everything. Methods use `frame.Covers`: `*` replaces a whole package, service or
method component, and `/*.*/*` covers all methods. Registry path globs and deny
rules are not this grammar.

`rbacfile.Open(rbacfile.Config{File: path})` validates the first document before
serving. Run its `Spin` worker for refreshes (five seconds by default), and put
its `Interceptor()` before authentication and authorization. Each unary request
pins one immutable snapshot; accepted changes end streams with
`Unauthenticated` so clients reconnect under the new policy. Reload accepts two
consecutive identical reads, retains the last valid snapshot on malformed or
missing files, and logs failures. A valid empty file removes this source's
grants. Every replica must run the refresh worker.

`Open` and `NewStore` also accept application validation callbacks, checked
before every replacement. Use these for identity constraints. Mutable aliases
are unsuitable keys for directory privileges unless every way to acquire an
alias enforces permission delegation. UUID components allow an application to
bind immutable tenant and holder identities instead.

Read grants through the same source in the gate, scope calculation, grant
delegation, credential-target checks and permission introspection. Omitting
file grants from a target check can let a narrow operator reset a more
privileged user's credentials. Combining file and database sources is a union:
removing a file binding cannot withdraw a grant still supplied by the database.
File groups have their own membership; mapping them to mutable database groups
requires the same delegation checks as database bindings.

## Notify the holders of a role

Use `Snapshot.Holders`, `Store.Holders` or `rbacfile.File.Holders` to select
names for a role-based notice. Direct bindings and bound groups contribute
their holders; duplicate memberships produce one name, sorted for stable
delivery. A valid role name with no bindings returns an empty list, even if
the role is not declared. A malformed name returns an error.

```go
// policy is the same *rbac.Store or *rbacfile.File used by the request gate.
// The interceptor already pins requests. Background work pins explicitly.
ctx = policy.Pin(ctx)
names, err := policy.Holders(ctx, "@operator/reader")
if err != nil {
    return err
}
for _, name := range names {
    // Resolve the canonical name using the application's directory and
    // enqueue its notification. The application supplies this function.
    if err := enqueueNotice(ctx, name, resourceID); err != nil {
        return err
    }
}
```

The pin keeps `Holders` and `Grants` on one revision even if a file refresh
arrives midway. A new operation pins a fresh context. For example, a purchasing
role can select recipients when an engineering change takes effect, and a
reader role can select recipients for a publication notice. Membership selects
recipients; the application's object policy still decides whether a recipient
may read that change or publication. These names can include service holders,
so apply any human-recipient requirements in the application.

`Source` and `Union` only promise forward grant lookup: an arbitrary external
directory may not support enumerating all holders. For combined directories,
compose their explicit recipient lookups in the application and preserve lookup
errors rather than treating an unavailable directory as an empty audience.

Credential restrictions remain an intersection with policy. `frame.IntersectMethod`
handles crossing patterns such as `/app.*/Get` and `/app.ManifestService/*`.
Config-backed authentication can attach `Identity.Changed` to the same accepted
revision, so token removal ends existing streams too. `Identity.Authority` is
trusted in-process provenance, never a claim to copy from an incoming header.
Resolvers may return named frames without row IDs, but applications must reject
operations needing persistent ownership or audit identity. A missing tenant ID
must produce an empty tenant scope, never unrestricted visibility.
