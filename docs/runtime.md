# The runtime

What payday is made of, and which of it you get by declaring something rather
than by writing it.

This is the map. The [guides](readme.md#guides) are how to use each part; the
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
| [`seen`](../seen) | what a transaction has already asked about the rows it names, so the gate and the sink ask each question once rather than once per write |
| [`audit`](../audit) | the trail. A recorder inside the transaction, and a layer serving the trail's own RPCs |
| [`access`](../access) | the access record: who read or wrote whose personal data, from where, and how it ended. An interceptor, and the writer it hands rows to |

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
| [`version`](../version) | what build is running, read from what the toolchain already stamps |
| [`pdcmd`](../pdcmd) | the commands every app has, ready to mount on your binary |
| [`trail`](../trail) | how long the audit table keeps a row, per kind of thing, and where it goes when it leaves |
| [`pdtest`](../pdtest) | the half of a test harness that does not know what app it is for |

### And the schema itself

| | |
| --- | --- |
| [`schema`](../schema) | payday's own entities, embedded, so an app's copies can be checked against them |
| [`pdpb`](../pdpb) | the generated Go for what a schema declares — `(payday.entity)`, `(payday.field)`, `(payday.app)`, `(payday.event)` — and for the two services payday serves on an app's behalf: `BatchService`, and the `TokenService` that `auth.Remote` asks |

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
| whose data a message carries (`Subjects`, `SubjectRefs`), and the access record's recorder and writer | they read the app's messages by what its schema declared `personal`, and write its `Access` |
| `pd.EventKinds`, in an app that declares a past: every event message by the kind its rows are written under | each is a message type of yours, read back from a row's `kind` |
| `ts/gen/events.ts`, in every app: `events`, a protobuf-es registry of the event messages, and `kinds` | the page's transport is handed it, and it names the app's schemas |

An app that declares a past also has `proto/<pkg>/payday/past.lock` beside the
copies of payday's entities: not code, and not written over, but the record of
every event message the app ever declared, which `pd gen` holds the schema to
and only adds to. A row of an event is read back by name for as long as it is
kept, so what it was written with is never taken back; see
[Keeping a past](guide/past.md#4-the-lock).

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
| every entity says whether it is behind the wall | saying nothing means `tenanted:`, which is the loud way to be wrong; see [tenancy.md](tenancy.md#2-the-default-is-the-loud-one) |
| identifiers are domain-tagged UUIDs | the minter, in every generated `Add`: an identifier of another kind, or one this app did not make, is refused before a statement is built |
| a `Watch` with no filter | the generated implementation answers `InvalidArgument` — a watch that says nothing is the whole table, for as long as it is open |
| the watch broker is named | required config field — an unnamed one is right for one replica and silently wrong for two |
| the spin lease is named | required config field, for the same reason: without one every process runs every loop — every sweep twice once there are two |
| a loop one process of a deployment runs has a name of its own | `spin.Run` refuses it under a lease, before anything starts — the name is what the processes agree on |
| generated code and linked payday agree | `version.Same`, refused at `NewSink` |
| the database is not behind the schema | `entschema.Check`, before anything is served on it |
| the runtime assumes no file system | CI builds `GOOS=js GOARCH=wasm` |
| every layer is an `enttx.Binder` | `pd doctor` finds one that is not — a missing `WithDriver` is nothing until the first transaction |
| the access record is wired | `pd doctor` says which of its interceptor, writer and recorder the app's code does not name — missing, nothing fails and nothing is recorded |
| a download says what it is for | the access interceptor refuses one without `payday-reason-bin`, and one whose row cannot be written |
| a request with no frame | refused. There is no scope that means "everything, because nobody asked" |
| `Patch` and `Apply` are closed | at the **transport**, not in a layer — a layer would close them to the server itself |
| the wire does not break | `buf breaking` in CI, against the published label and the branch |
| an error is logged as the error, and one to a record | a test, `internal/logcheck`, over payday's packages, the code it generates and what `pd new` writes; and of what the `otelslog` bridge does with an error, which is what makes it the rule (see [logging an error](guide/server.md#10-logging-an-error)) |
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

### One process of many (#54)

Without a lease every process that calls `Run` runs every loop, which is right
for one replica and **silently wrong for two**: each sweeps what the other is
sweeping, a notification goes out twice, the outbox is published twice, and
nothing reports it. `spin.Run(ctx, layers, spin.WithLease(l))` runs a loop only
in the process that holds its lease, and the serve command builds the lease from
`spin.lease` — required, for the reason `watch.broker` is.

- **Exclusive is the default.** Every loop is one process's under a lease but the
  ones that say they are each process's own — a cache it keeps, a stream it holds
  to keep one — with `spin.InEveryProcess`. Of the two mistakes, a per-process
  loop left unmarked is found the day the other processes go stale; a deployment
  loop marked by mistake is the duplicate sweep this is for.
- **A loop is named.** The name is what the processes agree on, so `Run` refuses
  an exclusive loop with none of its own and two with one name, before anything
  starts. A `spin.Func` is called by its type and so has none; `spin.Called`
  names one. `payday.outbox` and `payday.trail` are payday's.
- **A rename is a new lock.** A process of the old build holds the old name and
  one of the new build the new one, so through a rolling upgrade that runs both
  the loop runs in one of each, for as long as the upgrade takes.
  `spin.Formerly(old, s)` says what the loop was called: under a lease it holds
  that name as well as its own, every name in one order, so the two builds take
  turns and the loop runs in one process of either. A name a loop was formerly
  called is refused beside another loop holding it, as its own name is. Once no
  process of the old build is left, the old name can go.
- **A lost hold is not a failure.** The loop's context ends, and the process waits
  to hold the lease again; shutting down while waiting is an ordinary stop.
- **The leases.** `local` holds everything in this process, for a deployment of
  one; `none` holds nothing, for a process that only answers requests; `postgres`
  (`config/leasepg`, `spin/spinpg`) holds a session advisory lock on the app's
  own database — no table, no clock, released when the session ends — on one
  connection per process that asks for TCP keepalives of its own and pings what it
  holds. That connection is dialed outside the lock its holds share, under the
  context of the loop that asked, so a database that does not answer holds up
  nothing a shutdown waits for. It does not go through a transaction pooler, for
  the reason `LISTEN` does not; `spin.dsn` is for a direct address.
- **Built on the process's context.** `c.Spin.Build(ctx, c.Db)` -- in the serve
  command for the loops, in `cmd.Build` for the trail's turns -- like
  `c.Watch.Build(ctx, c.Db)`, is given the context the process logs into, and a
  lease says there which names a lost connection took with it. One built on a
  context of its own would say it to the OpenTelemetry global, which nothing
  installs.

**It is at most one at a time, but for a moment around a failover — not exactly
once.** A process that lost its hold goes on until it notices, and one cut off
from the database goes on with whatever it had already sent. A lease takes
duplicate work from always to rarely, so a loop still has to be safe to run
twice; one that cannot wants fencing or a queue, which `spin` is not (see what
payday does not do, below).

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

#### What a row holds is the other setting (#92)

How long is one question and **how much** is another, and the second is
answered per kind too -- `audit.content`, `audit.by.<kind>.content`:

| level | a row of the trail holds, beside who, what, which row and when |
| --- | --- |
| `fields` | the numbers of the fields the write set |
| `patch` (default) | the above, and the document the write was compiled from -- for an Add, the row it made, as a patch from the empty row |
| `value` | the above, and the row as the write left it |

The default is the patch because the trail is the one table nothing rewrites:
the whole row on every write was a copy of it in the place no correction
reaches, and a write that changed a person's team copied their address in
again. What a write said is what a trail is for, and the row it left is in its
own table. A deployment asks for the row where it has a reason to -- a record
of what a machine did, say -- and the generated `Recorder` is handed the answer
(`config.AuditConfig.Contents`), which is how a setting cannot be read and then
not passed on.

Whatever the level, a field the schema declared `personal` or `secret` is on the
trail as a value at none of them: what it was set to is taken out of the patch
and cleared out of the row, and that it was set is in `Audit.fields`. That is
the schema's to say and not a setting's, because the obligation it answers --
to correct and to erase a person -- is not a deployment's to opt out of.

#### A digest of the row the write left

With `audit.digest.key`, each row of the trail also keeps a keyed digest of the
row its write left (`Audit.digest`, payday#102). It is HMAC-SHA256 of the row
with every field declared `personal` cleared, over the fields the build that
wrote it knew. Those fields are recorded beside it (`Audit.digest_fields`). A
field declared `secret` is covered (payday#115): a keyed MAC of a verifier or a
token's hash is neither a copy of it nor a way to test a guess at it without the
key, and a credential rewritten in the database is the change most worth
finding. The recorder makes it from the row the write handed over, so it costs
no statement; an Erase hands none, and its row is read once, in its transaction
-- the row as the erase stamped it, for one that left it.

**A digest is bound to the line that keeps it** (payday#115). It is two MACs:
one of the row, and one of the line over that -- the line's key, the moment it
was written, its action and the thing it is about. So a digest copied from an
older line onto the newest is not that line's, and a row put back to what it
once was, with the trail made to agree, is a finding. A write that took its row
-- an erase made hard -- keeps the line's MAC alone, which says the row went.

A row is checked against it. The generated `pd.Digests` reads every row of
every kind on the trail -- live, or erased softly, which is a row still --
digests it again, and compares it with the newest line of the trail about it,
in the database or in the archive once it has left (payday#108). Then it goes
the other way: every thing the trail is about that is no row, against what its
newest line says.

```go
rows, err := pd.Digests(ctx, db, contents.Digest, policy)
// rows.Differs: rows that are not what the last write the trail recorded
// left -- changed in the database past the servers, or brought back from
// an erase.
// rows.Forged: rows whose newest line keeps a digest that is not its own.
// rows.NoDigest: rows whose newest line keeps none.
// rows.KeyNotHeld: rows digested under a key the check was not given.
// rows.NotOnTrail: rows nothing on the trail is about -- added past them.
// rows.Missing: things the trail says are rows, that are not -- removed.
// rows.Skipped: the kinds kept off the trail, with why.
// rows.Unsealed: rows from before seals, listed and no finding (below).
// rows.Forgotten: rows that match, whose person was forgotten.
```

Every list is a finding, and `rows.Ok()` is none of them. A page of rows is read
with the trail about it in one read-only transaction, so a write between the
two reads is not a difference. The archive is read through its manifest, as an
export reads it, and only the rows' digests: they are in the clear, so no
tenant's key is opened.

It is keyed because a row of the trail is signed only when a pass archives it.
A digest anybody can compute is one whoever can write the database computes
again for the row they changed. So the key is a file mounted as a secret, as the
checkpoint key is, and kept out of the database and its backups. A digest names
the key it was made under, and `audit.digest.previous` checks the ones made
before a rotation.

**Saying nothing is not an answer.** A line with no digest, and a digest under
a key the check does not hold, say nothing of the row beside them -- and both
are what whoever can write the database makes of any line in one statement. So
each is a finding, and a check handed the wrong key, or of a deployment that
writes with none, finds every row. What a deployment says it cannot check is
said beside the key, where that writer does not reach: `audit.digest.since` is
when it began keeping digests, and a line from before with none is counted
(`rows.BeforeDigests`); `audit.digest.retired` is the fingerprints of keys it
let go, and their digests are counted (`rows.Retired`). The first cannot tell a
row from before apart from one whose whole trail was made to look like it, which
is the price of a deployment that had rows before it had a key: accept those
once, and take `since` out.

**A personal field is the row's seal's.** The digest leaves a field declared
`personal` out: it is kept on a line of the trail, a line is copied to the
archive and kept for years after its row and its person are gone, and a keyed
hash of somebody's address kept that long is something of them that outlives
their erasure. A row of a kind that declares one is sealed instead (payday#115):
a MAC of every field of it, the personal ones among them, kept beside the row in
payday's `RowSeal` table -- in the database alone, never on the trail and never
in the archive, for exactly as long as the row is. The recorder writes it in
the transaction of every write that leaves the row, kept for the commit with
the trail and written with it, a statement for every few hundred; a write that
takes the row removes it, and so does `RecordErased`. Its key is derived from
the digest key, so there is nothing to configure, and it rotates with it.

The check reads it beside the row. A seal is of the write its newest line
records, bound to that line, and the line's digest says, in flags bound into its
MAC, that the write sealed the row. So a personal field changed past the
servers is `rows.Differs`; so is a row whose seal was taken off; a seal put back
from an earlier write is `rows.Forged`. A row whose newest line's write sealed
nothing -- written before this, or before its kind declared the field personal
-- is `rows.Unsealed`: listed, in `rows.Digest`, and no finding, since nothing
can be said of what it was. An acceptance seals those (`pd.Record` seals what it
records) once an operator has looked.

**Forgetting blanks the seal.** `Policy.Forget` puts, in place of the seal of
each row it names, one that says the person was forgotten and nothing of what
they were (`audit.Digest.Forgotten`), whatever a hold keeps of the trail: a hold
keeps the record of what happened, and a seal is no part of it. The row then
matches on what its digest covers (`rows.Forgotten`), until its next write
through the servers seals it again. A tenant leaving takes every seal filed
under it, and a pass that carries a line forward carries its row's seal with it.

So whether a field is personal decides what the trail keeps of it, what the
access record reads, and which kinds are sealed -- not whether a check sees it.

**What nothing accounts for is a finding.** A row somebody added past the
servers is one the trail says nothing about, and a row somebody removed is a
thing the trail says is there. Anything else the check cannot account for would
hide them, and the count would be the one an operator learns to ignore. These
make sure there is nothing else:

- **The archive is read,** for a row whose newest line of the trail has left the
  database. A deployment that discards its trail past the window rather than
  archiving it cannot tell a row nobody wrote since from one somebody added.
- **Before a pass destroys the newest line about a row that is still there**, at
  the end of its window, it carries what that line said of the row onto a line
  of the database's own (`trail.Policy.Digest`, the same key). The row's MAC is
  carried as it was recorded, never made again of the row as it is, so a row
  changed past the servers a year ago still differs. A line with no digest has
  nothing to carry.
- **A row an app writes beneath the servers on purpose is recorded** as the act
  that wrote it -- a workspace restored from its export, rows a migration
  rewrote and an operator accepted. `pd.Record(ctx, db, contents, action, ids)`
  writes one line of the trail for each, in the transaction that wrote them, by
  whoever the context is framed as, keeping the digest of the row as a Get reads
  it and nothing of what it holds. One it removes on purpose -- what a person
  forgotten took with them -- is `pd.RecordErased`, with a digest that says the
  row went. They are the ways onto the trail that are not a write through the
  servers.
- **A kind no server writes says how its writes reach the trail.** One with no
  `Add`, `AddMany` or `Patch` is written by the app's own code, and declares
  which of two it is: `trail: {recorded: {}}`, each write recorded with
  `pd.Record` and the kind checked as any is; or `trail: {off: {why: "..."}}`,
  kept off the trail and not checked -- a session's idle clock, a lock, a queue.
  `pd gen` refuses one that says neither, so leaving a kind out of the check is
  always written down, with a reason, and every check says it again
  (`rows.Skipped`). Whether it has RPCs is a separate question, about its API.
  A kind kept off that holds a `secret` is one whose rows let somebody in, and
  `pd doctor` asks whether that was meant.

A column a migration adds is outside every digest made before it, so the rows
under it still match. A column a migration rewrote is a difference, which is
the point.

#### And per tenant, when the app answers for them

A deployment whose customers keep their history for different lengths of time —
a plan, a contract, a regulated customer — would otherwise have to set every
kind to the **shortest** window it owes anybody. `trail.Policy.Tenants` is a
callback the app hands in, and a pass asks it once per tenant:

```go
p, err := c.Audit.Policy()
p.Tenants = func(ctx context.Context, tenant pdid.Id) (trail.Tenant, error) {
	v, err := contracts.Of(ctx, tenant) // the app's own data
	if err != nil {
		return trail.Tenant{}, err
	}

	return trail.Tenant{Keep: &trail.Keep{Retain: 90 * day, Destroy: v.History}}, nil
}
```

payday stores no plans: what a tenant keeps follows from its contract, which is
the app's data, and where to keep it is
[a guide of its own](guide/operator.md). What payday does is the arithmetic:

- **The most specific answer wins**, a kind before a blanket and the tenant
  before the deployment: the tenant's `By`, then the deployment's `by:`, then the
  tenant's `Keep`, then the deployment's. The zero answer is the deployment's,
  and a kind the deployment named — a machine's record kept forever — stays its
  until a tenant names it too.
- **Floors.** `audit.min` and `audit.by.<kind>.min` are the deployment's own
  obligations, which no contract shortens. A tenant's answer below one is
  raised to it and logged; the deployment's own windows below one are refused
  where the process comes up.
- **A row several tenants may read lasts as long as the longest of them keeps
  it.** A row names up to three tenants — the one it is filed under, the
  actor's, the other party's — and the wall lets each of them read it, so none of
  their contracts ends it for the others. Forever beats any duration. Who may
  *read* it does not change; this decides only how long it lasts.
- **A tenant whose answer fails keeps everything** for that pass, rows it shares
  included, and the failure is logged. Falling back to the deployment's window
  would be a window nobody chose for them.
- **Grace is the app's.** A shortened window applies from the next pass, so an
  app that owes a tenant a grace period answers with the old window until it has
  run out. `Policy.Preview` says what a given answer would take, kind by kind,
  for the warning before and the dashboard after.

#### The archive

`audit.archive` is a directory, and it is a [flob](https://github.com/lesomnus/flob)
store on the disk: **a namespace per tenant**, and three that are not —
`_shared` for the rows that name more than one tenant, each chunk saying which,
`_deployment` for the rows that name none, and `_receipts`. An archive anywhere
else flob reaches, S3 among them, is a `flob.Stores` the app sets as
`Policy.Archive`.

A blob is a **chunk**: the rows of one kind and one month, filed under one
namespace, written whole by one pass. It is still gzip JSONL — a database file
finds one subject fifty to a hundred times faster and is five to nine times the
size, and an archive is written every day and read when somebody asks. Its
labels say the kind, the month, the run and when its oldest and newest rows were
written, which is everything a pass decides from:

- a chunk whose newest row is past its window is **destroyed** — the kind's
  `destroy`, or its `retain` for a kind whose rows are discarded on their way
  out of the database: what a hold sent to the archive instead, or a longer
  window kept there before, lasts no longer there than the discard lets it
  last anywhere;
- once a month can receive no more rows, its chunks are **folded** into one, so
  that a daily pass is not a blob a day per tenant for as long as the archive is
  kept.

A directory a version before this one wrote files into is the same setting. The
first pass **adopts** what is there into `_legacy`, by hard link, and then
**files its rows by tenant**: each row goes to the chunk a pass would have
written it into, and the file is erased once they all have. From then on there
is one layout, and every row is on its own tenant's windows and holds. A file
the pass could not file stays in `_legacy` and is decided by the deployment's
policy, as it was.

Only the first pass does that: the one before the archive has had a
checkpoint, which takes the archive as it is. A file adopted is signed into the
[manifest](#the-manifest) as the deployment's, so after the first checkpoint a
file of that format in the directory is one somebody else put there. No pass
takes it in, files it under anybody or reads it; `Policy.Verify` finds it,
`unaccounted`, and an operator who has looked takes it in with `Policy.Accept`,
as the first pass would have.

Every destruction writes a **receipt** — a discard on the way out of the
database, a chunk destroyed at the end of its window, a purge by hand, a subject
forgotten — saying when, what kind, whose, how many rows and the cutoff, and
never what they said. `trail.Receipts` reads them back. A deployment with no
archive has nowhere to keep them, and has the log.

Destroying a chunk erases its reference, and whether the bytes go with it is the
store's. On a disk they go at once. On S3 they never would on their own, so a
pass also runs flob's reclaim on the archive (`flob.Reclaimer`, with a grace of a
day, `trail.Reclaimed`): a chunk a pass destroyed, forgot or purged is gone from
the bucket two passes later, at least a day apart. On a bucket with Object Lock
the bytes stay for the retention, whoever asks — which is what the checkpoints
want and what an archive that destroys by a window does not, so Object Lock is
for `Policy.Checkpoints` and not for the archive. Backups are the operator's.

#### The manifest

A chunk is named by its digest, so its bytes cannot change without its name
changing. What that does not say is whether the archive still holds **what it
should**: a chunk taken away, or one put there by somebody who is not the
deployment, looks like any other. So the database keeps an account of the
archive — the **manifest**, payday's `Archived` entity, one row per blob —
somewhere the archive's own storage cannot reach.

Every change to the archive goes through it, by the archive every act is handed
(a decorator on the `flob.Stores`), so no act changes the archive without it:

- an add is a row in `adding` before the blob is written, the blob labelled with
  the row's intent, and the row `present` once the store holds it;
- an erase is `erasing` before the blob goes, and `erased` after.

A crash between the halves leaves a row that says what was in flight, and a
pass settles it from what the store holds: an add whose blob is there is
present, and an erase whose blob is still there is **undone**, for the pass to
decide again. So a row in the database alone never makes a pass destroy
anything. Each waits until it is a day old (`date_updated`), since what is
younger may be another pass still at it. The first pass fills the manifest from
what the archive holds.

Two passes that move the same rows write the same chunk, and a chunk is one
blob: the second write finds the first's there and drops its own row, so that
the first pass's rows — and an erase that pass may already be making — are the
whole account of it.

`Policy.Verify` compares the manifest with what the archive holds, and with
`full` reads every blob and checks its bytes against its digest. A blob being
written or erased is in a state that says so, so a pass on another replica half
way through is no finding, and no clock is asked. What it finds:

| | |
| --- | --- |
| `missing` | a blob the manifest says is there, and is not |
| `unaccounted` | a blob that is there, and the manifest never added; or a file of the directory format in the archive's directory after its first checkpoint |
| `relabelled` | a blob whose labels are not the ones the manifest recorded, or a checkpoint whose labels do not say which it is |
| `altered` | a blob whose bytes are no longer its digest (`full` only) |
| `forged` | a row or a checkpoint whose signature is bad or by a key nobody trusts — but not a checkpoint a later one the deployment signed names, whatever signed it (a key rotated out); a blob of the checkpoint store that is neither a checkpoint nor a witness's receipt; or a statement a witness answers whose signature is not good |
| `unsigned` | a row with no signature; a checkpoint with none that no checkpoint the deployment signed names |
| `rewritten` | one of the latest two checkpoints, which the manifest's rows no longer add up to (not the one before an acceptance); or one of two the store keeps under one number, both the deployment's word; or one the store keeps as other than a witness heard, or a number a witness heard as two statements |
| `reordered` | a checkpoint that names one after it as the one before |
| `unkept` | a checkpoint the database numbered, the one after it names, or a witness heard, that the store does not keep; or the numbers a checkpoint skips over to the one it names that no checkpoint the store keeps says it is — taken away, lost to a store restored from an older copy, or numbered by a pass a crash stopped — until an operator accepts |
| `behind` | a checkpoint the store keeps that the database never numbered: a database restored from before it, until an operator accepts |
| `lost` | while the database is `behind`: a blob the newest checkpoint the deployment vouches for says the archive held, and the archive holds no more and the database has no row of — gone since the backup, or taken away by somebody who rolled the database back to hide it |
| `unheard` | the newest checkpoint the store keeps, older than `audit.checkpoints.hear_within`, that no witness that answers heard ([witnesses](#witnesses)) |
| `emptied` | a manifest with no rows beside checkpoints of an archive that held something (a checkpoint over nothing, a deployment's first pass, is not one) |

Every pass starts with the light half — names and labels, no bytes read —
settles what was in flight, says what it found (a warning, and a receipt,
`finding`), and goes on. A verification also answers its findings as one
digest, `Verified.Digest`, which an acceptance of them names, and the latest
acceptances the checkpoints keep, `Verified.Accepted`.

**What an act decides by is the manifest.** A chunk's month, kind and tenants
are the labels the manifest recorded for it, not the ones on the blob, which
whoever can write the archive can change: a chunk relabelled to look old is not
destroyed early, and one relabelled to look new is not kept. A chunk the
manifest does not account for is not destroyed, folded or written again by a
pass or `Policy.Purge` — copying what it holds into a chunk the manifest has
would launder it, and destroying it would destroy what `Verify` found. An act
that must reach it does: `Policy.Forget` and `Policy.PurgeTenant`, since
whatever it is, it holds what it holds about the person or the tenant. They
write it again beneath the manifest, so that what it became is as unaccounted
for as what it was.

**And nothing writes again what is not its digest.** A fold, a subject's
erasure, a tenant leaving and a file filed by tenant copy what a chunk says
into a chunk the manifest accounts for, and a chunk's bytes that are not its
digest any more are what somebody wrote beneath its name. So each reads what
it copies through the digest, and stops at a chunk that is not it: nothing of
it is written, it is not erased, and `Verify` says `altered`. A light check
reads no bytes, so it is the copy that finds it. `Policy.Forget`,
`Policy.PurgeTenant` and `Policy.Purge` go on with the rest and answer which
chunks they could not check; a pass says so in its log.

`Policy.Read` and `Policy.ReadTenant` read the same way — only what the
manifest accounts for, a chunk of `_shared` by the tenants the manifest
recorded for it — and check each chunk's bytes against its digest before a row
of it is handed over; one that is not its digest any more is an error. They are
what an export is made with. `trail.Read` and `trail.ReadTenant` read the
archive as it is and check nothing, for when there is no database to check it
against.

#### Checkpoints

A pass ends with a **checkpoint**, and so does every act that changed the
archive — `trail.Archive`, `Policy.Collect` when it leaves a receipt,
`Policy.Purge`, `Policy.Forget`, `Policy.PurgeTenant` and `Policy.Accept`: the
next number, taken under a row lock so that two never share one, and every blob
the manifest has at that moment, written whole to the checkpoint store, naming
the one before it, and signed. An act that changed nothing takes none, and nor
does a question
(`Policy.Doomed`, `Policy.PlanTenantPurge`, `Policy.Preview`, `Policy.Read`).
Each manifest row says which checkpoints it was in, so `Verify` recomputes the
latest one from the rows and compares — and reads again when a checkpoint was
taken while it read. The number comes before the write, so one the database
numbered and the store does not keep is `unkept` only once it is ten minutes
old.

**What decides is the deployment's word.** Whoever can write the checkpoint
store — the archive's own, by default — can put a checkpoint there, numbered as
they like. A checkpoint counts when its signature is good and by a key the
policy trusts, and so does every one such a checkpoint names as the one before
it, down the chain: it names it by digest, and a checkpoint is read only while
its bytes are its digest, so the one named is what was there when the one
naming it was signed. Only those decide whether the database is `behind`, what
the latest rows are compared with, what the next checkpoint is numbered after
and names, and which numbers an acceptance quieted. Any other is `unsigned` or
`forged`, and that is all it is: one numbered ahead of the database stops no
act, and an acceptance is not numbered after it. It is more in one store
only: one where none counts and one is kept under the database's latest number
— an archive kept without a key until now, below — which takes no checkpoint
until an operator has accepted its history.

**No checkpoint is taken over rows that no longer add up to the latest one.**
`Verify` recomputes the latest two, and with a checkpoint at the end of every
act the latest two are soon others: one taken over rows somebody changed would
put what they changed out of reach two acts later. So while they do not add
up, the next is refused — an act says so in its error, after doing what it
does, and a pass in the log — and `trail verify` finds it as `rewritten` for as
long as it is so. Nor does a pass settle anything meanwhile, or an act trust
the blob of an add in flight by the intent it is labelled with: a row put back
into `adding` with the signature it had is a way to take somebody's blob in.
An operator who has looked accepts, and the acceptance is a checkpoint of its
own, taken over the manifest as accepted and saying why (`Checkpoint.Accepted`);
the ones after it are compared with it, and the one before it no longer is.

What a checkpoint per act costs is one more write per act — the manifest's
blobs, signed, which a device is asked for once more — to a store that keeps
every one, and every act and every verification reads what the store lists. An
act handed one person to forget at a time takes one per person; handed
everybody who is due, one. A process keeps what it read of each — the chain's
part, without the blobs, which it reads again where they are asked for — by
digest, and asks the store only for what it lists and has not read: a blob is
named by its digest and read only while its bytes are it, so what one digest
was read as is what it is. One taken away is what the listing says. One
changed in place, under a name the process read before, is found by a full
verification, which reads every one again, and by any verification in a process
that had not read it — `trail verify`, a process of its own, always; the
serving process's own pass, until it restarts.

That is what covers somebody who can write both the database and the archive.
Their worth is where they are kept: `audit.checkpoints.dir`, or
`Policy.Checkpoints`, on a store that refuses deletion (S3 Object Lock, a WORM
mount) and apart from the archive, is the archive's contents day by day beyond
anybody's reach. In the archive itself (`_checkpoints`, the default) they say
what it held to whoever has not rewritten it. What happens after the latest
checkpoint is [the next part](#after-the-latest-checkpoint), and a key read
into the process is held by whoever holds the process. One in a device is not:
whoever holds the process can have it sign while they do, and cannot take it
away to sign anything after.

What signs is a `crypto.Signer`, `Policy.Signer`: a key read at start from
`audit.checkpoints.key` (`<APP>_AUDIT_CHECKPOINTS_KEY`) — a file, or the PEM
document itself; Ed25519, ECDSA or RSA, as `openssl genpkey` or
`trail.GenerateKey` writes one — or a key in a device, named there by its URI
([below](#keys-in-a-device)), whose private half never comes out. Ed25519
signs a checkpoint's bytes, ECDSA and RSA their SHA-256, and a signature is
verified by the kind of key that made it. `audit.checkpoints.trust` is the
public keys besides the signer's that a verification believes: each replica's
when each has a device of its own, and one rotated out until the next key has
signed a checkpoint and the rows the old one signed are accepted again
([below](#keys-in-a-device)).

**An archive is kept with a key.** `audit.archive` with no
`audit.checkpoints.key` is a process that does not start, and says how to make
one. Every row of the account and every checkpoint is signed, and nothing an
archive holds is decided by anything else: a row or a checkpoint without a
good signature is a finding, and that is all it is. An act asked of a policy
with no key to sign with is refused before it writes anything
(`trail.ErrNoKey`), and a policy given only keys to trust — a verifier —
answers questions and takes no act. There is no development key: one
everybody knew would sign forgeries for anybody, the day it reached a
deployment. `pd new`'s `init` writes a key of the deployment's own beside an
archive that names none, and `pdtest.Key` is a test's own.

An operator who has looked at what verification found and decided the archive
is right — a crash, a blob restored from a backup, an archive kept without a
key until now — calls `Policy.Accept`, and the manifest says what the archive
holds from then on. Accepting a row is signing it, so a policy accepts only
with its signer.

**An acceptance is of what was looked at.** It names the findings by the digest
the verification answered (`Acceptance.Of`), says who accepts and why, and
verifies again first: when that finds anything else it is refused
(`trail.ErrUnreviewed`), so a finding nobody looked at is never taken in beside
the ones somebody did. What signing the rows brings to light — the blob a row
nobody signed said the archive held, compared now — was not looked at either,
and is the next verification's to find. It is an act of the trail's, in the
trail's turn, and kept as one: the checkpoint it ends in keeps who accepted,
why, the digest of what was looked at, how many findings of each kind and which
— the first hundred — signed and named by every checkpoint after it; and a
receipt, `accept`, says the same. That checkpoint is also how a deployment
whose rows no longer add up to the latest checkpoint goes on taking them.

And how a database restored from a backup goes on. One older than the latest
checkpoint has numbered fewer than the store keeps, `behind`, and while it is
no act takes a checkpoint: it would take a number one already has. It is
accepted only as the restore it is, naming the checkpoint the backup holds
(`Acceptance.Restored`), which is the latest the database has numbered — zero
for a backup from before the first. Its own latest checkpoints are from before
the backup, so what was taken away since would look like what the backup never
had, and so would what somebody took away and then hid by rolling the database
back: a verification of a database behind the store also finds each blob the
newest checkpoint the deployment vouches for says the archive held, and it
holds no more (`lost`), and the acceptance of the restore is of those as of
every finding. It is numbered after the newest the store keeps -- by what each
says, whatever its labels -- and names that one as the one before it, so the
checkpoints from after the backup stay in the chain as what the archive held
then, and the database's numbering goes on past them. `Manifest.Checkpoint`
takes that floor, `after`, in the transaction that numbers it.

**The store is read for what it holds.** Every blob the checkpoint store keeps
is read, and is what it holds — a checkpoint, or a witness's receipt of one —
whatever its labels say: a checkpoint with its labels changed or taken off is
as much a checkpoint as before, and still makes the database `behind`, the
archive one that has had a checkpoint, and the next numbered past it. A blob
that holds neither is `forged`, "it does not read". Labels are written for
whoever lists the store by hand, and compared (`relabelled`), and decide
nothing.

#### Witnesses

A checkpoint names the one before it, so the chain below the newest is checked.
The newest itself — the head — is known only to the database's count and to the
checkpoint store, and whoever can write the database and take objects out of
the store can cut the chain short there: take the newest checkpoints away, wind
the count back, and the next act numbers past what is left. Nothing in the two
says so, on a store that allows deletes. A **witness** is somewhere they do not
reach that heard each checkpoint as it was taken (`trail.Witness`):

```go
type Witness interface {
	Hear(ctx context.Context, s Signed) (Witnessed, error)
	Latest(ctx context.Context, chain string) (Heard, error)
}
```

A deployment names one or several in `audit.checkpoints.witnesses`, each by a
scheme registered with `config.RegisterWitness` and linked in by the app as a
signer is (`trail.Policy.Witnesses` in code). Each is told of every checkpoint
as it is taken — its number, what the store keeps it under, the one before it,
and the statement as signed (`trail.Signed`) — and what it answers is kept
beside the checkpoint in the checkpoint store. One that cannot hear does not
stop the trail: the act says so in the log, and the next pass tells it again,
oldest first, along with every checkpoint taken before it was named (a hundred
a pass).

A verification asks each what it heard last (`Latest`) and holds the chain to
every one that answers, saying what each did (`Verified.Heard`):

- a checkpoint a witness heard that the store does not keep is `unkept`,
  "the chain was cut short there" when it is past the newest the store keeps,
  and one the store keeps as something else is `rewritten`;
- the newest the store keeps, once it is older than
  `audit.checkpoints.hear_within` (twice `audit.every` when empty) and no
  witness that answers heard it, is `unheard` — so keeping every witness from
  hearing, or from answering, is no way past them;
- what a witness answers is held to its signature, and one that is not good is
  `forged`: somebody wrote to the witness.

No act takes a checkpoint under a number a witness that answers has heard and
the database has not numbered: the count was wound back, and it would take
the number again. An acceptance is numbered past every one a witness heard, as
past every one the store keeps, and a database behind them is accepted as a
restore (`Acceptance.Restored`).

And below the head. Every witness away while the count is wound back and the
deployment numbers again what was taken away: told it all when it is back, a
witness's newest is the deployment's newest, and the head says nothing. It
heard each of those numbers as two statements, and a witness that can tell
says so (`Heard.Twice`; the store witness does): every number it heard as two
statements or more, each its first and one of the others. Each pair is
`rewritten` at its number, below the head, until an acceptance takes it in
(`Accepted.Twice`) — the pairs it was shown, as findings bound by its digest,
and no other: a rewind left out of what an operator was shown is found after
the acceptance, as one an attacker wound back before a rewind they meant to be
accepted would be, and a third statement of a number taken in is a pair of its
own. Every pair and not one, since the one would be what an acceptance hides
the rest behind.

Two are payday's, each a package of its own:

- **`store:<dir>`** (`config/witnessstore`, `trail/witnessstore`) keeps every
  statement it hears in a flob store of its own — in code, any flob store,
  `witnessstore.New` — and is read by the statements it holds, never by labels.
  Its worth is where it is: on a disk that refuses deletion, or a bucket under
  Object Lock, mounted with credentials neither the database's nor the
  archive's, so that what it heard stays heard whoever holds the deployment's.
  It only ever adds.
- **`log:`** (`config/witnesslog`, `trail/witnesslog`) writes a line of the log
  for every checkpoint, `trail: a checkpoint, heard` with `k`, `digest` and
  `prev`, and records the gauge `payday.trail.checkpoint` at its number. It
  answers nothing (`trail.ErrDeaf`): the monitoring outside is the witness, and
  it is one only with the alerts on it. Set two: one when the gauge goes down —
  `payday_trail_checkpoint < max_over_time(payday_trail_checkpoint[30d])`, or
  your system's spelling of it — and one when the logs hold two lines of one
  `k` with two `digest`s; and a third, if the deployment takes a checkpoint at
  least every pass, when the gauge has not moved for longer than two passes.
- **`rekor:<url>?key=<file>`**, `&v=2` for a log on tiles
  (`config/witnessrekor`, `trail/witnessrekor`) logs every checkpoint to a
  Rekor transparency log. More below.

**Rekor.** What goes to the log is a `hashedrekord`: the statement's digest, a
signature over it by the deployment's key, and the key's public half — never
the statement, which names the tenants whose chunks the archive held. So an
entry says nothing of a tenant, only that the deployment took a checkpoint,
and when; a tenant's chain is never told to a log the deployment names. The
signature over the digest is the checkpoint's own for an ECDSA or RSA key,
which signs SHA-256 of what it signs (a v2 log holds P-384 and P-521 to their
own hashes, and those are signed once more). An Ed25519 key signs once more,
as Ed25519ph (`trail.Prehashed`): the same key for both is safe, since RFC
8032's `dom2` prefix ("SigEd25519 no Ed25519 collisions", section 5.1)
separates Ed25519ph from the pure Ed25519 that signs the statement, so that
neither signature can stand for the other — and a key of the deployment's own
is the entries' worth, where a second key would be one more for an operator to
keep. A key that cannot sign a digest — an Ed25519 key in a device: payday's
device signers sign Ed25519 over the message alone — is refused where the
process comes up, and only when a `rekor:` witness is named; an ECDSA or RSA
key for the deployment, or `store:` and `log:`, instead. `key=` is the PEM file
of the log's key, which signs its trees' heads, and is the deployment's to know:
it is given, never asked of the log.

What the log answers — where the entry is, the proof that it is in the tree,
and the tree's head the log signed — is the receipt kept beside the checkpoint.
An entry carries no number, so the log answers no `Latest` (`trail.ErrDeaf`)
and holds the chain to itself (`trail.Checker`): each receipt is the entry of
its checkpoint, proved in the tree the log signed — which costs the log
nothing, the proof is in the receipt — and the trees it signed go on: the
newest receipt's to the tree the log answers with now, at every verification;
each receipt's to the next, for the newest two at a verification and for all
of them at a full one. A receipt that is none of that is `forged`, a log whose
tree does not go on is `rewritten`, and a log that does not answer is a
witness that did not answer. So a verification asks a v1 log for its tree
(`GET /api/v1/log`), a proof or two (`GET /api/v1/log/proof`), its search
once for each of the deployment's keys and its new entries ten at a time; a v2
log for its tree (`GET /checkpoint`) and the tiles of a proof or two, a few
each. A full verification also asks one proof for each pair of receipts. A
pass adds an entry for its checkpoint and one for each act that changed the
archive, and tells the log again at most a hundred it missed.

v1's search by key (`/api/v1/index/retrieve`) is deprecated, best effort and
may answer short, as Rekor's own API says: what it answers is held to the
store — an entry the deployment's key signed that no checkpoint the store keeps
is, is `unkept`, the head cut short or a count wound back — and what it does
not answer is no "nothing to find". v2 has no search. So on both the head is
the operator's monitor of the log to watch: Sigstore's `rekor-monitor`, say,
set to the deployment's key's fingerprint, which reports every entry the key
signs. Alert when it reports an entry at a time the log witness's gauge did
not move — an entry of a checkpoint the deployment did not take — and when the
gauge moves and no entry follows within `hear_within`. A key is the
deployment's alone: entries are found by it.

**Public Sigstore** is a log anybody may write to, and an operator's choice
to make knowing three things, never a default. Sigstore publishes no rate
limit, only a 99.5% availability objective and a limit of 100 KB an entry.
Whether an audit trail that is not software's belongs there is an open
question (sigstore/rekor#2993, unanswered by its maintainers); in #2798 a
maintainer is reported to have found one summary anchor an interval, rather
than one entry an action, a good way — which is what a checkpoint is: one a
pass, a day unless `audit.every` says otherwise, and one an act that changes
the archive, written by the one process that holds the trail's lease. Use v2
there: v1's search is deprecated, and #2993 reports v1's uploads may be frozen.
And a self-hosted rekor-tiles is cheap to run, and the store witness on a disk
that refuses deletion needs no third party at all.

What a witness hears is a statement in a chain (`Signed.Chain`): the
deployment's checkpoints are the chain named by nothing, and a tenant whose
contract names witnesses of its own is to have a chain of its own — a statement
of its part of each checkpoint, naming its statement before — told the same
way. A witness answers for each chain apart.

#### After the latest checkpoint

Between two checkpoints — which, with one at the end of every act, is while an
act is at it — somebody who can write both the database and the archive can
change the two together, and the next checkpoint would take in whatever they
made them say: a chunk put there with a row to match, a row relabelled so that
a pass destroys its chunk early, a row marked erased and its chunk taken away.
Two things are there for that, and a third that is the storage's:

- **Every row is signed.** Each row of the manifest carries a
  signature by `Policy.Signer` over what it says — which row it is, its
  namespace, digest, intent, state and labels — written with it, in every state
  it is ever in: adding, present, erasing, erased. A row that says anything the
  key did not sign is `forged`, one with no signature `unsigned`, and neither is
  what an act decides by: its chunk is to an act what a chunk nobody wrote is. A
  row is moved along — settled, erased, relabelled — only out of a state the key
  signed, so a row put in the database beside a blob of somebody's does not
  come out of the next pass signed. Not signed are the times, which only decide
  when a pass may settle a row a crash left, and `since` and `gone`, which the
  checkpoints themselves are compared with. The erased rows are read from the
  latest two checkpoints, so an erase nobody signed — a chunk taken away
  beneath a row marked erased to match — is found on the next two passes, as a
  checkpoint the rows were rewritten under is.
- **The checkpoints are a chain.** Each names the one before it by its digest in
  the store. One taken away, or written again as something else, is one the
  checkpoint after it names and the store does not keep, `unkept`, however long
  ago it was written and whatever its labels say; two of one number are
  `rewritten`, and one that names a later one is `reordered`. A checkpoint's
  number is what it says, which is signed, and not what its labels say: one
  whose labels were changed or taken off is found through the chain, and is
  `relabelled`. One a crash numbered and never wrote is gone past, and the next
  names the one before it: the number between is one no checkpoint the store
  keeps says it is, `unkept` until an operator accepts. Two acts taking a
  checkpoint at the same moment can be written the other way round, and then
  both name the one before the first: the store keeps the number between, and
  that is no finding. Checkpoints from before they were chained name nothing,
  and the chain starts after them.
- **The store refuses deletion.** That is the storage's to promise, beneath the
  process. S3 Object Lock in compliance mode keeps every version of every
  object it is given for its retention, whoever's credentials ask — a delete
  only hides one behind a marker, and an overwrite is a new version — so a
  checkpoint hidden or relabelled is still there to be found and recovered. A
  WORM appliance refuses to remove or rewrite a file once it is written. The
  process's half is `trail.WriteOnce`, which an app hands its store in as
  (`audit.checkpoints.write_once` for a `dir`): it never erases a checkpoint or
  changes its labels, and says where the process comes up that its checkpoints
  are on a store that says it is write-once.

  Tried on MinIO (`TestCheckpointsOnS3ObjectLock`, a bucket made with a default
  retention in compliance mode): flob's layout — a blob, then a marker that
  carries its labels — works on it as it is. An erase through flob is a delete
  marker over the latest version, which the chain finds (`unkept`), and taking
  the marker away brings the checkpoint back; a delete of a version is refused,
  whoever's credentials ask; and a relabel is a new version, as locked as the
  first, which is still beneath it. Not tried on AWS S3, whose documentation
  asks a `PUT` of an object with an Object Lock retention for a `Content-MD5` or
  checksum header, which flob sends only when it streams a blob — whether a
  bucket's default retention counts is the first thing to try there.

**A directory promises what its disk does.** On an ordinary disk, whoever holds
the process's user, or root, removes or rewrites any file in
`audit.checkpoints.dir`, and `write_once` keeps nothing from them: it is the
process promising not to, which keeps a pass, a bug, or an erase aimed at the
wrong store off the checkpoints, and nothing more. A WORM mount makes the
promise beneath the process. flob writes a blob and its labels into a staging
directory and moves the directory into place, links the blob beside it, and
relabels by moving a new labels file over the old. That was tried on the
closest thing to a WORM mount a test can make
(`TestCheckpointsInADirectoryThatKeepsWhatItIsGiven`): as root, every file
flob has put in place made immutable and every directory append-only as soon
as a checkpoint is written, the staging directories left as they are.
Checkpoints go on being written onto what was committed, and neither an erase
nor a relabel of one gets through. An appliance was not tried: one that refuses
moving a directory out of the staging area, or commits a file when it is
created rather than when it is closed, is one this layout does not fit.

**An archive kept without a key until now.** A payday before #115 kept one that
way, and its rows and checkpoints have no signature. Given a key, each is
`unsigned`, and none is what an act decides by: a chunk past its window is kept.
Nor does an act take a checkpoint after them, since one would name none of them
and the history before the key would be off the chain for good: every act says
so, after doing what it does, until an operator who has looked accepts, once.
The acceptance signs the rows as they stand and names the checkpoint the
database numbered last as the one before it -- of two, the one the database's
rows add up to -- which takes in, down the chain, every checkpoint before the
key; it is the one place a checkpoint nobody signed is taken in, and only the
database's own, never one ahead of it. Before the database has numbered one,
there is no history to take in, and what the store keeps stops nothing. The
first pass with a key does not do any of this itself: a row with no signature
looks the same as one whose signature somebody took off, and a pass that signed
every row it found without one would sign whatever was put there that way.

**What is still out of reach.** Two kinds of thing.

- **What an act still at it has written**, or one a crash stopped. Its rows are
  after the latest checkpoint until its own is taken, so a blob it added can be
  taken away with its row and nothing says it was there, and a row it moved can
  be put back to a state it was in during the act. And the blob of an add in
  flight is known by the intent it is labelled with, which anybody who can
  write the archive can label a blob with: an add a crash stopped before its
  blob was written can be answered by somebody else's, which the next pass
  takes in. With a checkpoint at the end of every act, that is one act's worth;
  for an act a crash stopped, until the next checkpoint.
- **A row put back to a state signed before the latest checkpoint, where no
  checkpoint says otherwise.** A checkpoint keeps which blobs the manifest had,
  and not each row's state or labels. So a row put back is found when that
  changes what the latest checkpoint's rows add up to — a present row put back
  into `adding`, or taken away — and not when it does not: a row erased before
  the latest two checkpoints, put back as present with the signature it had and
  `since` cleared, which brings its chunk back; the `adding` of a row erased
  before them, which the next pass settles with a blob labelled with its
  intent; a row the checkpoint has, put back into `erasing` with the signature
  of an erase that was refused or undone, which the next pass finishes once its
  chunk is taken away; a present row's earlier labels. Each needs a row's
  signature to say when it was made — the number of the checkpoint it was made
  after, read where the row is written so that none is numbered in between —
  and a checkpoint to keep each row's state, the adds and erases in flight
  among them. That is a column, a change to what a store's writes do, and a
  change to the checkpoint's format, and it is not done.

And a key read into the process is the process's: whoever holds the process
can have it sign what they like while they do, and with a key from a file,
after.

#### Keys in a device

A key in a device is named in `audit.checkpoints.key` by a URI, once the app
links in what speaks to the device. Each is a package of its own that the app
imports, as a database driver is, so that a binary carries the one client it
uses and none of the others:

| | `audit.checkpoints.key` | the app imports | signs with | tried against |
| --- | --- | --- | --- | --- |
| a PKCS #11 token | `pkcs11:token=payday;object=checkpoints?module-path=…&pin-source=…` | `config/signerpkcs11` | Ed25519, ECDSA P-256/384/521, RSA | SoftHSM 2.6 |
| a TPM 2.0 | `tpm:0x81000100` | `config/signertpm` | ECDSA P-256 (P-384/521), RSA 2048 (3072) | swtpm 0.7; an AMD fTPM, a key made for the run |
| Vault's Transit | `hashivault://checkpoints` | `config/signervault` | Ed25519, ECDSA P-256/384/521, RSA | Vault 2.1, OpenBao 2.7 |
| AWS KMS | `awskms:///alias/payday-checkpoints` | `trail/trailawskms` | ECDSA P-256/384/521, RSA | local-kms |

- **PKCS #11** is an HSM on the network or in the machine, a cloud's HSM
  through the library its vendor ships — AWS CloudHSM, Google Cloud KMS
  through `kmsp11`, Thales Luna, YubiHSM — or a smart card. The URI is RFC
  7512's: the token's label, the key's label or `CKA_ID`, the library, and the
  file the PIN is in. It loads a C library, so it needs cgo; built without, it
  refuses at start rather than not building.
- **A TPM** is the machine's, or a virtual machine's vTPM. The key is made in
  it and kept at a persistent handle (`trail/trailtpm` has the two
  `tpm2-tools` commands), reached through the kernel's resource manager,
  `/dev/tpmrm0`. P-384, P-521 and RSA 3072 are there where the TPM has them;
  Ed25519 is in no TPM.
- **Transit** is Vault's, or OpenBao's, over HTTP and no SDK. The address and
  the token are where Vault's own tools read them, `VAULT_ADDR` and
  `VAULT_TOKEN`; a Vault Agent's token file (`token-file=`) or its proxy is
  what keeps a token alive for longer than it lives. The address is
  `https://`, but for a Vault on this machine — `localhost`, a loopback address:
  a dev server, an agent's proxy beside the process — since across a network
  plain `http` would carry the token, and the key's public half the process
  trusts, in the clear. The version that was latest at start is the one that
  signs, until the next start.
- **KMS** is its own module, `github.com/lesomnus/payday/trail/trailawskms`,
  so the AWS SDK is in no `go.mod` but its own, and the app registers it with
  one line, `config.RegisterSigner(trailawskms.Scheme, trailawskms.Open)`.
  Credentials and the region are the SDK's to find. An alias is resolved at
  start. KMS's Ed25519 keys are refused: KMS signs a message itself of 4 KiB
  at most, and a checkpoint is longer.

**What "cannot give the key away" means** is each device's own guarantee, and
a key that does not have it is refused at start rather than trusted for one:

| | refused | so whoever holds the process |
| --- | --- | --- |
| PKCS #11 | `CKA_EXTRACTABLE`, or not `CKA_SENSITIVE` | holds a session, and the token gives the key out to nobody, in the clear or wrapped |
| TPM | not `fixedTPM` and `fixedParent` | holds the device, and the key cannot be duplicated out of the TPM, so it is nowhere else |
| Transit | `exportable`, or `allow_plaintext_backup` | holds a token — give it `update` on `transit/sign/<key>` and `read` on `transit/keys/<key>` and nothing else — and Transit hands the key to nobody |
| KMS | — | holds credentials, and KMS hands a key's private half to nobody |

In every case that is the bargain [checkpoints](#checkpoints) make: whoever
holds the process can have the key sign while they hold it, and cannot take it
away to sign anything after, so a compromise cleaned up is over rather than a
key to rotate and a trail to doubt. What it does not cover is the device's own
administrators — Vault's with its storage and unseal keys, an HSM's security
officer — and a key that was made outside and imported, which the device
records (`CKA_LOCAL` on a token, KMS's `EXTERNAL` origin; a TPM key that is
`fixedTPM` was made in the TPM) and which is the operator's to decide about.
Make the key in the device.

**A device is made to sign once at start**, the way a checkpoint is signed and
verified the way `Policy.Verify` verifies one (`trail.TrySigner`), so a PIN that
is wrong, a mechanism the token lacks, or a public half that is not the private
half's is a process that does not start rather than a checkpoint called
`forged` a day later. **And every signature is verified** against the public
half the device gave at start before it is handed over: a key made again under
the same name, a persistent handle given another key, a token whose objects
disagree, each fails to sign rather than signing what nobody can verify.

**A device signs every row an act writes**, as a key from a file does
([above](#after-the-latest-checkpoint)), and not only the checkpoint a pass
ends with. One across the network — Transit, KMS, an HSM on the network — is a
request a row: a pass that moves many chunks takes as long as that many
requests, and for KMS counts against its request quota and its bill.

**What each was tried against** is the table's last column, and it is
emulators but for one chip: an AMD fTPM signed through `trailtpm.New` with a
key made for the run and flushed after it, so no persistent handle was opened
(#62). A device's first start is the first time it is asked, and the try at
start makes what goes wrong there a process that does not start; before one
is relied on, a scratch archive taken through a pass and `Policy.Verify` says
the rest. Three have not been asked at all: **KMS on P-384 or P-521**, whose
digest goes padded as `trailawskms` says, which local-kms signs and KMS itself
has not been given; **an HSM on the network**, which is also where a request a
row is felt; and **a YubiKey as the master**, since the plugins were tried
with one the tests make.

A key rotated is a new public half. Keep the old one in
`audit.checkpoints.trust` until the next key has signed a checkpoint — the
first names the last one the old key signed, and with it every one before —
and an operator has accepted the rows the old key signed, which signs them
again; then take it out. The checkpoints it signed are what the chain says they
were without it, and what it signs after that is `forged` and decides nothing:
a key rotated out because it leaked stops vouching for anything. `trust` reads
PKIX, as base64 or PEM — what `tpm2_readpublic -f pem`,
`pkcs11-tool --read-object --type pubkey` (DER, so through `base64 -w0`) and
`aws kms get-public-key` write, and what Transit's `keys` endpoint answers for an
ECDSA or RSA key. A device of another kind is an app's own `crypto.Signer`,
registered under a scheme of its own with `config.RegisterSigner`. Google Cloud
KMS and Azure Key Vault are not here yet, for want of something to try them
against; the first is reachable through PKCS #11 today. Nor, for want of
somebody who needs them, are a TPM key whose use is a policy, or that is kept
in a TSS2 key file; a TPM on Windows named by `tpm:` (TBS, which `trailtpm.New`
reaches through what go-tpm's `windowstpm` opens); and Transit asked with
anything but a token: a client certificate (`VAULT_CLIENT_CERT`),
`VAULT_CAPATH`, an auth method of Vault's own (#63). The device tests run at a
desk with what CI's `signers` job starts: each skips itself without its
emulator, and its file says how the emulator is started.

#### Sealed contents

Encryption at rest is the storage's — S3's server-side encryption with a KMS
key, an encrypted disk — and it covers what most requirements ask of it; the
manifest is what says the archive is what it was. What it cannot do is let a
tenant that leaves take its trail out of the archive's **backups**, which are
kept as long as the archive is and which no purge reaches. `trail.Keys`
(`audit.keys`) is for that:

- **Only what a row says is sealed** — `value` and `patch`, together, row by
  row, with [age](https://age-encryption.org), in a member of their own,
  `sealed` (`trail.SealedMember`). It is no field of payday's, so `pd gen`
  refuses a field of `Audit` or `Access` named or JSON-named that: a row with
  one would read as sealed already. Who acted, on what, when and whose it is
  stay in the clear, because every act on the archive decides by them: a pass
  folding a month, a subject's erasure, a tenant leaving, a hold that keeps
  part of a file. None of them needs a key; only reading what a row
  said does. The cost is the archive's size, since the contents no longer
  compress together.
- **A key per tenant, and a master.** A row is sealed to the key of every tenant
  it names — the ones that may read it, the wall's rule — and `_deployment`'s
  for a row that names nobody. Each tenant's key is an X25519 identity, made by
  the process that first has a row of theirs to seal and kept **sealed to the
  master** in a key store of its own (`audit.keys.dir`, or any `flob.Stores` as
  `Keys.Store`). The process that writes the archive holds public keys only:
  the tenants', in the key store's labels, and the master's, which is
  configuration. The master's private half, `Keys.Identities`
  (`audit.keys.identity`), is given to a process that reads what rows say — an
  export, an operator at a shell — and to no other.
- **The master may be in a device**, as age has one: a plugin, `age-plugin-<name>`
  on PATH — age-plugin-yubikey for a YubiKey's PIV slot, age-plugin-tpm for a
  TPM. Its recipient (`age1yubikey1…`) is the master, and the process that
  serves seals to it by running the plugin, which needs no device to seal; its
  identity (`AGE-PLUGIN-YUBIKEY-1…`) is what a reading command is given, and
  opening takes the device and whatever it asks the terminal for, a PIN or a
  touch. So the master's private half is in no file at all. The master is sealed
  to once where the process comes up, and a plugin that is not on PATH, or
  will not seal, is a process that does not start.
- **A tenant leaving takes its key first.** `Policy.PurgeTenant` destroys the
  tenant's keys before anything else, writes the receipt for it at once, and
  then its rows. From then on what was its alone cannot be read from any copy:
  the archive's backups, a file somebody saved. A row it shared stays readable to
  the other side, whose evidence it is too — which is also what keeps a row
  another tenant's hold is on readable after this one has gone. Grace keeps a
  key, because nothing before the purge touches it; a hold refuses its
  destruction, because a held tenant is not purged; a window that destroys every
  chunk of a tenant leaves its key, since the tenant is still there. A tenant
  that retires (below) keeps its key until the last of what its floors are on
  has gone, and a pass destroys it then.
- **And leaves a mark where its key was**, saying when. A key is made by the
  process that first has a row to seal, so no key is no answer: a row written
  before the mark — the other side of a transfer, or one a pass read a moment
  before the purge — would be sealed to a key made for it again. So a row
  written before it is sealed to the tenants it names that are still there,
  and one that names nobody still there is not archived at all; one written
  after it is a tenant that is back, with a key of its own. A key a pass makes
  as the tenant leaves is found under the mark, which comes before the keys go,
  and goes too.
- **Reading.** `Policy.Read` and `Policy.ReadTenant` hand a sealed row over
  opened, given the master; without it a sealed row is `trail.ErrSealed`, and
  one whose every key is gone is `trail.ErrKeyGone`. `trail.Read`,
  `trail.ReadTenant` and `trail.ReadFiles` take no key and refuse a sealed row;
  `Keys.Open` opens one row, for a file taken out of the archive.

The key store's **backups are what decide** how long after a tenant leaves its
rows can still be read, so they are kept short, and it is not inside the
archive's directory, which would back it up with the archive. Whoever can write
the key store can put a key there that rows will be sealed to, as whoever can
write the archive can put rows there: it is trusted as the archive is, and the
manifest does not cover it. Nothing changes for an archive with no keys, and a
chunk written before them stays as it was; the rows a pass files from `_legacy`
are sealed on the way. The database's half of the trail — the `retain` window —
is not sealed.

#### Legal holds

A tenant's answer can say it is under a **hold**, `Tenant.Hold`, on everything
or on some kinds. It is apart from the windows because lifting it has to bring
them back, and a hold written as a window of forever would be one nobody could
lift without remembering what the window was.

While a hold is on, nothing the tenant may read stops existing — and a row it
shares with another tenant is one it may read:

- a pass still **moves** its rows out of the database on their window, into the
  archive even when the window says discard, and keeps them in the database
  when there is no archive; what it does not do is discard or destroy. Once the
  hold lifts, what it sent to the archive goes when the discard would have
  taken it;
- `Policy.Collect` and `Policy.Purge`, the acts an operator takes by hand,
  leave what is held and answer with it. They go ahead for everybody else,
  because refusing would keep every other tenant's trail past what they are
  owed. A file of everybody's from before the archive was a flob store is
  written again with what is held and nothing else;
- `Policy.Forget`, a subject's erasure, leaves the held rows as they are and
  answers with them and whose holds they are. A legal claim overrides an
  erasure request — GDPR Art. 17(3)(e), and the exception in 개인정보보호법 §21
  for what another law requires to be kept — and the caller erases again when
  the hold lifts;
- `Policy.PurgeTenant` refuses, with `trail.ErrHeld`.

A tenant the app gives no answer for is held by all of these, for the reason a
pass keeps its rows: nothing is destroyed on a guess. A hold on one subject — a
person, an asset — inside a tenant that is not held is not here yet.

#### A tenant leaving

`Policy.PurgeTenant` takes a tenant's trail out, and `Policy.PlanTenantPurge`
says what it would take. It is a function and not a schedule: **when** is the
app's — at once, on a request or a legal demand, or after the grace an
offboarding gives.

- What was the tenant's **alone** goes: its rows in the database, its
  namespace in the archive, its rows in the files from before.
- What it **shares** stays, as an event with its contents gone, because it is
  the other tenant's evidence as much as its own. One it shares with a tenant
  under a hold stays exactly as it is.
- What is **filed under another tenant** and only names this one — the other
  side of a transfer — is the other tenant's, and is not touched.

It refuses a tenant under a hold and one the app has no answer for, goes round
again until a round finds nothing (a pass can write a chunk after the
namespace was erased), and writes a receipt.

**Or it retires**, `Policy.RetireTenant` (`Policy.PlanTenantRetire` says what it
would take), which is a purge but for what a floor of the deployment's is still
on. The floors — `audit.min`, `audit.by.<kind>.min` — are what the deployment
owes whoever it answers to, the access record Korea's PIPA has a processor
keep for a year among them, and a purge takes that with the tenant. Which of
the two is the operator's, by the contract or the law, and not payday's.

- What goes **at once** is what a purge takes, of every kind under no floor,
  and of every row whose floor has already passed.
- What **stays**, as it is: a row of a kind under a floor that is still on it
  — the floor's longer clock, from when the row was written — in the
  database, in the archive (a chunk whose newest row is under one), in the
  files from before, and the contents of what it shares. So does the tenant's
  key.
- **The passes** take the tenant to keep nothing of its own, which the floors
  raise: each kind's rows go once its floor has passed, whatever the
  deployment's window for it. Its contract is not asked; its hold is, and
  holds as it would.
- **Once the last floor has passed** — `TenantPurge.Until`, by the floors as
  they are then — a pass purges what is left, as `Policy.PurgeTenant`: a chunk
  that held rows of both sides of a floor, the contents of what it shared, and
  its key.

The record that it retired is a blob of `_retired`, written through the
manifest and signed, so that a pass knows a tenant has left whether or not the
app still answers for it, and nobody can make one think so that has not. So a
tenant retires into the archive: with floors and no archive it is refused. Its
receipts are `retire-tenant`, saying what went, what was kept of which kinds,
and until when.

**And it can come back.** An app that puts a tenant back under the identifier
it had — a workspace restored from its export into the deployment it left —
puts back a tenant the passes still take to have left: what it writes goes at
the next pass, and the whole of its trail at the end of its floors. So the app
asks `Policy.Retired` first, the tenants whose records the manifest accounts
for and when each retired, and takes the tenant back with `Policy.Return`
before it writes anything of it. Return erases the records, through the
manifest and signed, in the trail's turn, with a receipt `return-tenant` and a
checkpoint after; from then on the tenant is the app's to answer for, and what
its retirement kept, and its key, are its own. A purged tenant needs nothing:
the mark its key left is of what was written before it left, and what it
writes after is sealed to a key of its own, so Return leaves the mark where it
is and does nothing.

**And what it left can be taken out.** Nothing holds a retired tenant's trail
past its floors: a hold is the app's answer for a tenant, and an app that
destroyed the tenant's rows has none to give. So a demand to preserve what one
left, while it is still there, is answered by writing it out, for keeping
somewhere the deployment does not reach: `Policy.ExportTenant`. `Policy.Retired`
says, of each retired tenant, when it retired and until when its floors keep
what they keep (`Retirement.Until`, by the floors as they are), which is
whether there is anything to take out. It works for a live tenant as well.

A bundle is every row that names the tenant, wherever it is: the database's
rows a page a file (`database/`), each chunk of its own namespace as the
archive keeps it (`archive/<namespace>/<digest>`), and each chunk's rows of the
tenant opened (`rows/`) — its own, and of a chunk it shares or a file from
before, its rows alone, so nothing another tenant's rows said is in it. It is
written encrypted to whoever it is for, `age` recipients, and never otherwise:
there is no clear copy on any disk. Sealed rows are opened as it is written,
which takes the master's private half; whoever it is for reads it with their
identity alone. Last comes `manifest.json`: every file's digest and size, the
chunks read, and the newest checkpoint the deployment vouches for — its
number, its digest, and every witness's receipt of it — with whether each
chunk is one it lists, signed with the deployment's key.

`trail.OpenTenantExport` holds a bundle to its manifest — every file there,
each its digest, nothing more — and the manifest to a key the deployment
trusts. That says the deployment wrote it, and no more. What says the archive
held those chunks before anybody asked is the checkpoint: read it later from
the checkpoint store's write-once copy by its digest, check its signature and
number, find each whole chunk's digest among its blobs, and hold it to each
witness's receipt as that witness says to. The rows that were still in the
database are newer than any checkpoint, and only the manifest's signature
vouches for them.

It hands people's data out of the deployment, so it is an act of the trail's,
in the trail's turn, with a receipt, `export-tenant`, saying who took it out,
why, and the manifest's digest, and a checkpoint after. The app records it
too, as it records any download of people's data, with its reason: nothing
here knows who a row is about.

#### A pass and what erases take turns

A pass holds what it read of a row from the moment it reads it until the
chunk it copies the row into is written and the row is forgotten. An erasure
that blanked the row in that time, and then looked through the archive before
the chunk was there, would have its work undone by the copy — and so would a
tenant's purge, and a discard by hand. So they take turns, on the
deployment's lease, `Policy.Lease`, under a name of their own (`trail.Turn`):

- a pass holds the turn for each chunk's worth of rows it moves, from the first
  read to the forgetting, and gives it back between, so that an act waits for a
  chunk and not for a pass;
- `Policy.Forget` and `Policy.PurgeTenant` change the database's rows in their
  turn, and look through the archive after it, where a pass that held the rows
  has put them by then; `Policy.Collect` discards in its own. `Forget` inside
  an app's transaction takes no turn; see below.

#### An erasure decided in a transaction is owed, then paid

An app decides an erasure in a transaction of its own — a person
pseudonymized, a membership ended — and the trail's erasure belongs in it.
But the archive takes no part in a transaction: an act that wrote it as it
went, then rolled back, would leave the archive after an erasure that never
happened, the manifest no longer its account, and a checkpoint the database
never numbered (`behind`, which stops every act until an operator accepts).
Written after the commit by the process that committed, a crash in between
would lose the archive's half without a word.

So `Policy.Forget` on a store inside a transaction (`Store.InTx`) blanks the
database's rows there and writes one row of payday's `Owed` per object, in the
same transaction: the archive's half and the rows' seals, owed. It writes
nothing the archive keeps, and `Forgotten.Owed` says so. A rollback takes the
owed rows with the rest; a commit keeps them for whatever pays them:

- **`Policy.Pay(ctx, s, objects...)`**, for an app that wants the archive's
  half at once, right after its commit, naming the objects it forgot: only
  their debts are paid, and what it answers is that erasure's. Naming none
  pays every one, which is the pass's — a debt a hold keeps would be gone
  through again, an archive's walk apiece, by every payment after every
  commit;
- **the next pass**, before it moves anything — a row moved into a chunk first
  would be one more copy of what is owed to be forgotten.

Paying forgets each object in both halves, writes `Forget`'s receipts, removes
the owed rows that are done and ends in a checkpoint. A row is removed only
once both halves are done and their receipts are written: blanking a blank row
changes nothing, so a process that stops part of the way leaves it for the
next pass, and paying twice is paying once. One whose archive's half could not
be done — a chunk whose bytes are not its digest — stays owed. Outside a
transaction `Forget` owes and pays at once, so its own crash is covered too.

**Inside a transaction, in no turn.** The turn keeps a pass and an erasure
from each writing what the other read: a chunk a pass made of rows an erasure
then did not find in the archive, and a seal a pass carrying a line forward
writes back. Taken inside a transaction it keeps neither apart, because it is
given back before the commit: a pass taking it then still reads the rows and
the seals as they were, and writes what it read once the transaction commits.
And it is a wait a pass can be inside of — a pass in its turn waiting on rows
the transaction blanked, and the transaction, forgetting a second thing,
waiting for the turn — which no deadlock detector sees, since the turn is none
of the database's locks: a transaction and a pass waiting on each other for
ever. So `Forget` takes no turn there, and does there only what no pass writes
back. The database's rows are blanked, which a pass never writes back: it
copies rows into a chunk and removes them, and the chunk is the archive's
half. The archive's half and the seals are owed, and paid after the commit in
a turn of their own. A pass holds its turn from reading a chunk's rows until
the chunk is written, so the payment comes after any chunk made of the rows
as they were, and finds it; a carry holds its turn while it reads and writes
a seal, so one before the payment is blanked by it, and one after carries the
forgotten seal forward as forgotten. A pass and a transaction blanking the
same rows wait on each other as any two writers of a row do, which the
database sees and settles.

**A hold keeps a debt.** An owed erasure a legal hold keeps part of stays
owed, the rows the hold is on kept as they were, and says whose holds
(`Owed.held_by`, `Policy.Owing` by tenant). The pass that finds the hold lifted
pays the rest, and nobody has to ask again.

**Every other act is refused inside a transaction**, with `trail.ErrInTx`,
before it writes anything: a pass, `Archive`, `Collect`, `Purge`,
`PurgeTenant`, `RetireTenant`, `Return`, `ExportTenant`, `Accept` and `Pay`.
What has to happen in an app's transaction is the erasure, and only that is
taken there. Questions — `Verify`, `Doomed`, `Preview`, `Read`, the plans,
`Retired`, `Owing` — are answered, since what they write, if anything, is the
database's alone.

What a pass does inside the archive — a fold, a file filed by tenant — is
the erasures' rounds to answer, as it was. It is not the loop's lease,
`payday.trail`, which one process holds for as long as it runs the passes: an
act in any process takes the turn for as long as it changes rows. With no
lease nothing takes turns, and the process says so where it comes up; a lease
that holds nothing (`none`) is no lease for this, so a process that runs no
loops acts without one. `pd new`'s `Build` hands the policy the lease
`spin.lease` names.

### The access record (#111)

The trail is what was **written**. A deployment that processes personal data is
also asked who **saw** it: Korea's PIPA safety measures (art. 8) want a record of
each access -- the account, when, from where, whose data, and what was done, a
read, a write or a download -- kept for a year or two, reviewed every month, with
a reason confirmed for each download. The trail cannot be that. It has no reads
and no caller's address, and its object is the row that changed rather than the
person a screen showed: a list of a hundred people is a hundred people and no
write at all.

So it is a table of its own beside the trail: `Access`, payday's entity, copied
into every app, domain 6. A row is what one call did to somebody's personal
data, filed under the tenant that data was in: who called (`actor_id`,
`actor_tenant_id`), `action` (the RPC), `kind` -- `write`, `download`, or `read`
for anything else it carried -- `result` (`ok`, or the code it was refused
with), when, `subjects` (the keys of whose rows, a thousand at most, and
`subject_count`), and `value` -- where the call came from, what a read or a
download asked for, a download's reason -- which is what the archive seals. Of
a request it keeps a read's and a download's, as its message declares it and
`access.MaxRequest` bytes at most, and nothing of a call that wrote, whose
request is the write and on the trail, or of one refused, whose request is
what it tried and somebody's data as often as not. A
call that wrote one person's data and showed another's is a row of each.

**Whose data a call carried is read off the declarations, not written by each
app.** A message holds somebody's data when it holds a field declared
`personal` (#92), and its key is whose; the generated `pd.Subjects` reads any
answer that way -- a `Get`, a list of a hundred, an edge two hops into a select,
a batch's packed answers -- so an RPC an app adds is recorded without anybody
saying what it shows. An `Any` is read only where payday packs one -- a batch's
operations and answers, a watch's events -- and once: one an app's rows hold is
its own data, which no declaration describes, and a message packed in one
packed in another is work that grows with the square of its size. What no declaration can see, the app says:
`access.Saw(ctx, ...)` for a name put into a sentence or a column of a file. A
write is the rows it wrote, before or after it (`pd.AccessRecorder`, which asks
`bare` for those images); a refused call is the rows its request named by key. A
call that carried nobody's data leaves nothing.

**When a row is written** depends on what can be lost:

- a **write** is recorded in its own transaction, as the trail is: at its
  commit where the transaction keeps one, as the write is made where it keeps
  none. What holds the write holds its record, a rollback takes both, and a
  write whose row cannot be written is refused. The call says the row once it
  has answered: a commit refused after the row was written takes it along, and
  the call that ends in that refusal writes it again as the refusal;
- a **download** -- a method the app names in `access.Config.Downloads` -- says
  what it is for in the `payday-reason-bin` header or is refused, and its row
  is written before its answer leaves; one that cannot be recorded is refused.
  The reason is a sentence of any text, `access.MaxReason` bytes at most: a
  `-bin` key is base64 on the wire, which is what lets a browser send words a
  plain header cannot carry. A download is called by itself, and a batch
  refuses one as an operation;
- a **read** is queued and written a second later with the others of its
  second (`access.Writer`, a loop each process runs for itself). A page is a
  dozen reads, and a statement in front of each would be the cost. The queue is
  the one loss: a process killed outright loses its last second of reads, and
  one stopped writes what it holds before it goes, and again once its server
  has stopped. A stream's reads are queued
  as it sends, so a watch open for an hour is a record of that hour.

**It is kept by the trail's machinery**, not a copy of it. The generated
`TrailStore` reads both tables, so access rows leave the database on a window,
are archived, sealed, checkpointed, destroyed, and leave with their tenant, as
a kind of their own, `access`:

```yaml
audit:
  by:
    access:
      profile: pipa-sensitive   # two years
```

A person's erasure leaves it alone. The record names them by key and nothing
else, and keeping it is what the law that asks for it requires (개인정보보호법
§21's exception); their name is gone from the row the key points at.

**What payday does not decide**: which methods are downloads, which addresses
to believe behind a proxy (`access.Config.Address`; `access.Peer` is the
connection's), who may read the record (the generated `AccessService` is `Get`
and `List` behind the wall, and the policy says who; it is no operation of a
batch), and the monthly review.

## 7. What payday does not do

Some of these are gaps and some are decisions. The difference is written down
because a reader cannot tell them apart.

**Decided against:**

- **Deciding when one person leaves the trail.** The retention policy above is
  about **age** and reaches everybody's rows at once. A right-to-erasure request
  is about a subject — *which* rows, and *when*, is what an app owes somebody
  under a regime payday cannot know. Most of it is answered before the request
  arrives: a field declared `personal` is never on the trail as a value (#92),
  so what an erasure would have blanked of a person's name and address was
  never written there. What payday supplies for the rest is the half with no
  judgement in it but one: `trail.Policy.Forget` blanks `value` and `patch` for
  a set the caller chose, in the database and in the archive in one call —
  a mechanism that stopped at the database would destroy the copy an operator
  can see and leave the copy in the archive beside it — and leaves the rows a
  legal hold is on, answering with them. The rest is what no declaration
  reaches: free text, rows written before #92, and a field nobody declared on a
  kind kept at `value`.

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

**Background work that crosses replicas** is `config/leasepg`: `spin.lease:
postgres`, an advisory lock on the rows' own database, so one process of the
deployment runs each loop; see [one process of many](#one-process-of-many-54).

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
- [schema.md](schema.md) — what a declaration is allowed to say
- [tenancy.md](tenancy.md) — the wall, in full
- [client.md](client.md) — the browser half
