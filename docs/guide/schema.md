# Declaring an entity

An entity is a message with two options on it. Everything else payday builds —
the service, the wall, the identifier, the list, the watch, the trail, the
TypeScript — comes out of that declaration and is regenerated whenever it
changes.

This document is what those options say. For who may *see* what comes out, read
[Permissions and the wall](permissions.md).

---

## 1. One entity, whole

```proto
edition = "2023";

package app;

import "payday/tenant.proto";
import "google/protobuf/timestamp.proto";
import "orm.proto";
import "payday.proto";

option features.field_presence = IMPLICIT;
option go_package = "github.com/acme/widget";

message Robot {
  bytes  id     = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string alias  = 4;
  string name   = 5;
  string desc   = 6;

  google.protobuf.Timestamp date_updated = 13 [(orm.field) = {version: {}}];
  google.protobuf.Timestamp date_erased  = 14 [(orm.field) = {erased: {}}];
  google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];

  option (orm.message) = {
    rpc: {crud: true}
    indexes: [
      {name: "page", refs: [{name: "date_created", number: 15}, {name: "id", number: 1}]},
      {name: "slug", refs: [{name: "alias", number: 4}, {name: "tenant", number: 2}], unique: true}
    ]
  };
  option (payday.entity) = {
    domain: 7
    list: {
      order: [{field: "date_created"}, {field: "id"}]
      by:    ["ref"]
      size:  20
      max:   100
    }
    watch: {}
  };
}
```

Two options, and they answer different questions.

**`(orm.message)`** is the *storage* contract, and it is not payday's — it comes
from [protobuf-orm](https://github.com/protobuf-orm/protobuf-orm). It says which
RPCs exist and what the table is indexed by.

**`(payday.entity)`** is payday's, and it says the things that **fail quietly**
when left to be worked out later: what an identifier names, whether a row is
behind the wall, and what `Erase` does to it.

`Tenant` is payday's entity, in your package — see
[permissions §10](permissions.md#10-whose-names-these-are).

---

## 2. What you have to say, and what you may leave out

### `domain:` — required

```proto
domain: 7
```

One byte written into every identifier of this entity, saying what kind of thing
it names. Generation refuses an entity without one:

```
no (payday.entity) option: every entity has to say what its identifiers
name and whether its rows are behind the tenant wall

    option (payday.entity) = {domain: 7, tenanted: {via: "tenant"}};
```

Treat the number the way you treat a field number: **chosen once, and never
given to something else.** An identifier outlives the row it named — that is
most of what carrying the domain buys — so reusing a number makes an old
identifier lie about what it used to be.

- `1`–`6` are payday's. Yours start at `7`.
- `0` is refused: it is what an identifier nothing registered reads as.
- Two entities sharing one is refused.

`pd entity add` picks the next free number for you, and does not hand back the
number of an entity you deleted while anything above it survives.

### Erasure — required, one way or the other

Every entity has to say what `Erase` does to a row:

| carries `erased:` field | says `erase: {hard: {}}` | |
| --- | --- | --- |
| yes | no | **soft** — the row is stamped and stays |
| no | yes | **hard** — the row goes |
| no | no | **refused** |
| yes | yes | **refused** — the ORM runs soft and the schema says destroy |

It is a refusal rather than a default because payday cannot add a field to your
schema: the only way to say "soft" is to carry one, so silence cannot mean it.

Soft is what you want almost always. The row is stamped and stays, so it cannot
be read or changed, its alias comes free for a new row (the unique index becomes
partial, `WHERE date_erased IS NULL`), and the trail can still say what it held.

Hard is a real answer for a table of things that arrive faster than anyone reads
them. What it costs is that `Audit.value` has nothing to read after the write,
so the trail cannot say what was lost.

> **Do not answer this by going to the database instead.** A `DELETE` run
> outside the app skips the trail, the version and the `Watch` — and a watch
> says a row is gone by *not sending it*, so a row deleted behind the app's back
> is one every client holding it holds forever.

### Tenancy — leave it out

Saying nothing means `tenanted: {via: "tenant"}`: behind the wall, by the edge
of that name. Write something only for the two cases that are not that —
`tenant: {}` for the entity that **is** the tenant, `global: {}` for rows that
are not behind it.

The whole of why is in [permissions §1](permissions.md#1-what-you-get-by-saying-nothing).

---

## 3. The field numbers

payday reads some numbers by name across every entity, so that something holding
a row it has no type for can still read the parts every row has.

| | |
| --- | --- |
| **1** | the key |
| **2** | the tenant |
| **3** | **yours** — a set smaller than a tenant, if this app has one |
| **4** | `alias` — the name a person writes |
| **5** | `name` |
| **6** | `desc` |
| **7** | `labels`, `map<string, string>` |
| **8–12** | yours |
| **13** | `date_updated`, the version |
| **14** | `date_erased` |
| **15** | `date_created` |
| **16+** | yours |

An entity that does not want one of 4–7 **leaves the number empty** rather than
spending it on something else. That is what keeps `header.Of` able to read any
row of any entity.

Nothing is required. An entity nobody names simply has no `alias`, and then it
gets no naming, no folding and no slug — there is no switch to turn off.

**3 is left open for you and payday will never spend it.** Put an edge there and
you get a second axis every read is narrowed by; see
[permissions §7](permissions.md#7-a-second-axis-if-you-need-one).

---

## 4. `alias` — the name a person writes

```proto
string alias = 4;
```

Declaring it turns on rather a lot. Every write of it is folded and checked on
the way in, so `"  Arm-01 "` and `"arm-01"` are one row and `"Not A Name"` is
refused with the field it was in. The row becomes reachable by
**slug** — `@acme/arm-01` — wherever a reference is taken, and the generated
`RobotRefBySlug` is what carries it.

A slug may say what kind of thing it names, and the domain is checked rather
than believed:

```
@acme/arm-01          the domain comes from context
@acme/arm-01#robot    the same, said out loud
@acme/arm-01#joint    refused: InvalidArgument
```

Uniqueness is yours to declare, and it decides the shape of the reference. A
unique index on `(alias, tenant)` makes the slug tenant-scoped, which is the
ordinary case. `unique: true` on the field itself makes it global, which is what
`Tenant` does — and then the reference is a bare string with no tenant beside
it.

---

## 5. `list:` — the page

```proto
list: {
  order: [{field: "date_created"}, {field: "id"}]
  by:    ["ref", "listed"]
  size:  20
  max:   100
}
```

Saying nothing is no `List` RPC at all.

**`order` has to end in the key.** A cursor cannot tell apart two rows equal in
every column of the order, so the page after the first of them repeats the
second or skips it — and rows written by one request are stamped a moment apart
at best. Generation refuses an order that does not:

```
list: order ends in "date_created" and has to end in the key, "id".
a cursor cannot tell apart two rows equal in every column of the order, so
the page after the first of them repeats the second or skips it
```

**`max` is required.** A page with no cap is an answer with no cap, and the
request that finds that out is the one that reads the whole table. `size` is
what a request that asks for nothing gets, and it may not exceed `max`.

**`by` is what a caller may filter on.** `"ref"` means naming a row outright;
anything else is a field, compared for equality. A field that equality is not a
sensible question about is refused rather than generated.

**Declare an index that covers the order.** Generation *warns* rather than
refuses — it is not wrong, only slow — but the read it names would scan the one
table that never stops growing and sort what it found.

`with:` names edges to load alongside, so a page can render without a second
round trip per row.

---

## 6. `watch:` — the page, kept current

```proto
watch: {}
```

A watch is the list it declared, run again for every write that touches the
entity, for as long as a stream is open. So it needs three things, and each is a
refusal rather than a surprise later:

| | why |
| --- | --- |
| a `list:` | there is nothing to order by, filter on, or cap |
| `"ref"` among `by:` | otherwise a watch has no way to name the rows it is about |
| a version field | see below |

The version is the one worth reading twice:

```
watch: needs a version field, and this entity has none.

    google.protobuf.Timestamp date_updated = 13 [(orm.field) = {version: {}}];

A watch sends state, so a client replaces what it holds -- and two answers
about one row can arrive out of order. Without something to compare, a
stale one overwrites a fresh one and nothing anywhere fails
```

A version field is stamped on every write and refused to a patch document. That
is what makes it a version rather than a field somebody can set.

**A watch says a row is gone by not sending it.** There are no tombstones — a
row that left the filter and a row that was erased look the same, which is
right, because to a client watching a filter they *are* the same.

---

## 7. Adding to payday's entities

payday's four are copied into your schema on every generation, so editing them
lasts until the next `pd gen`. What lasts is an **overlay**:

```proto
// proto/ext/payday/holder.ext.proto
message Holder {
  string email  = 8;
  bytes  badge  = 16;
}
```

`pd gen` merges it before anything else runs. You may add at any number payday
does not use; changing one it does is refused:

```
an overlay may add to one of payday's entities and may not change it.
payday keeps 1..7 and 13..15; an app's own go in 8..12 and from 16.

  app.Holder: 4 is payday's "alias" (string) and was redeclared as "handle" (string)
```

The check exists because merging takes the overlay's word: without it,
`string alias = 4` becomes `int64 alias = 4` and the app still compiles — while
the wall goes on reading a tenant and `auth` goes on looking a holder up, both
against a column that is no longer what they were written for.

Overlays for your own generated services go in `proto/ext/app/`, and are how a
hand-written RPC joins a generated one.

---

## 8. What generation refuses

Everything here fails quietly if it is left to be noticed later, which is the
only test for whether it belongs on this list.

| | |
| --- | --- |
| no `(payday.entity)` | identifiers that say nothing about what they name |
| `domain: 0`, or `> 255` | zero is what an unregistered identifier reads as |
| two entities, one domain | an old identifier starts lying about what it was |
| erasure unsaid, or said twice | a row destroyed where one was meant to stay |
| `tenanted:` with no tenant in the schema | a wall made of nothing |
| a `via:` that does not reach the tenant | a predicate narrowing to the wrong rows |
| `via:` and `field:` together | an edge says the tenant is still there; a column says only what its identifier was |
| `list:` with no `max` | an answer with no cap |
| `list:` order not ending in the key | a cursor that repeats or skips a row |
| `watch:` with no `list:` | nothing to order, filter or cap |
| `watch:` with no `ref` in `by:` | no way to name the rows it is about |
| `watch:` with no version field | a late answer overwriting a fresh one |
| an overlay changing payday's field | a column that is no longer what its readers expect |
| two `go_package`, or two proto packages | two Go packages is two ent schemas, and the wall is an edge |
| generated code from a different payday | a wall written by one version, read by another |
| the database not matching the schema | a server up on a table that is not there |

And one **warning**: a `list:` whose order no index covers.

---

## 9. `pd entity add`

```sh
$ pd entity add Robot --tenanted --watch
```

It is not for saving typing. It is for the two things that go wrong on the
fourth entity: a domain that collides with one you already used, and tenancy
written out when it should have been left silent.

```
--tenanted   behind the wall (writes no tenancy line — that is the declaration)
--tenant     it is the tenant
--global     not behind the wall, and said on purpose
--watch      a version field and `ref` among the filters, so the watch generates
```

What it writes passes every refusal above, which is asserted by a test — the
first thing anyone does with a scaffold is run `pd gen`, and reading a generated
error there is a bad first minute.

`pd entity list` prints what is declared and which domain each holds.

---

## 10. After a change

```sh
$ pd gen .                 # everything the schema says
$ pd gen --check .         # regenerate and fail if anything moved — this is CI
```

Two things do not follow automatically and will refuse to be forgotten:

**A migration.** Generation rewrites the ent schema; it does not touch your
database. A server whose database does not match refuses to start rather than
serving on a table that is not there.

**A regeneration after upgrading payday.** Generated code carries the version it
came from, and a binary linking a different payday is refused — an app that
upgraded and did not regenerate otherwise compiles, links and serves, on a wall
the older payday wrote.

---

## Where the reasoning lives

| | |
| --- | --- |
| [permissions.md](permissions.md) | who may see what comes out of this |
| [client.md](client.md) | what a page does with what this generates |
| [SCHEMA.md](../SCHEMA.md) | why payday owns these entities, and what it costs |
| [RUNTIME.md](../RUNTIME.md) | what `List` and `Watch` generate, and what is refused |
