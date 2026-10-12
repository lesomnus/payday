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
- **A lost hold is not a failure.** The loop's context ends, and the process waits
  to hold the lease again; shutting down while waiting is an ordinary stop.
- **The leases.** `local` holds everything in this process, for a deployment of
  one; `none` holds nothing, for a process that only answers requests; `postgres`
  (`config/leasepg`, `spin/spinpg`) holds a session advisory lock on the app's
  own database — no table, no clock, released when the session ends — on one
  connection per process that asks for TCP keepalives of its own and pings what it
  holds. It does not go through a transaction pooler, for the reason `LISTEN`
  does not; `spin.dsn` is for a direct address.

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

- a chunk whose newest row is past its window is **destroyed**;
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

Every destruction writes a **receipt** — a discard on the way out of the
database, a chunk destroyed at the end of its window, a purge by hand, a subject
forgotten — saying when, what kind, whose, how many rows and the cutoff, and
never what they said. `trail.Receipts` reads them back. A deployment with no
archive has nowhere to keep them, and has the log.

Destroying a chunk erases its reference, and whether the bytes go with it is the
store's. On a disk they go at once. On S3 they never would on their own, so a
pass also runs flob's reclaim on the archive (`flob.Reclaimer`, with a grace of a
day, `trail.Reclaimed`): a chunk a pass destroyed, forgot or purged is gone from
the bucket two passes later, at least a day apart. Backups are the operator's.

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
| `unaccounted` | a blob that is there, and the manifest never added |
| `relabelled` | a blob whose labels are not the ones the manifest recorded, or a checkpoint whose labels do not say which it is |
| `altered` | a blob whose bytes are no longer its digest (`full` only) |
| `forged` | a row or a checkpoint whose signature is bad or by a key nobody trusts |
| `unsigned` | a row with no signature when the policy names a key; a checkpoint with none after one that had one |
| `rewritten` | one of the latest two checkpoints, which the manifest's rows no longer add up to; or one of two the store keeps under one number |
| `reordered` | a checkpoint that names one after it as the one before |
| `unkept` | a checkpoint the database numbered, or the one after it names, that the store does not keep |
| `behind` | a checkpoint the store keeps that the database never numbered |
| `emptied` | a manifest with no rows beside an archive with checkpoints |

Every pass starts with the light half — names and labels, no bytes read —
settles what was in flight, says what it found (a warning, and a receipt,
`finding`), and goes on.

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

`Policy.Read` and `Policy.ReadTenant` read the same way — only what the
manifest accounts for, a chunk of `_shared` by the tenants the manifest
recorded for it — and check each chunk's bytes against its digest before a row
of it is handed over; one that is not its digest any more is an error. They are
what an export is made with. `trail.Read` and `trail.ReadTenant` read the
archive as it is and check nothing, for when there is no database to check it
against.

#### Checkpoints

A pass ends with a **checkpoint**: the next number, taken under a row lock so
that two passes never share one, and every blob the manifest has at that
moment, written whole to the checkpoint store, naming the one before it, and
signed when the deployment gives it something to sign with. Each manifest row
says which checkpoints it was in, so `Verify` recomputes the latest one from
the rows and compares — and reads again when a checkpoint was taken while it
read. The number comes before the write, so one the database numbered and the
store does not keep is `unkept` only once it is ten minutes old.

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
public keys besides the signer's that a verification believes: one rotated
out, or each replica's when each has a device of its own.

An operator who has looked at what verification found and decided the archive
is right — a crash, a blob restored from a backup, rows from before the
deployment had a key — calls `Policy.Accept` with why, and the manifest says
what the archive holds from then on. Accepting a row is signing it, so a policy
that names a key accepts only with its signer.

#### After the latest checkpoint

Between two passes, somebody who can write both the database and the archive
can change the two together, and the next checkpoint would take in whatever
they made them say: a chunk put there with a row to match, a row relabelled so
that a pass destroys its chunk early, a row marked erased and its chunk taken
away. Two things are there for that, and a third that is the storage's:

- **Every row is signed.** With a key, each row of the manifest carries a
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
  names the one before it, so it is no finding once the next is written.
  Checkpoints from before they were chained name nothing, and the chain starts
  after them.
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

**A directory promises what its disk does.** On an ordinary disk, whoever holds
the process's user, or root, removes or rewrites any file in
`audit.checkpoints.dir`, and `write_once` keeps nothing from them: it is the
process promising not to, which keeps a pass, a bug, or an erase aimed at the
wrong store off the checkpoints, and nothing more. A WORM mount makes the
promise beneath the process. flob writes a blob into a staging directory and
moves it into place, which has not been tried on one.

**Without a key** nothing is signed and nothing is held to a signature, as
before: the rows are covered up to the latest checkpoint and by nothing after
it, and the process says so where it comes up. The checkpoints are still
chained, which is worth what the store they are on is.

**Rows from before.** A row written before rows were signed, or while the
deployment had no key, has no signature. With a key each is `unsigned`, and not
what an act decides by, until an operator who has looked accepts it with
`Policy.Accept`, which signs it as it stands. The first pass with a key does not
sign them itself: a row with no signature looks the same as one whose signature
somebody took off, and a pass that signed every row it found without one would
sign whatever was put there that way.

**What is still out of reach.** With a key, every change to the manifest is one
the key made or one that is found, but two, and both are of what happened since
the latest checkpoint:

- a blob added since then, taken away with its row: nothing written yet says it
  was there;
- a row put back to a state it was in since then, with the signature it had
  then. For a row added since, that is its `adding`, and a blob of somebody's
  labelled with its intent is taken in under it by the next pass.

A pass ends with a checkpoint, so for what a pass writes the window is the
pass. What `Policy.Forget`, `Policy.Purge` and `Policy.PurgeTenant` write is
covered from the next pass's checkpoint. And a key read into the process is the
process's: whoever holds the process can have it sign what they like while they
do, and with a key from a file, after.

#### Keys in a device

A key in a device is named in `audit.checkpoints.key` by a URI, once the app
links in what speaks to the device. Each is a package of its own that the app
imports, as a database driver is, so that a binary carries the one client it
uses and none of the others:

| | `audit.checkpoints.key` | the app imports | signs with | tried against |
| --- | --- | --- | --- | --- |
| a PKCS #11 token | `pkcs11:token=payday;object=checkpoints?module-path=…&pin-source=…` | `config/signerpkcs11` | Ed25519, ECDSA P-256/384/521, RSA | SoftHSM 2.6 |
| a TPM 2.0 | `tpm:0x81000100` | `config/signertpm` | ECDSA P-256 (P-384/521), RSA 2048 (3072) | swtpm 0.7 |
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
  what keeps a token alive for longer than it lives. The version that was
  latest at start is the one that signs, until the next start.
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

A key rotated is a new public half: keep the old one in
`audit.checkpoints.trust` for as long as there are checkpoints it signed.
`trust` reads PKIX, as base64 or PEM — what `tpm2_readpublic -f pem`,
`pkcs11-tool --read-object --type pubkey` (DER, so through `base64 -w0`) and
`aws kms get-public-key` write, and what Transit's `keys` endpoint answers for an
ECDSA or RSA key. A device of another kind is an app's own `crypto.Signer`,
registered under a scheme of its own with `config.RegisterSigner`. Google Cloud
KMS and Azure Key Vault are not here yet, for want of something to try them
against; the first is reachable through PKCS #11 today.

#### Sealed contents

Encryption at rest is the storage's — S3's server-side encryption with a KMS
key, an encrypted disk — and it covers what most requirements ask of it; the
manifest is what says the archive is what it was. What it cannot do is let a
tenant that leaves take its trail out of the archive's **backups**, which are
kept as long as the archive is and which no purge reaches. `trail.Keys`
(`audit.keys`) is for that:

- **Only what a row says is sealed** — `value` and `patch`, together, row by
  row, with [age](https://age-encryption.org). Who acted, on what, when and
  whose it is stay in the clear, because every act on the archive decides by
  them: a pass folding a month, a subject's erasure, a tenant leaving, a hold
  that keeps part of a file. None of them needs a key; only reading what a row
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
  chunk of a tenant leaves its key, since the tenant is still there.
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
  when there is no archive; what it does not do is discard or destroy;
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

## 7. What payday does not do

Some of these are gaps and some are decisions. The difference is written down
because a reader cannot tell them apart.

**Decided against:**

- **Deciding when one person leaves the trail.** The retention policy above is
  about **age** and reaches everybody's rows at once. A right-to-erasure request
  is about a subject — *which* rows, and *when*, is what an app owes somebody
  under a regime payday cannot know. What payday does supply is the half with no
  judgement in it but one: `trail.Policy.Forget` blanks `value` and `patch` for
  a set the caller chose, in the database and in the archive in one call —
  a mechanism that stopped at the database would destroy the copy an operator
  can see and leave the copy in the archive beside it — and leaves the rows a
  legal hold is on, answering with them.

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
