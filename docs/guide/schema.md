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

// payday's entities are copied **into** this package on every generation, so
// the tenant is under it and never at `payday/tenant.proto`.
import "app/payday/tenant.proto";
import "google/protobuf/timestamp.proto";
import "orm.proto";
import "payday.proto";

option features.field_presence = IMPLICIT;
option go_package = "github.com/acme/app";

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
      {name: "page", refs: [{name: "tenant", number: 2}, {name: "date_created", number: 15}, {name: "id", number: 1}]},
      {name: "slug", refs: [{name: "alias", number: 4}, {name: "tenant", number: 2}], unique: true}
    ]
  };
  option (payday.entity) = {
    domain: 7
    list: {
      order: [{field: {name: "date_created"}}, {field: {name: "id"}}]
      by:    [{name: "ref"}]
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

One of those RPCs is worth knowing about before a schema has many rows to
write: `rpc: {crud: true, add: {many: {}}}` adds an `AddMany` beside the `Add`,
which takes up to a thousand `Add` requests (`many: {max: N}` for another
bound) and writes them in one transaction, a statement for every few hundred
rows. Each item is the row an `Add` of it would be, and payday asks of each
what it asks of an `Add` -- the gate, the names and the tenancy the Sink
gives a row, the Secret layer on what it answers with; see
[permissions](permissions.md#what-the-gate-reads-and-what-it-does-not). None
of payday's own entities has one.

**`(payday.entity)`** is payday's, and it says the things that **fail quietly**
when left to be worked out later: what an identifier names, whether a row is
behind the wall, and what `Erase` does to it.

`Tenant` is payday's entity, in your package — which is why the import carries
your package's name. See
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
app.Robot: no (payday.entity) option: every entity has to say what its identifiers name and whether its rows are behind the tenant wall

    option (payday.entity) = {domain: 153, tenanted: {via: "tenant"}};
```

The number in the suggestion is derived from the entity's own name — `153` is
what `app.Robot` hashes to — so it is a number nothing is likely to be using
rather than the next free one. Two schemas that take the hint at once are caught
by the duplicate check below, which is the point: a suggestion that pretended to
allocate would be one people trusted.

Treat the number the way you treat a field number: **chosen once, and never
given to something else.** An identifier outlives the row it named — that is
most of what carrying the domain buys — so reusing a number makes an old
identifier lie about what it used to be.

- `1`–`6` and `248`–`255` are payday's. Yours are `7`–`247`.
- `0` is refused: it is what an identifier nothing registered reads as.
- Two entities sharing one is refused.
- payday's past, when you declare one, takes two of yours: the numbers you give
  it ([§7](#your-past-paydays-event-and-fact)).

`pd entity add` writes one past the highest number the schema holds, rather than
the lowest that is free, so it does not hand back the number of an entity you
deleted while anything above it survives.

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
And a guess would not be one risk but two unlike ones. Assume soft where hard
was meant and rows somebody wanted gone are still sitting there, which is
noticed by looking at them; assume hard where soft was meant and they are gone,
which is noticed by somebody asking for one back. So an entity that says neither
is refused, with both ways out written into the refusal:

```
app.Robot: erase: nothing says what `Erase` does to a row, and with no field marked `erased:` it destroys one

    google.protobuf.Timestamp date_erased = 14 [(orm.field) = {erased: {}}];

or, if losing the row is what this entity is for:

    option (payday.entity) = {... erase: {hard: {}}};
```

The number it offers is 14, which is where payday's own entities put it; an
entity that has already spent 14 is offered the first free number from 16 up,
never 15, because 13 to 15 are payday's on every entity it ships.

`erase:` is an entity option and goes beside `domain:`, not in `(orm.message)`
with `rpc:` and `indexes:`:

```proto
  option (payday.entity) = {
    domain: 8
    erase: {hard: {}}
  };
```

Putting it in the other block is refused by protobuf itself rather than by
payday. What comes back — from `buf`, under the file and line of the option you
wrote — names the block it looked in and the field it did not find there, and
nothing at all about the block where that field does live:

```
message app.Robot: option (orm.message): field erase not found
```

It reads like "there is no such option". It means "not on this one".

Soft is what you want almost always. The row is stamped and stays, so it cannot
be read or changed, its alias comes free for a new row (the unique index becomes
partial, `WHERE date_erased IS NULL`), and there is still a row for the trail to
record.

Hard is a real answer for a table of things that arrive faster than anyone reads
them. What it costs is that there is nothing left to read after the write —
which the trail pays for too, and what it pays is
[in the permissions guide](permissions.md#9-erasure).

> **Do not answer this by going to the database instead.** A `DELETE` run
> outside the app skips the trail, the version and the `Watch`: nothing is
> published, so no stream ever says the row went, and every client holding it
> holds it forever. A declared hard erase is safer than the database.

### Tenancy — leave it out

Saying nothing means `tenanted: {via: "tenant"}`: behind the wall, by the edge
of that name. Write something only for the two cases that are not that —
`tenant: {}` for the entity that **is** the tenant, `global: {}` for rows that
are not behind it.

Silence means the walled answer because the two ways of being wrong are not
alike: a wall assumed where there is none empties a screen and somebody says so,
and a wall missing where there should be one breaks nothing and is first noticed
by a caller reading another tenant's rows. The whole of that argument is
[tenancy.md §2](../tenancy.md#2-the-default-is-the-loud-one); what an entity
that declares nothing gets for it is
[permissions §1](permissions.md#1-what-you-get-by-saying-nothing).

#### `stamp:` — when the path is long and the table is hot

A `via` of more than one step is a subquery in the wall, on **every read**:

```go
joint.HasRobotWith(robot.TenantIDIn(vs...))   // via "robot.tenant"
robot.TenantIDIn(vs...)                       // a direct edge
```

`stamp:` names a `bytes` column on the row, and payday writes what the path
reaches into it. The wall then reads the column, so an entity two hops from its
tenant gets the predicate a direct edge gets — and `list: {by: [{name: ...}]}`
becomes possible, which a path of two has nothing to name.

```proto
bytes tenant_id = 9 [(orm.field) = {type: TYPE_UUID}];

option (orm.message) = {
  rpc: {crud: true}
  indexes: [{name: "wall", refs: [{name: "tenant_id"}, {name: "date_created"}]}]
};
option (payday.entity) = {
  domain: 11
  tenanted: {via: "robot.tenant", stamp: "tenant_id"}
};
```

**The index is not optional and generation refuses without one.** What a stamp
replaces is `HasRobotWith(robot.TenantIDIn(...))` — a semi-join over two indexed
columns — and `tenant_id IN (...)` on an unindexed column is not faster than
that, it is a scan of the table the wall is read from most. The stamp has to be
the index's **first** column: a composite index answers a predicate on its first
column and not on its third.

**A stamp over a one-hop `via` is refused too.** One hop is already a column —
the wall reads the foreign key rather than walking it — so the stamp would be a
second copy of the same identifier with nothing to keep it in step with.

**It is not a field of the request.** A caller that could write it could put a
row behind a wall its edge does not agree with, and the row would then be
readable by a tenant that does not hold it. Whatever a caller puts there is
overwritten, on every path in — the stamp is on the Sink, which is what both
stacks are built on, so the server a deployment does its own work through cannot
write one either.

**It is not a cache.** Generation refuses a stamped `via` whose steps are not
all immutable and not nullable, so the value is decided when the row is written
and nothing can move it. That is why nothing refreshes it: there is no way for
the question to arise.

#### `agrees:` — when a second edge also reaches a tenant

A row can hold two edges that each arrive at a tenant. payday says nothing about
the second, because it cannot know which disagreements are mistakes: its own
trail holds two tenants on purpose.

`agrees:` is how an app says it wants one path checked against `via`:

```proto
tenanted: {via: "lead.tenant", agrees: ["follow.tenant"]}
```

The comparison happens on the Sink, before the write, and each path is read from
**below** the wall — a row in another tenant has to arrive as a disagreement and
not as "there is no such row".

Which is also why it is worth declaring. The generated gate reads every edge of
an `Add` through the wall, and every edge a `Patch` moves — see
[permissions §3](permissions.md#3-where-it-is-enforced-and-why-in-two-places) —
so an ordinary caller cannot point either path at a row it cannot see. What the
gate does not do is compare the two, and the caller that leaves uncaught is the
one who can see **both** tenants: an operator whose scope covers several holds
one edge in each, so "may I see what this edge names" is answered yes twice and
the row is written with a foot in two walls. A declared disagreement names the
edge and the path it did not agree with:

```
follow: it is in another tenant than lead.tenant
```

Being on the Sink rather than in a layer is what makes that hold where no layer
runs at all: the deployment doing its own work through the server with no wall
and no gate is refused the same row.

Paths here are immutable too, for the same reason, so the comparison happens
once and stays true.

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

An entity that does not want one of 4–7 or 13–15 **leaves the number empty**
rather than spending it on something else. `pd gen` and `pd doctor` refuse
fields in these slots unless they have the listed name and protobuf shape,
including singular strings, string-to-string labels and singular timestamps.
Declaring `alias`, `name` or `desc` somewhere else is also refused. Holding the
numbers is what lets payday add a header field later without every app having
already spent it. Payday-owned entities use their shipped schema, which is
checked separately.

What `header.Of` reads a row by is the **name**, not the number — which is why a
`name` that is not a string is refused as well. A reflective read of one finds
nothing, a page falls back to the alias or to the identifier, and nothing
anywhere says why.

Nothing is required. An entity nobody names simply has no `alias`, and then it
gets no naming, no folding and no slug — there is no switch to turn off.

**3 is left open for you and payday will never spend it.** Put an edge there and
you get a second axis every read is narrowed by; see
[permissions §7](permissions.md#7-a-second-axis-if-you-need-one).

### A field can be a message

Not everything belongs in its own table. A profile, an address, a set of
preferences — a value that has no key, no lifecycle of its own, and is written
and read with whoever holds it:

```proto
message Holder {
  Profile profile = 9;
}

message Profile {
  string display_name = 1;
  int32  how_many     = 2;
}
```

That works, and it is stored as the canonical protobuf JSON — a `jsonb` column
on PostgreSQL, `json` on MySQL, and on SQLite a text column holding the same
bytes. There is nothing to declare beyond the field.

Three things follow from it, and the first is the one that decides whether you
want this at all.

**It cannot be filtered or indexed.** It is one value to the database. There is
no `HolderByDisplayName`, no filter for it on `List`, and no index that would
make one fast. So a claim that has to be **looked up** does not go in here — it
goes flat beside it, where it can be unique:

```proto
string  idp_subject = 8 [(orm.field) = {unique: true, nullable: true}];
Profile profile     = 9;
```

The rule: searched for, flat; shown, in the message.

**It is replaced whole.** A patch that sets it assigns the whole message, so a
field the new one does not carry is gone rather than kept. There is nothing for
a patch to merge *into*: the column holds a document, not a set of columns. That
is usually what you want from something like a profile — it is what one source
said at one moment — but it is worth being sure.

**Renaming a field of it is a migration nothing performs.** protojson writes
field *names*, so `display_name` → `full_name` compiles, leaves the wire
unchanged, and stops finding the old value. `buf breaking` does say so — the
`FILE` category payday and the `pd new` template both configure includes
`FIELD_SAME_NAME` — but saying so is all anything does. No tool rewrites the
rows already stored under the old spelling, and no migration is generated for a
column whose type did not change.

If you do need to reach inside from SQL, ent's `sqljson` predicates work on the
column — and the path is protojson's spelling, so `Path("displayName")` and
`Path("howMany")` find the row while `Path("how_many")` finds nothing.

### A field about a person

A name, an address, a phone number — a value that is somebody's, on a row that
may or may not be about them — is declared personal:

```proto
string email = 8 [(payday.field) = {personal: true}];
```

It is answered with like any other field: a screen shows it, a mail goes to it,
and whoever may read the row reads it. What it changes is the **trail**, which
records that the field was set and never what to, at every content level, in
the patch and in the row (#92). The row is where a person is corrected and
erased, and the trail is the copy nothing rewrites, so their values have one
home.

It is also what the trail's keyed digest leaves out, for the same reason, and
what the row's **seal** covers instead (payday#115): a MAC of the row, kept
beside it in the database alone for as long as the row is, and blanked when the
person is forgotten. So a personal field changed past the servers is found by a
check of the rows, as any other field is. Declaring a field personal does not
take it out of what a check sees; it decides what the trail keeps, what the
access record reads, and that the kind is sealed.

What it is for is a value that identifies or describes somebody — what they are
called, how they are reached, what another system calls them, a picture, what
is written about them. Not what they may do or where they stand: a role, a
suspension, a date they leave are what a trail is kept to say **with** their
values, since who made somebody an administrator is the record.

Declare every field that identifies or describes a person, including one that
only sometimes does: an entity that holds people and teams in one table declares the shared
`name`, and a team renamed is on the trail as `name` having been set. There is
no declaration that depends on the row. payday's own `Holder` declares its
`alias`, `name`, `desc` and `labels`.

It is not `secret`. A secret is never answered with, to anybody; a personal
field is read like any other. Generation refuses the two on one field, and
refuses `personal` on an edge or on the key — those are identifiers, which is
what the trail is made of.

It goes on the entity's own column. A message one column holds — a profile, a
contact — is withheld or kept as the column it is in, so its own fields cannot
say anything, and generation refuses a `(payday.field)` there rather than
reading nothing off it: declare the column that holds the message.

#### Saying a field is about no one

`personal: false` says a field was looked at and is about no one in
particular:

```proto
string role = 9 [(payday.field) = {personal: false}];
```

The trail keeps it as it keeps any field nobody declared. What the declaration
changes is that `pd doctor` stops asking, and it asks about a field in two
places:

- **Free text on an entity that holds people.** An entity with a personal field
  holds people, so each of its strings, bytes and documents — and lists and maps
  of them — says which it is. The one nobody declared is the one that copies a
  person onto the trail.
- **A name about somebody, on any entity:** `email`, `phone`, `address`,
  `birthday`, `first_name` and the like, as whole words of the name. An entity
  that holds people and declares nothing at all is not reached by the first, and
  this is what reaches it.

It is advice and not a refusal. Generation does what it always did, and which a
field is stays the schema's to say.

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
  order: [{field: {name: "date_created"}}, {field: {name: "id"}}]
  by:    [{name: "ref"}, {name: "listed"}]
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
app.Robot: list: order ends in "date_created" and has to end in the key, "id".
a cursor cannot tell apart two rows equal in every column of the order, so the page after the first of them repeats the second or skips it -- and rows written by one request are stamped a moment apart at best
```

**`max` is required.** A page with no cap is an answer with no cap, and the
request that finds that out is the one that reads the whole table. `size` is
what a request that asks for nothing gets, and it may not exceed `max`.

**`by` is what a caller may filter on.** `"ref"` means naming a row outright;
anything else is a field, compared for equality. A field that equality is not a
sensible question about is refused rather than generated.

**A `map<string, string>` is filtered by pairs.** Labels, annotations, anything
Kubernetes-shaped: the filter carries a map, and every pair in it has to be
present and equal — `{team: core, tier: gold}` is rows carrying both.

```proto
by: [{name: "ref"}, {name: "tenant"}, {name: "labels"}]
```

Two things it is worth knowing the answer to before asking:

- **A missing key is not an empty value.** `{team: ""}` is rows that say the
  team is empty, and not rows that have never heard of `team`. Reading a map by
  index gives the same answer for both, which is a filter that widens instead of
  narrowing; the generated predicate asks whether the key is there.
- **An empty map constrains nothing**, the way an absent scalar does. A filter
  that names nothing else is then a filter that names nothing, which was already
  refused.

No selector grammar — `k in (...)`, `!k`. That is a language, and a language in
a generator is where an `Rpc` somebody wrote is the better answer.

**Declare an index that covers the order.** Generation *warns* rather than
refuses — it is not wrong, only slow — but the read it names would scan the one
table that never stops growing and sort what it found. Covered means some index
**begins** with the order: one on `(a, b, c)` serves an order of `(a)` and
`(a, b)`, and neither `(b)` nor `(b, a)`.

**Behind the wall, the index begins with the tenant.** Every read of a tenanted
entity is narrowed to the tenants its caller may see, so the index a page walks
is `(tenant, <the order>)` -- the edge `tenant`, or the column a `stamp:` or a
single `field:` names. One that begins with the order instead serves the tenant
that owns most of the rows and makes every other one read past them: a
workspace of two hundred rows in a deployment of a million read its newest page
in 82 ms that way and in 0.09 ms with the tenant first (lesomnus/rove#122). The
warning asks for it so. A trail-shaped entity narrowed by the OR of several
columns is not asked about, since no one index serves that read.

An index's columns are in the order its refs are, an edge's among the fields':
`refs: [tenant, date_created, id]` is `(tenant_id, date_created, id)`.
protoc-gen-orm-ent once wrote every edge after every field, which turned
`(tenant, date_created)` into `(date_created, tenant_id)`; an index generated
before it kept its order comes out different, and the migration moves it.

`with:` names edges to load alongside, so a page can render without a second
round trip per row.

**A list reads the other way round, and in the orders it names.** Every List
request has `reverse`, which turns every column of the order round at once --
oldest first where it is newest first -- and needs declaring nowhere: an index
walks from either end, so the one the order walks serves both ways. `orders`
declares other orders a request may name, each by a name and each ending in the
key, as `order` does:

```proto
list: {
  order:  [{field: {name: "date_created"}, desc: true}, {field: {name: "id"}, desc: true}]
  orders: [
    {name: "occurred", order: [{field: {name: "occurred_at"}}, {field: {name: "id"}}]},
    {name: "alias_desc", order: [{field: {name: "alias"}, desc: true}, {field: {name: "id"}}]}
  ]
  max: 100
}
```

The request then has `order`, an enum of them -- `STOCK_MOVEMENT_LIST_ORDER_OCCURRED`,
and `…_UNSPECIFIED` for `order` itself -- and any of them can be read `reverse`.
A request names an order and never makes one up: an order is a walk of an
index, and which indexes there are is the schema's to know. **Declare the index
each one walks**: some index has to begin with its columns, in its directions or
in all of them the other way, or reading a page scans the table and sorts it. An
order of mixed directions, `alias` backwards and `id` forwards, wants an index
of those directions; an ordinary `(alias, id)` serves `(alias, id)` both ways
round and not that. The warning above asks about each named order as it does
about `order`.

**An order is over columns a cursor can carry.** A cursor is the last row's
value of every column of the order, handed to the caller and back, and the
next page is the rows past it. So generation refuses, in `order` and in every
named order:

- **a column that may be null** -- a null compares as neither greater nor less,
  so a row whose column is empty is skipped by the page after it, or never
  reached, and the databases do not agree on where nulls sort either. A default
  does not help: a nullable column with one is still one a write may clear.
  Order by a column that is `default`ed rather than `nullable`, or keep a
  nullable one out of the order and read the rows it would sort some other way;
- **a `secret:` field** -- it is never answered with, and a cursor is an
  answer: the last row's value would go out in every page's `next`;
- **a list, a map, a JSON column or bytes**, which have no order a cursor can
  name.

What is left -- a number, a string, a bool, an enum, a time, an identifier -- a
cursor reads back as the type the column is, so it names the row exactly; read
as a JSON number, an int64 past 2^53 came back as a neighbour and the next page
answered the same row again. A `personal:` field may be an order: the cursor
carries a value the page it came with already answered with, to the same caller.

A cursor is of the order it was read in and the way: carried on in another, or
the other way round, it is refused, since it names a place in a sequence it is
not in. The cursor of `order` read forwards is the same shape it always was, so
one a client is holding carries on across the upgrade.

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
app.Robot: watch: needs a version field, and this entity has none.

    google.protobuf.Timestamp date_updated = 13 [(orm.field) = {version: {}}];

A watch sends state, so a client replaces what it holds -- and two answers about one row can arrive out of order. Without something to compare, a stale one overwrites a fresh one and nothing anywhere fails
```

A version field is stamped on every write and refused to a patch document. That
is what makes it a version rather than a field somebody can set.

**A caller has to say which rows, and names them to say so.** A watch is a set
of rows, resolved from the filters when the stream opens and compared identifier
against identifier from then on — so **every** filter has to carry a `ref`. Two
refusals at the call, both `InvalidArgument`. Filters that are empty:

```
filters: a watch says which rows it is about; one that says nothing is the whole table, for as long as it is open
```

and a filter that says something other than which row — an equality on a field,
which the same entity's `List` would have taken:

```
filters[0]: a watch says which rows it is about by naming them
```

The first is the one shape with no cap at all. The second is the `"ref"` rule of
the table above, seen from the calling side: a list may be filtered by other
things, and a watch may not be filtered by nothing. What a watch subscribes to
is rows rather than a predicate — an equality on a field names a set that moves,
and a row that came to match it after the stream opened would be one nothing had
ever told the stream about. Resolving the references at the open is also what
makes a name that names nothing an answer rather than a stream quietly watching
none, and what keeps a row renamed mid-stream the row that was asked for.

**A watch says a row is gone by naming it with no value.** The item carries the
identifier, an absent value, and the RPC that did it. There are no tombstones,
and there is deliberately no way to tell "erased" apart from "no longer yours":
a stream that distinguished them would be telling a caller about rows that
stopped being theirs, which is the thing the wall is for. It is only ever said
about a row this stream has already carried — one that never matched is not
news.

---

## 7. Adding to payday's entities

payday's entities are copied into your schema on every generation, so editing
them lasts until the next `pd gen`. What lasts is an **overlay**:

```proto
// proto/ext/payday/holder.ext.proto
message Holder {
  string idp_subject = 8 [(orm.field) = {nullable: true}];
  bytes  badge       = 16;

  // Unique within a tenant, which `unique: true` on the field cannot say: one
  // person in two tenants is two holders with the same subject.
  option (orm.message) = {
    indexes: [
      {name: "idp", refs: [{name: "idp_subject", number: 8}, {name: "tenant", number: 2}], unique: true}
    ]
  };
}
```

`pd gen` merges it before anything else runs. What the overlay imports is
unioned into the file it is merged into, so a field here may be a message the
app declared elsewhere — see [a field can be a message](#a-field-can-be-a-message)
for what that costs.

You may add at any number payday does not use; changing one it does is refused:

```
an overlay may add to one of payday's entities and may not change it.
payday keeps 1, 2, 4..7 and 13..15; 3 is the app's set edge, and an app's own go in 8..12 and from 16.
Of its Event and Fact, payday keeps every number they declare, and an app's own go at any other.
Of its options, an overlay may add an index to (orm.message), and nothing else.

  app.Holder: 4 is payday's "alias" (string) and was redeclared as "handle" (string)
```

The check is there because the merge takes the overlay's word and says nothing
about it. `int64 alias = 4` is a perfectly good message, so the app still
**compiles** and what breaks does so at run time; the whole of that argument is
[the generation contract §2](../schema.md#2-overlays-adding-never-overriding).

An overlay may add an **index**, as above. payday's entity declares
`(orm.message)` too, and the two are merged: the overlay's indexes are appended
to payday's. Nothing else in either option is the overlay's. `(payday.entity)` is
payday's declaration, so an overlay that adds anything to it is refused.
`(payday.entity) = {tenanted: {via: "tenant"}}` on Holder is refused even though
it restates the default, because saying nothing **is** Holder's declaration.
Anything in `(orm.message)` other than an index is refused the same way, and
where both sides set the same value, payday's is the one kept.

What the gate asks of your own entities it asks of what an overlay adds to
these. A field the overlay stamps (`(payday.field) = {stamped: true}`) is refused
to an `Add` request, and an edge the overlay adds is read through the wall at
`Add` and again by a `Patch` that moves it. Holder's gate is the one payday
writes out by hand, and these are written into it, not into a second gate
beside it.

Nor may a field it adds to the trail's two entities, `Audit` and `Access`, be
named or JSON-named `sealed`. That is the member a row of the archive keeps what
it said in once it is sealed (`trail.SealedMember`), and no field of payday's,
so protobuf's own refusal of two fields with one JSON name does not see it: a
row with such a field would read as sealed already, and be sealed by nothing.

One thing an overlay may not say is what the file **is**. A file-level
`features.` option — `features.field_presence = IMPLICIT` copied down from the
entity file — is refused, because the merge would put it on every field of every
message in the contract, including the ones the generator wrote.

### Your past: payday's Event and Fact

payday ships two entities an app has only by asking: an **Event** for each of
its operations -- what was done, to what, by whom, when and why -- and a
**Fact** for each field of a resource from a moment on (payday#151). Ask on the
file that says the package is yours, with the two domains they take:

```proto
// proto/app/app.proto
option (payday.app) = {past: {event: 17, fact: 16}};
```

`pd gen` writes them into `proto/app/payday/event.proto` and `fact.proto` on
every run, beside payday's other entities and like them, and takes them out on
the first run after the line goes. The numbers are yours because the tables are:
anything in `7`–`247` your schema does not hold, the two different. A number of
payday's, or one number for both, is refused before anything is compiled, and
one another entity declares when the schema is read.

Add to them the way you add to the rest, with an overlay at
`proto/ext/payday/event.ext.proto` or `fact.ext.proto`: a field at any number
the template leaves empty, and an index. Every number the template declares is
payday's -- an Event's 8..12 are its kind, its actor, when, why and what about --
so what is free is what it leaves empty: 17 and from 20 on an Event, 11, 18 and
from 22 on a Fact, and on both the header's numbers, which hold the header's
fields here as everywhere (`desc` at 6), and 3, the set edge. An app moving its
own events into payday's table keeps what they had:

```proto
// proto/ext/payday/event.ext.proto
message Event {
  string desc = 6 [(payday.field) = {personal: false}];
  string payload = 17;
}
```

Two things are refused readably rather than left to buf, because buf would name
neither in words that point here:

- **A message of yours called `Event` or `Fact`**, in any of your packages. In
  yours it is a name defined twice; in another it is two Go types of one name in
  the one Go package everything is generated into. An app with events of its
  own takes them out, and adds what they have that payday's do not with an
  overlay.
- **A file of yours called `event.proto` or `fact.proto`.** Generated Go is named
  after the file and not its directory, so it and payday's copy would both be
  `event.pb.go`.

Neither applies to an app that declares no past: a calendar's `Event` is its
own.

### An RPC of your own

The CRUD of an entity is generated and the general writes are closed, so an
operation that means something is an RPC you declare.

**First ask whether it is one.** An operation that is what a generated verb
already means -- `Add` that also writes the rows the row is useless without --
is not a new verb, and declaring one makes *do it properly* a second name
beside *do it*. That is a layer and no schema change at all: see
[completing a generated verb](server.md#completing-a-generated-verb). What
belongs here is an act the generated four do not name -- retiring a widget,
verifying a secret, spending a link. It goes in an overlay too,
in `proto/ext/app/`, named after the **generated contract** it joins. That
contract is named from the file the entity is in rather than from the entity, so
`proto/app/widget.proto` produces `app/widget_svc.g.proto` and is overlaid by
`proto/ext/app/widget_svc.ext.proto` — a file declaring several entities has one
contract and one overlay for all of them:

```proto
// proto/ext/app/widget_svc.ext.proto
edition = "2023";

package app;

import "app/widget.proto";

option go_package = "github.com/acme/app";

service WidgetService {
  rpc Retire(WidgetRetireRequest) returns (Widget);
}

message WidgetRetireRequest {
  WidgetRef ref    = 1;
  string    reason = 2;
}
```

It is an overlay rather than a file of its own because of the second line of
that request: `WidgetRef` does not exist until the contract is generated, and an
RPC that names a row the way every other RPC names one has to be merged into the
place those names live. Redeclaring `service WidgetService` adds to it; the
generated methods stay.

The service keeps the name of the entity — `WidgetService` for `Widget` — and
what you write joins whatever `pd gen` put there. `WidgetRef` needs no import:
the overlay is merged **into** the file that declares it, so importing that file
would be importing itself.

Implementing it is [a layer](server.md#3-writing-a-layer), which is also what
puts it on the trail: an RPC nothing listed is audited for the same reason an
`Add` is.

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
| `alias`, `name` or `desc` of another type | a generic read that finds nothing and says nothing |
| `alias`, `name` or `desc` at another number | a header number spent, and nothing payday adds later can land on it |
| `tenanted:` with no tenant in the schema | a wall made of nothing |
| a `via:` that does not reach the tenant | a predicate narrowing to the wrong rows |
| `via:` and `field:` together | an edge says the tenant is still there; a column says only what its identifier was |
| `stamp:` over one hop, or off the front of every index | a wall that scans the table it is read from most |
| `stamp:` through a mutable or nullable step | a column that goes on saying where the row used to belong |
| `personal` beside `secret`, on an edge, or on the key | one thing said twice, or a declaration on what the trail names a row by |
| `(payday.field)` on a field of a message that is not an entity, or one the ORM maps to no column | a declaration nothing reads, kept as if it were |
| `agrees:` through a mutable step | an agreement true only until somebody repoints it |
| `list:` with no `max` | an answer with no cap |
| `list:` order not ending in the key | a cursor that repeats or skips a row |
| `list:` order over a nullable, `secret:`, list, map, JSON or bytes column | a cursor that skips a row, or carries what is never answered with |
| `watch:` with no `list:` | nothing to order, filter or cap |
| `watch:` with no `ref` in `by:` | no way to name the rows it is about |
| `watch:` with no version field | a late answer overwriting a fresh one |
| an overlay changing payday's field | a column that is no longer what its readers expect |
| an overlay adding to payday's options, beyond an index | an entity declared differently from the one payday's layers were written for |
| an overlay setting a file-level `features.` option | the whole contract silently changed by one line |
| a field of `Audit` or `Access` named or JSON-named `sealed` | a row of the archive that reads as sealed and is sealed by nothing |
| a past numbered in payday's ranges, the same for both, or with a number another entity holds | identifiers that say the wrong kind of row, or nothing |
| a past declared in two files | two answers to the number its copies are written with |
| a message `Event` or `Fact`, or a file `event.proto` or `fact.proto`, of an app that declares a past | a name defined twice, or two files written as one |
| a copy of the past with no declaration, or at another number | a table numbered by nothing the schema says |
| two `go_package`, or two proto packages | two Go packages is two ent schemas, and the wall is an edge |
| generated code from a different payday | a wall written by one version, read by another |
| the database not matching the schema | a server up on a table that is not there |

And one **warning**: a `list:` whose order, or one of its named orders, no
index covers -- behind the wall, an index that begins with the tenant. `pd gen`
prints it; it used to be dropped with the rest of what the plugins said of a
generation that succeeded, which is how an app could go on generating dozens of
them without a word.

---

## 9. `pd entity add`

```sh
$ pd entity add --tenanted --watch Robot .
```

It is not for saving typing. It is for the two things that go wrong on the
fourth entity: a domain that collides with one you already used, and tenancy
written out when it should have been left silent.

```
Options:
       --tenanted        its rows belong to a tenant, and the wall narrows every read
       --tenant          refused: payday ships the tenant, and an app does not declare a second
       --global          not behind the wall, and said on purpose
       --watch           also a List and a Watch, with the version field a Watch needs
       --file string     the .proto to write into; one named after the entity by default
```

`--tenanted` writes **no** tenancy line, because that is the declaration: what
gets written out is the answer that leaks when it is wrong. `--watch` writes the
version field, `ref` among the filters and an index over the order -- the tenant
first, for a tenanted one -- so the watch generates and nothing warns.

What comes back is a draft to finish rather than a finished entity: it ends in a
`TODO` for this entity's own fields. What it does not leave for you is anything
generation would refuse — the erased field is written on every path, because
saying what `Erase` does is owed by every entity and not only by one that is
watched.

It skips the two numbers an app's past is declared at, which no file holds
until the next generation writes the copies, and refuses `Event` and `Fact` in
an app that declares one.

`pd entity list` prints what is declared and which domain each holds.

---

## 10. After a change

```sh
$ pd gen .                 # everything the schema says
$ pd gen --check .         # regenerate and fail if anything was not in step — this is CI
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

## Where to go next

| | |
| --- | --- |
| [permissions.md](permissions.md) | who may see what comes out of this |
| [server.md](server.md) | the stack that serves it |
| [client.md](client.md) | what a page does with what this generates |
| [testing.md](testing.md) | asserting that a declaration means what you thought |
| [schema.md](../schema.md) | why payday owns these entities, and what it costs |
| [runtime.md](../runtime.md) | what `List` and `Watch` generate, and what is refused |
