# The runtime

What payday is made of, and which of it you get by declaring something rather
than by writing it.

This is the map. The [guides](guide/) are how to use each part; the package
comments are the detail. What is here is the shape of the whole, and the
decisions that gave it that shape.

- [1. Three kinds of code](#1-three-kinds-of-code)
- [2. The packages](#2-the-packages)
- [3. What is generated](#3-what-is-generated)
- [4. What you write](#4-what-you-write)
- [5. How each rule is enforced](#5-how-each-rule-is-enforced)
- [6. Background work](#6-background-work)
- [7. What payday does not do](#7-what-payday-does-not-do)

## 1. Three kinds of code

An app is made of three things, and the line between them is one question:
**does this name `*ent.Client` or `predicate.*`?**

Those types are generated into your app from your schema. Anything that has to
say one of their names cannot live in a library, so it is generated too.
Everything else is a package you import.

```
payday/…       a library. Knows nothing about your entities
server/pd/     generated. Knows all of them
server/core/   yours
```

The third is the smallest, and that is the point. What is left for you to write
is your rules — not the wall, not the trail, not the paging, not the page's
cache invalidation.

## 2. The packages

Every one of these has a package comment that is the real documentation. This
table is for finding the right one.

### Deciding who and what

| | |
| --- | --- |
| [`frame`](../frame) | who a request is from. Put in the context once, read by everything that has to decide |
| [`auth`](../auth) | reads a credential and resolves it to a frame: `Plain`, `MTLS`, `Bearer`, `Seq`. It does not issue one |
| [`auth/authoidc`](../auth/authoidc) | reads the token an external provider issued — signature, issuer, audience, expiry — and turns its claims into an identity |
| [`auth/authsession`](../auth/authsession) | the browser's half: a `POST /session` that sets an opaque cookie, and the handler that reads it back. The app supplies a `Verify` and a `Store` |
| [`gate`](../gate) | what the caller may **do**, as against what they may see. Most of the rule is stated here and enforced in the query |
| [`audit`](../audit) | the trail. A recorder inside the transaction, and a layer serving the trail's own RPCs |

### Naming things

| | |
| --- | --- |
| [`pdid`](../pdid) | the identifier every row is named by: a UUID carrying one byte that says what kind of thing it names |
| [`slug`](../slug) | the human-readable form — `@acme/arm-01#robot`. What a person writes; never on the wire |
| [`header`](../header) | the part of a row that is the same whatever entity it is, read from any of them |

### Serving

| | |
| --- | --- |
| [`grpcx`](../grpcx) | what every call goes through: deadlines, recovery, logging, rate limits, and what is closed |
| [`web`](../web) | the same gRPC server answering Connect and gRPC-Web, for browsers |
| [`config`](../config) | the blocks every app is configured with, and a loader that fills in any struct |
| [`spin`](../spin) | background work a layer has, found rather than declared |
| [`watch`](../watch) | publishes what a call changed, once its transaction committed |
| [`batch`](../batch) | several writes as one transaction, with the four transport rules re-applied per operation |
| [`pderr`](../pderr) | the shape of a refusal: which field, and why |
| [`migrate`](../migrate) | plans and applies versioned migrations, using the atlas packages ent already has |
| [`version`](../version) | what build is running, read from what the toolchain already stamps |
| [`pdcmd`](../pdcmd) | the commands every app has, ready to mount on your binary |
| [`pdtest`](../pdtest) | the half of a test harness that does not know what app it is for |

### And the schema itself

| | |
| --- | --- |
| [`schema`](../schema) | payday's own entities, embedded, so an app's copies can be checked against them |
| [`pdpb`](../pdpb) | the generated Go for `payday.entity` and `BatchService` |

## 3. What is generated

`pd gen` writes these into your app. You do not edit them, and `pd gen --check`
in CI is what keeps that true.

| | Why it cannot be a library |
| --- | --- |
| the messages and ent schema for Tenant, Holder, Audit, Outbox | they come out of the merged proto, with your added fields on them |
| the domain constants and their registration | they come out of the schema's `(payday.entity).domain` |
| the `Sink` — minter, wall, recorders | a scope is `predicate.Robot`, which is your type |
| the `Gate` and `Audit` layers | they overlay `app.Server`, which is generated |
| the per-entity wall predicates | `tenanted: {via: …}` becomes `predicate.Robot` |
| `List` and `Watch` — request, response, RPC, implementation | the ent query builder is your type |
| the batch dispatcher | it has to know every RPC the app has |

### `List` and `Watch` are the same machine

Both were the argument for generating anything at all. Written by hand they come
out as the same eighty lines per entity: get the scope, narrow it, apply the
filters, decode the cursor, eager-load the edges, order, take `size+1`, cut one
off, encode the next cursor.

Four things differ between two hand-written `List`s — the order, the edges to
load, what the filters mean, the page size — and **only the filters are
domain**. The rest is declarable, and hand-written copies of it drift as soon as
there are three.

So they are declared:

```proto
option (payday.entity) = {
  list: {
    order: [{field: {name: "alias"}}, {field: {name: "id"}}]
    by:    [{name: "ref"}]
    size:  20
    max:   100
  }
  watch: {}
};
```

See [the schema guide](guide/schema.md#5-list--the-page).

## 4. What you write

Four things, and the last one matters most.

**Domain rules.** Alias normalisation, filter limits, cascade behaviour —
whatever your app means that no schema can state.

**Authorization for your own entities.** payday's `gate` covers the wall and the
rules about Tenant and Holder. "Only an admin may erase a Robot" is yours: your
own layer, or a `gate.Policy` you inject. The generated `Gate` is not edited.

**The filters in your `List`s.** Paging is the runtime's.

**The wiring.** Ten lines in `cmd/serve.go` that stack the layers, build both
server instances, and order the interceptors.

That last one is deliberately not `pd.Serve(cfg)`. The order of the stack and
the existence of an ungated instance are the two most load-bearing facts about
an app, and a framework that supplied them would be hiding the thing a reader
most needs to see. **The wiring stays where it can be read; the runtime supplies
the parts.**

## 5. How each rule is enforced

The strength of a mechanism, strongest first:

**generation failure > compile error > the generator writes it > a check >
a test > a lint > a sentence in a document.**

Everything payday enforces is placed as far left as it goes.

| What | How |
| --- | --- |
| every entity declares a domain | generation failure |
| domain numbers do not collide | generation failure |
| an overlay does not touch a payday-owned field number | generation failure — adding only |
| every entity says whether it is behind the wall | generation failure. Saying nothing means `tenanted:`, which is the loud way to be wrong |
| the last key in `list.order` is the row key | generation failure |
| an index covers `list.order` | generation failure |
| `list.max` is set | generation failure |
| a `watch:` entity has a version field | generation failure |
| one `go_package` per app | `pd gen` refuses |
| identifiers are domain-tagged UUIDs | the minter, on both the write and the read path |
| a `Watch` with no filter | the generated implementation answers `InvalidArgument` |
| the watch broker is named | required config field — an unnamed one is right for one replica and silently wrong for two |
| generated code and linked payday agree | `version.Same`, refused at `NewSink` |
| the database is not behind the schema | `migrate.Check`, before serving |
| the runtime assumes no file system | CI builds `GOOS=js GOARCH=wasm` |
| every layer is an `enttx.Binder` | `pd doctor` finds one that is not |
| a request with no frame | refused |
| `Patch` and `Apply` are closed | at the **transport**, not in a layer — a layer would close them to the server itself |
| the wire does not break | `buf breaking` in CI, against the published label and the branch |

Two of these deserve their reasoning spelled out.

**Saying nothing means walled.** An entity that forgot to declare its tenancy is
behind the wall. Get that wrong and every row vanishes and you know in minutes;
get the other default wrong and every row is visible to everybody and nobody
finds out. **An assumption should fail loudly, and the dangerous answer should
be the one somebody had to write down.**

**No superuser.** Going around the wall is being handed a server instance that
never had one, which is a line of wiring a reader can find — not a claim that
opens things up wherever it happens to be checked.

### And what is deliberately *not* enforced

**Layer order.** A stack's value is that you can put anything anywhere. Ranking
the layers would mean asking the framework where your own layer goes. A warning
from `doctor` is enough.

## 6. Background work

A layer that has something to do on a clock answers `spin.Spinner`, and
`spin.Run` finds the ones that do.

The alternative was `Spin(ctx)` on the generated `Server` interface, walked
recursively. That interface is generated, so a method on it is one the generator
emits and every `Overlay`, `StaticServer` and `UnimplementedServer` carries —
and then every layer inherits an empty `Spin` saying "nothing here", which is
exactly the fact worth being able to see.

**Capability is found, not declared.**

A `Spin` that returns kills the process: a sweep loop that stopped quietly is
found days later. Whether it also fails readiness is separate, and the default
is no — one dead loop should not pull the whole replica out of the load
balancer.

## 7. What payday does not do

Some of these are gaps and some are decisions. The difference is written down
because a reader cannot tell them apart.

**Decided against:**

- **Files and blobs.** Not payday's concern. An app that needs them attaches
  something that does them.
- **Jobs and schedules.** `spin` is a background loop a layer owns; a job queue
  is a durable list of work. `spin` makes a place for one to sit and is not one.
- **A superuser.** See above.
- **A runtime schema-compatibility probe.** Breaking the wire is a migration and
  `buf breaking` is what says so, at build time where there is enough
  information to tell a breaking change from a compatible one.

**Not built, and the reason is written where the seam is:**

- **An external watch broker.** `watch.Broker`, `payday.Outbox` and `pd.Drain`
  are the place for one; the implementations are `memory` and `none`.
- **Bidirectional `Watch`.** Multiplexing many subscriptions onto one stream
  changes the transport and not the fan-out, which is where the cost actually
  is.
- **Issuing a credential that somebody else verifies.** A token with an issuer,
  a signing key and a JWKS endpoint is what an identity provider is, and payday
  is not one. `auth/authoidc` reads what one issued; `auth.Bearer` reads a
  credential a deployment minted itself. There was an `auth.Issuer` interface
  here for a while; payday neither implemented nor called it, which made it a
  naming convention wearing a seam's clothes.

  `auth/authsession` is **not** the exception it looks like. A session key is
  opaque, is verified by nothing but the server that minted it, and is a row
  rather than a claim — closer to a row number than to a token. That is why
  revoking one is a delete, and why it needs no key, no rotation and no
  audience. The line is: payday will hand a browser a handle to state it keeps
  itself, and will not tell a third party who somebody is.

- **Checking a secret.** `authsession.Verify` is a function payday calls and
  never writes: the people are in the app's schema and their secrets are
  wherever it keeps them. `github.com/lesomnus/roster` is a payday app that does
  this part, and a deployment with one behind it fills the seam with a single
  RPC.
- **Client trace propagation.** From a click in the browser to a span on the
  server. Mechanical, since drpc carries metadata, and worth doing.

## See also

- [guide/server.md](guide/server.md) — how to use all of this
- [SCHEMA.md](SCHEMA.md) — what a declaration is allowed to say
- [TENANCY.md](TENANCY.md) — the wall, in full
- [CLIENT.md](CLIENT.md) — the browser half
