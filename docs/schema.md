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
payday/schema/payday/tenant.proto          the source
        ↓ pd gen copies and merges with your overlay
your-app/proto/app/payday/tenant.proto     what your app generates from
        ↓
your-app/tenant.pb.go, internal/ent/tenant.go, server/bare/tenant.g.go
```

The `app` in that path is **your** proto package, not a fixed directory name:
the copies land inside your package rather than beside it, which is
[§4](#4-edges-across-packages)'s subject and what lets two payday apps be linked
into one process.

### The cost

Your app carries generated copies of payday's entities, and upgrading payday can
change them. That is why three things exist:

- `pd gen --check` in CI, so a copy that was not regenerated is caught.
- `version.Same`, refused at `NewSink`, so generated code and a linked payday
  cannot disagree at run time.
- `entschema.Check` before serving, so a field that arrived in `internal/ent`
  cannot be served against a database that does not have the column.

None of those is optional decoration. A field added upstream arrives quietly: it
compiles, and the tests pass against a database the tests just created. The
three are one failure caught at three moments — in CI, when the stack is
assembled, and before the first request — because on its own it has no symptom
until the one handler that reads the new column runs.

Both run-time checks are deliberately narrow, so that neither is a guard people
learn to work around. `version.Same` says nothing when either side cannot name
itself — a checkout, a `replace`, a workspace — since refusing those would
refuse every `go test` in every app developed against payday's source.
`entschema.Check` asks whether the database is **not missing** anything rather
than whether it matches: columns and indexes it does not know about are left
alone, because a deployment's database is allowed to have more than the schema.

## 2. Overlays: adding, never overriding

An overlay is a fragment declaring what your app adds to one of payday's
entities:

```proto
// proto/ext/payday/holder.ext.proto
message Holder {
  string department = 20;
}
```

`protobuf-merge` unions it with payday's, and that union is the first step of a
generation. Overlays are **excluded from buf**, because they are not files that
compile — they name messages that exist only after the merge.

### What is refused

**Touching a payday-owned field number.** Adding is allowed; redeclaring is a
generation failure. The check is there because merging takes the overlay's word
and says nothing about it: without it `string alias = 4` becomes
`int64 alias = 4` and the app still compiles, while the wall goes on reading a
tenant and `auth` goes on looking a holder up — both against a column that is no
longer what they were written for. What is compared is what payday shipped
against what is about to be generated, by number: the name it was given, the
kind it holds, and for a message field what it points at. The numbers you may
use, and the refusal itself, are in
[the schema guide](guide/schema.md#7-adding-to-paydays-entities).

**An overlay that changes what the file is.** `features.field_presence =
IMPLICIT` copied out of an entity file lands on the *whole merged file* — every
field of every message in it, including the generated ones. It is refused on
both kinds of overlay, the ones that extend an entity and the ones that extend a
service contract, because it is the same union either way. Inside a `message`
such a line is about that message and is the app's to say; outside one it is
not. This was found by an overlay that took `HasId` off an Add request and
stopped the build, which is the lucky version; on a field nobody calls `Has` on,
it would silently change what "not set" means on the wire.

### What `pd doctor` catches instead

**An overlay for an entity payday does not have.** `pd gen` looks up one overlay
per entity it ships, by name, so a file called `hodler.ext.proto` is never
opened: the generation succeeds and the first sign is a missing column. `pd
doctor` reads the directory the other way round — every `*.ext.proto` in it,
against what payday ships — and reports the file together with the entities
payday does have, because the cause is nearly always a typo. It is a finding and
not a fatal one: nothing about the app is broken, and the fix is a rename.

### The number ranges

payday reads some numbers by name across every entity, so that code holding a
row it has no type for can still read the parts every row has. The table is in
[the schema guide](guide/schema.md#3-the-field-numbers).

**Field 3 is yours and payday will never spend it.** It is left for an edge to a
set smaller than a tenant — a site, a fleet, a project — and putting one there
gives you a second axis every read is narrowed by; see
[permissions §7](guide/permissions.md#7-a-second-axis-if-you-need-one).

**It is a number rather than an option, because the number is the scarce
thing.** The header is what anything can read off a row whose type it does not
have, and 3 is the one place a reader could ever be taught to ask "what set is
this row in" without knowing what the set is called. An option would let two
apps answer that question at two different numbers, and then nothing generic can
ask it at all — at which point the slot may as well have been 8. The shape is
fixed for the same reason: an edge, one of them, to one entity, the same entity
throughout the schema.

### An added field may be a message

An overlay can add a field whose type is a message — a `Profile` on the Holder,
say:

```proto
message Holder {
  Profile profile = 9;
}

message Profile {
  string display_name = 1;
  int32  how_many     = 2;
}
```

It needs no declaration beyond the field. It is stored as the canonical protobuf
JSON in one column: `jsonb` on PostgreSQL, `json` on MySQL, and on SQLite the
TEXT any string gets, holding the same bytes. See
[the schema guide](guide/schema.md#a-field-can-be-a-message) for what that costs:
it cannot be filtered or indexed, it is replaced whole, and renaming one of
*its* fields is a migration nothing checks.

**The names in the column are protojson's, not the schema's.** ent's `sqljson`
predicates are the way down to it when an app has to reach inside after all, and
the path they take is `displayName` and `howMany` — not the `display_name` and
`how_many` the schema is written in. That is the third of those costs said as a
fact rather than as a warning: a rename here breaks no build and no wire, it
stops finding the old value, and a SQL predicate is the only place that would
ever say so.

The shape is worth knowing because it recurs. A message-typed field used to
generate an `encoding/json` column, and every message payday generates uses the
protobuf opaque API — whose fields are all unexported. So `encoding/json` saw
nothing, every value round-tripped as `{}`, and the `Add` answered with what it
had been handed, so it looked stored right up until somebody read it back. What
let it last is that nothing in payday's own test app had such a field. One does,
and three tests hold it: the value comes back, a patch replaces the message
whole, and `sqljson` finds the row by the protojson spelling and finds nothing
by the schema's.

## 3. Whose names these are

Your app has `app.Tenant`, not `payday.Tenant`. `pd gen` rewrites the `package`
line of the copied entities to your app's proto package, read from the `package`
declaration your own entities carry — or, when your schema has more than one
proto package, from the file that says `option (payday.app) = {};`. `go_package`
is rewritten too, but that decides where the generated Go lands, not what the
message is called.

This is unconditional, not opt-in. Nobody wants a concept as central as *tenant*
or *holder* bound to a vendor's namespace. Using payday's shape should not mean
being unable to leave.

**`payday.` survives on the wire in exactly two places, and both are
transports.** `/payday.BatchService/Do` takes several writes as one transaction
and takes no position on what is in them; `/payday.TokenService/Introspect` says
who an opaque token stands for. Neither describes your domain, and neither
mentions an entity, so there is nothing per app in either of them to rename.
They keep their names on purpose, the way `grpc.health.v1.Health` does: a
**shared contract** is what a client written once finds in any payday app, and
renaming them per app would cost that and buy nothing.

The `(payday.entity)` option keeps its name too, and it never reaches a caller —
it is a build-time annotation, like the `import "orm.proto"` beside it.

### The `own:` marker

Because the package name is no longer fixed, payday cannot find its own entities
by full name. Each carries a marker instead:

```proto
option (payday.entity) = {own: OWN_TENANT};
```

It is written only in payday's shipped schema and copied along with everything
else. It is what the overlay check and the tenant's domain registration key on.

Finding them by name is what this replaced, and it is worth saying what that
cost, because the failure had no signal at all: three layers are built out of
these entities, and each looked for `payday.Tenant` and the rest by full name.
Rename the proto package and nothing failed — those layers were simply not
generated. The Gate layer is the whole of the `Add` tenant check, so **reads
stayed walled and writes stopped being**, and the first sign was a row planted
in somebody else's tenant.

### Two apps sharing a boundary

If a second app shares users and permission boundaries with the first, it will
want the first app's `Tenant` and `Holder`. Giving both the same proto package
is how a family of apps says so, and then `acme.Tenant` names one type across
the family.

Do that as a claim about the **schema**, not just about the name: a shared
package is
[a shared schema](guide/packages.md#a-shared-package-is-a-shared-schema-and-this-is-the-trap),
and one fully-qualified name has to mean one message. Here the shared names are
payday's own: add `employee_number` to Holder in one app of the family and
`acme.Holder` means two things depending on which server you asked. **Keeping
the family's overlays compatible is the app's responsibility**, not payday's.
There is no mechanism enforcing it and there should not be — that is a claim
about two deployments payday cannot see.

They also have to stay **separate processes**. One proto package is one set of
file paths, and a protobuf registry keys files by path, so linking two apps of
the family into one binary registers `acme/payday/holder.proto` twice and panics
before `main`. See [two apps in one
process](guide/packages.md#3-two-apps-in-one-process).

What is shared without any of this is the **identifier**. A tenant is domain 1
and a holder is domain 2 in every payday app, and a `pdid` is unique without
coordination, so a row one app minted is nameable by the other already, with no
agreement about packages at all. That is usually the thing actually wanted.

## 4. Edges across packages

payday's entities are copied *into* your package, so an edge to `Tenant` is an
ordinary edge inside one package. What crosses a boundary is an edge to a second
package of your own — `Robot.thing` naming a `shared.Thing` — and it requires
two things, both of which are enforced.

### `strategy: all`

buf must compile the whole workspace as one unit rather than file by file, or
the generator sees a type it cannot resolve. The `buf.gen.yaml` is written by
payday rather than by the app, precisely so this cannot be got wrong.

### One `go_package` per app

Every entity in an app has to generate into the same Go package. Two packages
and the ent schemas split, and the edges between them do not compile — the wall
itself is such an edge.

`pd gen` refuses an app with two, naming both files.

**Two *proto* packages are allowed**, and are a different question: one of them
has to say `option (payday.app) = {};`, because that is what decides which
package payday's own entities are copied into. Neither saying it is refused, and
so is both. See [more than one proto package](guide/packages.md), which also
covers two apps linked into one process — the case where sharing a package
fails loudly rather than quietly, in the way §3 above describes.

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

54 random bits, and ordering guaranteed up to 4096 rows per millisecond. The
version is written into the high nibble of byte 6 rather than as a whole byte,
which is what leaves `seq` its full twelve bits: taken four at a time by the
version it would order 256 rows per millisecond instead.

The domain sits at byte 9, the second half of the fourth group as a UUID is
written out — so it reads without counting.

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

payday keeps the low numbers for what it ships — Tenant 1, Holder 2, Audit 3,
Outbox 4 — and an app's own start at 7; the rules for choosing one are in
[the schema guide](guide/schema.md#2-what-you-have-to-say-and-what-you-may-leave-out).

Both the write path and the read path check the byte, and they check it in
different places on purpose. Writing is `Minter`: the generated `Add` asks it
for the key a row is stored under, and a request that named its own identifier
is refused there if the domain is another entity's — before a statement exists,
with a message saying what it actually was. Without that, a caller could store a
Robot under an identifier whose domain says Holder, and every later reading of
that domain would be reading the caller's word rather than the schema's.

Reading is checked where a written reference is **parsed**, by `slug.Slug.Expect`
and `pdcmd.Ref.Expect`. It is not the server's job, because the server is not
wrong: it narrows to rows of its own entity, finds none, and says `NotFound`
correctly. Checking the claim at the edge is what turns that silence into a
refusal that names the mistake.

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

`<E>RefBySlug` is generated where the name needs both halves — an entity whose
`alias` is unique *within* a tenant, declared as a unique index over `alias` and
`tenant`. Holder and Robot are like that, and their `RefBySlug` carries an alias
and a `TenantRef`. An entity whose alias is unique across the whole deployment
has no second half to carry, so its ref takes the alias bare on the oneof: that
is Tenant, and it is why there is no `TenantRefBySlug`. An entity with an
`alias` and no uniqueness at all gets neither, since an alias nothing keeps
unique cannot name a row.

The domain can be left off when the surrounding context implies it:
`RobotService.Get` expects a Robot, so `Slug.Expect` fills it in and
`@acme/arm-01` resolves. Written out, it is checked rather than obeyed — a slug
saying `#holder` where a Robot belongs is refused, because obeying it would name
a different kind of row and throw away the disagreement that could have caught
the mistake.

## See also

- [guide/schema.md](guide/schema.md) — how to declare an entity
- [tenancy.md](tenancy.md) — what the tenancy declaration means
- [runtime.md](runtime.md) — what is generated from all of this
