# More than one proto package

Most apps have one, never think about it, and can stop reading here. Three
situations need a decision, and they are different problems that look alike:

| | What you have | What settles it |
| --- | --- | --- |
| **1** | One app, one package | nothing — this is the default |
| **2** | One app, entities in **two packages** | `option (payday.app)` |
| **3** | **Two apps** in one process | their packages must differ |

The first two are about where payday copies its own entities. The third is
about a registry that panics before `main`.

---

## 1. One package

`pd new` writes `proto/app/thing.proto`, package `app`, and payday reads that
package off your schema. Its own entities — the tenant, the holder, the trail,
the queue — are copied **into** it, so a caller of your app says
`app.TenantService/Get` and never learns the name of the framework you built it
with.

There is nothing to configure and nothing to declare. Skip to
[the schema guide](schema.md).

---

## 2. One app, two packages

An app may keep entities in more than one proto package. Then payday cannot
read which one is "the app's", because there are two, and the answer decides
where its own entities are copied and where `pd entity add` writes.

So the app says:

```proto
edition = "2023";

package acme;

option (payday.app) = {};
```

One file in the package is enough — the option is about the **package**, and
presence is the whole of it. Setting it in two packages is refused: two answers
to "where do payday's entities go" is the thing it exists to prevent.

### Why you would want two

Because a name can outlive the service holding it. Several services may want
one word for the thing they are all about — `fleet.Robot`, read off a
certificate by a service that did not issue it — while everything each service
keeps for itself stays in a package of its own.

Without this the choice was between naming everything after one app and putting
one app's bookkeeping in the shared vocabulary.

```
telemetry    proto/telemetry/     option (payday.app)  ← the app's own
             proto/fleet/         Robot                ← the shared word
```

payday's own test app is the demonstration rather than the description: it holds
entities in `app` and in `shared`, and it is one app, because `app/robot.proto`
declares the option. `shared.Thing` generates, serves and reaches TypeScript
like any other entity.

### A shared package is a shared *schema*, and this is the trap

Putting a message in a package two services both use is a claim that
`fleet.Robot` **means the same thing** in both. Nothing enforces it. Each
service compiles its own copy of the `.proto` and a fully-qualified name is what
tells anyone they agree — so two copies that have drifted publish two different
messages under one name, which is the one thing a fully-qualified name exists to
prevent.

It is worth being concrete, because this has already happened (a real pair of
services; the names here are stand-ins). `dispatch` and `telemetry` both declare
`fleet.Robot`, and they are not the same message:

| | `dispatch` | `telemetry` |
| --- | --- | --- |
| tenancy | behind the wall, by a `tenant` edge | `global: {}` |
| the tenant | field 2, an edge | field 12, `string tenant_ref` — "as last seen in a certificate" |
| what else | `model`, `serial`, `keeper`, `attested` | `channels`, `agent_version`, `date_seen` |

Both are right about their own service. Neither is wrong. What is wrong is the
name: a wire message from one decoded as the other is not a Robot with fields
missing, it is a different row shape at the same numbers.

Two things follow.

- **They must never be linked into one process**, and if they are, the failure
  is the loud one from §3 rather than a silent mis-decode: two files at
  `fleet/robot.proto`, and a panic before `main`.
- **A shared package needs one owner.** One repository holds the `.proto`, the
  rest copy it and nobody edits their copy — the same discipline payday holds
  itself to for its own entities, which is why `pd gen` writes the copies back
  on every run, and why `pd gen --check` lists a copy that was edited as **out
  of date** and fails the build that asked. A package shared by editing two
  copies is a package that agrees until somebody is in a hurry.

If what two services actually have is two different concepts, the fix is two
names. A shared vocabulary is worth having when it is shared; it costs more than
it saves when it is only spelled the same.

### The `go_package` is still one

Two proto packages, **one** Go package. That is not a limitation payday chose:
every entity generates an ent schema, an edge is a relation between two of them,
and ent cannot hold an edge to a schema in another Go package. The wall between
tenants is an edge.

```proto
// proto/app/robot.proto        proto/shared/thing.proto
package app;                   package shared;
option (payday.app) = {};
option go_package = "github.com/acme/thing";   // ← the same in both
```

`pd gen` refuses two, naming both files.

### What a second package does not get you

- **Not a second wall.** Tenancy is per entity, wherever it is declared.
- **Not a second database.** One app is one ent schema and one connection.
- **Not a second `payday/` copy.** The tenant and the holder are copied into the
  declared package only, and an entity in the other package reaches them by an
  ordinary edge: an edge names its target in full — `app.Tenant` from a file in
  `shared` — so crossing the package boundary is nothing the schema has to be
  told about. It is one Go package either way, which is what makes that true.

---

## 3. Two apps in one process

This is the other problem entirely. `dispatch` embeds `directory` — the real
`directory`, built from `directory`'s own `cmd.Build`, answering over a
`bufconn` instead of a socket — so two payday apps are linked into one binary.
(Another real pair, and the names are stand-ins again.)

**It works when their proto packages differ, and it panics before `main` when
they do not.** A protobuf registry is per process and keys files by their path,
so two apps that both kept the default package each register
`app/payday/holder.proto` and the second one to init blows up. Nothing you write
prevents it and no test of either app alone catches it. What registers is the
file and not the instance, so two of the *same* app in one process were never
the problem: one file path is registered once, at init, however many times the
app is built.

```
directory     proto/directory/payday/holder.proto     package directory
dispatch      proto/fleet/payday/holder.proto         package fleet
```

That is the whole rule. It follows from §1 without being an extra thing to
remember: the package name is the path on disk, so distinct packages are
distinct paths.

### What is shared and what is not

| | |
| --- | --- |
| **Identifiers** | already compatible. A tenant is domain 1 in every payday app and a `pdid` is unique without coordination, so a row minted by one is nameable by the other |
| **`payday.` on the wire** | two names survive, in every payday app: `payday.BatchService` and `payday.TokenService`. Both are deliberate — they are transports and not domain concepts, there is nothing per app in either, and a generic client finds them by those names |
| **Everything else** | separate. Two schemas, two databases, two walls, two sets of servers |

`directory.Holder` and `fleet.Holder` are two messages with two histories, and
that is correct: one is the identity store's person and the other is this app's.

### Two apps means two of everything above them

A `Conn` reaches one of them, so:

```go
mine   := pdcmd.NewIn(local{c}, "fleet")
theirs := pdcmd.NewIn(embedded{c}, "directory")
```

`pdcmd.New` guesses only when there is one answer, and refuses naming both when
there is not: a connection cannot say which app it speaks to.
`pdcmd.Packages()` lists every package with entities in it.

**§2 and §3 stack, and the order they are read in is the useful part.** `New`
asks for a package declaring `option (payday.app)` first, and falls back to
counting packages only when no one package declares it:

| in the process | `pdcmd.New` |
| --- | --- |
| one package | that one |
| two packages, one declares | the declared one — this is §2, and it is one app |
| two packages, none declares | refused, naming both — this is `dispatch` today |
| two packages, both declare | refused: two apps, and each said so |

So an app that embeds another and wants `New` to keep working declares its own
package, even though it has only one. That is not a workaround; it is the same
sentence the option always says — *this package is the app's own* — being useful
in a second place.

---

## What is refused

The first three are refused at generation, with the way out named, because each
of them fails quietly if it is left to be noticed later. The fourth is not
something generation can see: `pd gen` reads one app, and nothing in that app
mentions the other. The process catches it instead, and loudly.

| | | caught by |
| --- | --- | --- |
| two proto packages, neither declaring `(payday.app)` | payday has nowhere to copy its entities and no answer that is not a guess | `pd gen` |
| two packages both declaring it | two answers to the question the option exists to settle | `pd gen` |
| two `go_package` | two sets of ent schemas, and no edge between them — so no wall | `pd gen` |
| two apps sharing a proto package | a duplicate file path in the registry | a panic before `main` |

---

## Where to go next

| | |
| --- | --- |
| [schema.md](schema.md) | declaring an entity, and everything else generation refuses |
| [commands.md](commands.md) | `pdcmd.NewIn`, and a command tree per connection |
| [schema.md](../schema.md) | the generation contract: what payday owns, overlays, identifiers |
