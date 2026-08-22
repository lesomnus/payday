# The runtime

What payday is made of, and which of it you get by declaring something rather
than by writing it.

This is the map. The [guides](README.md#guides) are how to use each part; the
package comments are the detail. What is here is the shape of the whole, and the
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
payday/…                   a library. Knows nothing about your entities
server/bare/, server/pd/   generated. Know all of them
server/core/               yours
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
| [`trail`](../trail) | how long the audit table keeps a row, per kind of thing, and where it goes when it leaves |
| [`pdtest`](../pdtest) | the half of a test harness that does not know what app it is for |

### And the schema itself

| | |
| --- | --- |
| [`schema`](../schema) | payday's own entities, embedded, so an app's copies can be checked against them |
| [`pdpb`](../pdpb) | the generated Go for what a schema declares — `(payday.entity)`, `(payday.field)`, `(payday.app)` — and for the two services payday serves on an app's behalf: `BatchService`, and the `TokenService` that `auth.Remote` asks |

## 3. What is generated

`pd gen` writes these into your app. You do not edit them, and `pd gen --check`
in CI is what keeps that true.

| | Why it cannot be a library |
| --- | --- |
| the messages and ent schema for Tenant, Holder, Audit, Outbox | they come out of the merged proto, with your added fields on them |
| the domain constants and their registration | they come out of the schema's `(payday.entity).domain` |
| one bare server per entity, straight onto `*ent.Client` | every statement it builds names a type of yours |
| the `Sink` — minter, wall, recorders | a scope is `predicate.Robot`, which is your type |
| the `Gate` and `Audit` layers, and `Secret` where a field declared one | they overlay `app.Server`, which is generated |
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
most needs to see. Both are then answerable by reading one file: which layer
sees a call first, and who is handed the instance the wall was never installed
on. **The wiring stays where it can be read; the runtime supplies the parts.**

## 5. How each rule is enforced

The strength of a mechanism, strongest first:

**generation failure > compile error > the generator writes it > a check >
a test > a lint > a sentence in a document.**

Everything payday enforces is placed as far left as it goes.

| What | How |
| --- | --- |
| what a schema is allowed to say | generation failure. The whole list is in [the schema guide](guide/schema.md#8-what-generation-refuses) — domains, erasure, tenancy that reaches a tenant, `list:`, `watch:`, overlays, one Go package |
| every entity says whether it is behind the wall | saying nothing means `tenanted:`, which is the loud way to be wrong; see [TENANCY.md](TENANCY.md#2-the-default-is-the-loud-one) |
| identifiers are domain-tagged UUIDs | the minter, in every generated `Add`: an identifier of another kind, or one this app did not make, is refused before a statement is built |
| a `Watch` with no filter | the generated implementation answers `InvalidArgument` — a watch that says nothing is the whole table, for as long as it is open |
| the watch broker is named | required config field — an unnamed one is right for one replica and silently wrong for two |
| generated code and linked payday agree | `version.Same`, refused at `NewSink` |
| the database is not behind the schema | `migrate.Check`, before anything is served on it |
| the runtime assumes no file system | CI builds `GOOS=js GOARCH=wasm` |
| every layer is an `enttx.Binder` | `pd doctor` finds one that is not — a missing `WithDriver` is nothing until the first transaction |
| a request with no frame | refused. There is no scope that means "everything, because nobody asked" |
| `Patch` and `Apply` are closed | at the **transport**, not in a layer — a layer would close them to the server itself |
| the wire does not break | `buf breaking` in CI, against the published label and the branch |
| an index covers `list.order` | a **warning** from `pd gen` and not a refusal: a hundred rows of configuration need no index, and refusing to generate for them would be insisting on a cost nobody is paying |

One rule is in no row of that table, because what enforces it is an absence.
**No superuser**: going around the wall is being handed a server instance that
never had one, which is a line of wiring a reader can find. See [the two
servers](guide/server.md#2-the-two-servers-and-why-there-are-two).

### And what is deliberately *not* enforced

**Layer order.** A stack's value is that you can put anything anywhere. Ranking
the layers would mean asking the framework where your own layer goes. What the
order is stays readable instead: it is the argument list in `cmd/serve.go`, and
the last builder given is the one a request meets first.

## 6. Background work

A layer that has something to do on a clock answers `spin.Spinner`, and
`spin.Run` finds the ones that do.

The alternative was `Spin(ctx)` on the generated `Server` interface, walked
recursively. That interface is generated, so a method on it is one the generator
emits and every `Overlay`, `StaticServer` and `UnimplementedServer` carries —
and then every layer inherits an empty `Spin` saying "nothing here", which is
exactly the fact worth being able to see.

**Capability is found, not declared.** Nothing is added to `Server`, `Run` walks
a stack that was built and asks each layer, and a layer with no background work
writes not one line.

A `Spin` that **gives up** — answers an error — takes the process down: `Run`
stops the rest, waits for them, and answers with the failure. That direction is
chosen for one failure mode. A sweep that gave up quietly is found days later,
by which time the table it was keeping small is why something else fell over.
Answering nil is the opposite statement — a loop saying it is finished, which is
not a failure and stops nothing else — so a pass that should be tolerated logs
and answers nil.

None of it touches a health check, and that is a decision rather than an
omission: one dead loop should not pull a whole replica out of the load balancer
while every request it was answering was fine. A deployment that wants otherwise
says so where it reads `Run`'s error, which is one deployment deciding rather
than payday deciding for all of them.

### The trail's retention is one of them, and it is the loud one

`trail.Sweep` is a `spin.Spinner` like the others and is not the same kind of
loop. The sweeps an app writes collect rows that are already refused — an
expired attempt is refused the moment it is presented — so an outage of one
costs disk. **Nothing else applies a retention window**, so a deployment whose
trail sweep has been failing for a month has been keeping records it told
somebody it would not.

Which is why the policy is refused where the process comes up rather than at the
first pass: `config.AuditConfig.Policy()` returns an error, `Build` stops, and
an operator finds out while they are watching. A `retain` with nowhere to put
what leaves it is the configuration mistake worth being loudest about, because
it *works* — the sweep runs, the table stops growing, every graph improves, and
what it is doing is destroying the evidence.

It is **per kind of thing**, keyed on `Audit.domain`. A deployment's obligations
are not uniform across its entities: what was done to a person is under a
privacy regime and eventually has to stop existing, and what a machine did is an
operating record with the opposite requirement. One clock over the table forces
the shorter of the two onto everything.

```yaml
audit:
  profile: pipa
  archive: /var/lib/app/audit
  by:
    robot:
      profile: forever
```

`trail.Profiles` carries the sentence each number comes from — PCI-DSS 10.5.1,
45 CFR 164.316(b)(2)(i), and so on — so a value in a configuration file is
arguable rather than arbitrary. It is a starting point and **not** a compliance
guarantee: what a deployment is obliged to keep depends on what it processes and
for whom, and payday knows neither.

Empty is forever, which is the only honest default. A version upgrade is not the
right thing to decide how long somebody's evidence lasts.

## 7. What payday does not do

Some of these are gaps and some are decisions. The difference is written down
because a reader cannot tell them apart.

**Decided against:**

- **Deciding when one person leaves the trail.** The retention policy above is
  about **age** and reaches everybody's rows at once. A right-to-erasure request
  is about a subject — *which* rows, and *when*, is what an app owes somebody
  under a regime payday cannot know. What payday does supply is the half with no
  judgement in it: `pd.ForgetInTrail` blanks `value` and `patch` for a set the
  caller chose, and `trail.Forget` does the same to the archive, because a
  mechanism that stopped at the database would destroy the copy an operator can
  see and leave the copy on the disk beside it.

  Everything else stays — who acted, what they did, which object, when. That is
  the record a trail exists to be and what a legal-obligation exemption is an
  exemption *for*. The **actor** is not touched: it is personal data only
  because it resolves, which is a property of the row it points at, and blanking
  it would destroy *who did this*.
- **Files and blobs.** Not payday's concern. An app that needs them attaches
  something that does them.
- **Jobs and schedules.** `spin` is a background loop a layer owns; a job queue
  is a durable list of work. `spin` makes a place for one to sit and is not one.
- **A superuser.** See above.
- **A runtime schema-compatibility probe.** Breaking the wire is a migration and
  `buf breaking` is what says so, at build time where there is enough
  information to tell a breaking change from a compatible one.

**A watch that crosses replicas** is `config/brokerpg`: `watch.broker: postgres`,
`LISTEN`/`NOTIFY` on the rows' own database. There is nothing to store — a
notification reaches whoever is listening and is then forgotten — so an app
already on Postgres needs no second piece of infrastructure, and the difference
between one replica and several is a line of configuration.

What travels is the identity of what changed and not the row: what a subscriber
may see is decided per subscriber, by re-reading each row through their own
narrowing, and putting content on a channel every replica reads would answer
that question once, in the wrong place, for everybody.

Two things to know beside it. `broker: none` refuses a `Watch` outright —
`watch.ErrNoBroker` — rather than handing back a stream that sends a snapshot
and then never speaks. And `pd.Drain` publishes into the broker it was given,
so an outbox buys **durability** and not fan-out on its own: with `memory` a
drained event still reaches one process, and with `postgres` it reaches all of
them. The pair is what a deployment that can lose neither wants.

**Not built, and the reason is written where the seam is:**

- **A watch broker that is a message bus.** `config.RegisterBroker` is the
  registry, in the shape `config.RegisterDriver` has and for the same reason: a
  broker is a client for something that has to be linked in, so it is a package
  of its own and an app blank imports it. Writing one against NATS or Redis is
  ordinary work nobody has needed yet.

  What **is** built is `postgres` — above — which is the one that needs no
  bus at all.
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
  wherever it keeps them. An identity store is itself a natural payday app — the
  people are entities of its schema and their secrets are fields it never
  answers with — and a deployment with one behind it fills the seam with a
  single RPC.
- **Client trace propagation.** From a click in the browser to a span on the
  server. Mechanical, since the page speaks Connect on both wires — HTTP in
  production, a message port in the sandbox — and a Connect call's headers are
  what the server reads as metadata. Worth doing.

## See also

- [guide/server.md](guide/server.md) — how to use all of this
- [SCHEMA.md](SCHEMA.md) — what a declaration is allowed to say
- [TENANCY.md](TENANCY.md) — the wall, in full
- [CLIENT.md](CLIENT.md) — the browser half
