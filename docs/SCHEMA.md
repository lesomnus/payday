# The generation contract

payday owns part of your schema. This is what that means, what it costs, and
what you are allowed to do to it.

[The schema guide](guide/schema.md) is how to declare an entity. This is the
contract underneath — read it when you want to change one of payday's own
entities, or when you want to know why an identifier looks the way it does.

- [1. What payday owns](#1-what-payday-owns)
- [2. Overlays: adding, never overriding](#2-overlays-adding-never-overriding)
- [3. Whose names these are](#3-whose-names-these-are)
- [4. Edges across packages](#4-edges-across-packages)
- [5. Identifiers](#5-identifiers)
- [6. Slugs](#6-slugs)

## 1. What payday owns

Four entities: **Tenant**, **Holder**, **Audit**, **Outbox**.

They are owned because everything that reads a request reads them. The wall
narrows by tenant, the trail stamps who acted, a rate limit counts against
somebody. An app that declared its own would be declaring the thing the
framework is built on.

### It is a contract, not a Go type

payday does **not** ship `payday.Tenant` as a Go struct for you to import. It
ships `.proto` files, and `pd gen` copies them into your app and generates from
them alongside your own entities.

That is the whole reason your added fields work. A Go type in a library has the
fields the library gave it; a `.proto` merged with your overlay produces a
message, an ent schema and a server with **your** fields on them, in one module,
with one set of types.

```
payday/schema/payday/tenant.proto     the source
        ↓ pd gen copies and merges with your overlay
your-app/proto/payday/tenant.proto    what your app generates from
        ↓
your-app/tenant.pb.go, internal/ent/tenant.go, server/bare/tenant.g.go
```

### The cost

Your app carries generated copies of payday's entities, and upgrading payday can
change them. That is why three things exist:

- `pd gen --check` in CI, so a copy that was not regenerated is caught.
- `version.Same`, refused at `NewSink`, so generated code and linked payday
  cannot disagree at run time.
- `migrate.Check` before serving, so a field that arrived in `internal/ent`
  cannot be served against a database that does not have the column.

None of those is optional decoration. A field added upstream arrives quietly: it
compiles, and the tests pass against a database the tests just created.

## 2. Overlays: adding, never overriding

An overlay is a fragment declaring what your app adds to one of payday's
entities:

```proto
// proto/ext/payday/holder.ext.proto
message Holder {
  string department = 20;
}
```

`protobuf-merge` unions it with payday's. Overlays are **excluded from buf**,
because they are not files that compile — they name messages that exist only
after the merge.

### What is refused

**Touching a payday-owned field number.** Adding is allowed; redeclaring is a
generation failure. Without it, an overlay could quietly redefine `alias` and
everything reading `alias` would be reading something else.

**An overlay for an entity payday does not have.** It would simply never be
merged, and the first sign would be a missing column. `pd doctor` names the
entities payday does have, because the cause is nearly always a typo.

**An overlay that changes what the file is.** `features.field_presence =
IMPLICIT` copied out of an entity file lands on the *whole merged contract* —
every field of every message in it, including the generated ones. This was found
by an overlay that took `HasId` off an Add request and stopped the build, which
is the lucky version; on a field nobody calls `Has` on, it would silently change
what "not set" means on the wire.

### The number ranges

payday reads some numbers by name across every entity, so that code holding a
row it has no type for can still read the parts every row has. The table is in
[the schema guide](guide/schema.md#3-the-field-numbers).

**Field 3 is yours and payday will never spend it.** It is reserved for an edge
to a set smaller than a tenant — a site, a fleet, a project — and putting one
there gives you a second axis every read is narrowed by.

### An added field may be a message

An overlay can add a field whose type is a message — a `Profile` on the Holder,
say. It is stored as the canonical protobuf JSON in a `jsonb` column, and needs
no declaration beyond the field. See
[the schema guide](guide/schema.md#a-field-can-be-a-message) for what that costs:
it cannot be filtered or indexed, it is replaced whole, and renaming one of
*its* fields is a migration nothing checks.

This did not work until recently, and the way it did not work is worth knowing
because the shape recurs. It generated an `encoding/json` column, and every
message payday generates uses the protobuf opaque API — whose fields are all
unexported. So `encoding/json` saw nothing, every value round-tripped as `{}`,
and the Add answered with what it had been handed, so it looked stored right up
until somebody read it back.

## 3. Whose names these are

Your app has `app.Tenant`, not `payday.Tenant`. `pd gen` rewrites the `package`
line of the copied entities to your app's proto package, read from your own
entities' `go_package`.

This is unconditional, not opt-in. Nobody wants a concept as central as *tenant*
or *holder* bound to a vendor's namespace. Using payday's shape should not mean
being unable to leave.

**`payday.BatchService` is the one exception.** It is not a message describing
your domain, it is a transport — a shared contract that anything speaking batch
to any payday app can rely on.

### The `own:` marker

Because the package name is no longer fixed, payday cannot find its own entities
by full name. Each carries a marker instead:

```proto
option (payday.entity) = {own: OWN_TENANT};
```

It is written only in payday's shipped schema and copied along with everything
else. It is what the overlay drift check and the domain registration key on.

### Two apps sharing a boundary

If a second app shares users and permission boundaries with the first, it will
want the first app's `Tenant` and `Holder`. Match the names and it works —
**keeping the two compatible is the app's responsibility**, not payday's. There
is no mechanism enforcing it and there should not be: that is a claim about two
deployments that payday cannot check.

## 4. Edges across packages

An entity in `app` can hold an edge to an entity in `payday` — that is how
`Robot.tenant` works at all. It requires two things, and both are enforced.

### `strategy: all`

buf must compile the whole workspace as one unit rather than file by file, or
the generator sees a type it cannot resolve. The `buf.gen.yaml` is written by
payday rather than by the app, precisely so this cannot be got wrong.

### One `go_package` per app

Every entity in an app has to generate into the same Go package. Two packages
and the ent schemas split, and the edges between them do not compile.

`pd gen` refuses an app with two, naming both files.

**Two *proto* packages are allowed**, and are a different question: one of them
has to say `option (payday.app) = {};`, because that is what decides where
payday's own entities are copied. Neither saying it is refused, and so is both.
See [more than one proto package](guide/packages.md), which also covers two apps
linked into one process — a case with a sharper failure, since a shared package
is a duplicate path in the protobuf registry and a panic before `main`.

### One upstream sharp edge

`protoc-gen-orm-service` panics if an edge target is declared *after* the entity
referencing it — `generated filepath to the entity not found`. Declare the
target first.

## 5. Identifiers

A UUIDv8 carrying one byte that says what kind of thing it names.

v8 is the version the standard defines as "everything except version and variant
is up to the implementation", so this is a layout payday defines rather than a
variation on v7.

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                          unix_ts_ms                           |  byte 0..3
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|          unix_ts_ms           |1 0 0 0|          seq          |  byte 4..7
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|1 0|    rand   |     domain    |             rand              |  byte 8..11
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                             rand                              |  byte 12..15
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| Where | Bits | What |
| --- | --- | --- |
| 0..5 | 48 | `unix_ts_ms` |
| 6 high | 4 | version = `8` |
| 6 low + 7 | 12 | `seq`, monotonic within one millisecond |
| 8 high | 2 | variant = `10` |
| 8 low | 6 | random |
| **9** | **8** | **domain** |
| 10..15 | 48 | random |

54 random bits, and ordering guaranteed up to 4096 rows per millisecond.

### What the domain byte buys

- **A reference can be checked before the database is.** A Holder's identifier
  handed to something wanting a Tenant's is refused at the edge, naming the kind
  it actually was — rather than becoming a query matching nothing and a
  `NotFound` saying nothing.
- **A row that is gone can still be named.** The trail records what was written
  to by identifier, not by table, and an identifier is unique across tables.
- **A page can route on one.** A client holding an identifier knows what kind of
  thing it is without asking.

### Domains come from the schema

```proto
option (payday.entity) = {domain: 7};
```

One declaration produces the constant, the registration and the name. The
alternative — a hand-maintained `domain.go` with constants, a `String()` and a
`DomainString()` — compiles fine when you add an entity and forget one of the
three.

payday reserves 1–6 for its own (Tenant 1, Holder 2, Audit 3, Outbox 4); apps
start at 7. A number is like a field number: fixed once chosen, and never reused
after an entity is deleted. `0` means unknown and is not assigned.

Both the write path and the read path check it. Writing is `Minter`; reading
refuses a reference whose domain is wrong before it becomes a query.

## 6. Slugs

The human-readable way to name a row.

```
@[TENANT/]ALIAS[#DOMAIN]

@acme/arm-01#robot   the robot "arm-01", held by the tenant "acme"
@acme/arm-01         what kind of thing it is comes from where it is written
@acme#tenant         a tenant, which nothing is above
arm-01               the tenant comes from who is asking
```

A slug is what a **person** writes: an authorization header, a key in a config
file, a URI in a certificate, a line in a log, a CLI argument.

**The wire does not carry one.** A reference in a request is a structured
message the server resolves. A slug is parsed at the edge and turned into one.

Every entity with an `alias` and a uniqueness constraint gets a
`<E>RefBySlug` — not just Holder. Tenant differs only in that its alias is
unique across the whole deployment rather than within one.

The domain can be left off when the surrounding context implies it:
`RobotService.Get` expects a Robot, so `Slug.Expect` supplies the domain and
`@acme/arm-01` resolves.

## See also

- [guide/schema.md](guide/schema.md) — how to declare an entity
- [TENANCY.md](TENANCY.md) — what the tenancy declaration means
- [RUNTIME.md](RUNTIME.md) — what is generated from all of this
