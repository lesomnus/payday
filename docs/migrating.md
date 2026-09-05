# Migrating

What an app has to change when payday does. Newest first, and each entry says
how to tell whether it applies to you.

## A UUID is the standard library's

`uuid.UUID` now comes from Go 1.27's `uuid` package rather than from
`github.com/google/uuid`. An app that names the type does so from the new
import path, and drops the dependency.

Nothing moves in the database or on the wire. `database/sql` writes a
`uuid.UUID` out in the canonical form and reads one back, the way it has always
done a `time.Time`, which is the same text `github.com/google/uuid` wrote for
itself; and a UUID has always crossed the wire as sixteen bytes.

What the standard library does not have, `entuuid` does:

| was | is |
| --- | --- |
| `uuid.FromBytes(b)` | `entuuid.FromBytes(b)` |
| `uuid.Nil` | `uuid.Nil()` |
| `uuid.NewString()` | `uuid.New().String()` |
| `uuid.Must(uuid.NewV7())` | `uuid.NewV7()` |
| `v.Version()`, `v.Variant()` | `v[6]>>4`, `v[8]>>6` |

`entuuid` is `github.com/protobuf-orm/protoc-gen-orm-ent/runtime/entuuid`, which
a generated server already depends on. Its `FromBytes` is the one that matters:
a UUID arrives as protobuf `bytes`, where nothing enforces the length.

A predicate written by hand against a UUID column binds the `uuid.UUID`; there
is nothing to convert. The one place that still wants a `driver.Valuer` is
`entql`, which asks for one at the call rather than at the bind:
`driver.DefaultParameterConverter.ConvertValue` is what to wrap it in.

## Every table is named after its entity, and none of them is pluralized

**Applies if** you have a database. It is a rename of every table an app has.

payday builds on a fork of ent, and the fork stopped putting entity names
through an English inflector to decide what to call a table. `User` was `users`
and is `user`; `Person` was `people` and is `person`; `Category` was
`categories` and is `category`. The name is the `snake_case` of the entity now,
so it can be worked out from the schema rather than looked up.

Join tables of a plain many-to-many edge are unchanged -- they are named after
the edge, not the type -- and so are index names, which were already built from
the singular. Foreign key constraint symbols carry two table names and so move
with them: `cards_users_card` is `card_user_card`.

**What to change.** Nothing in your schema or your Go code: the generated
constants follow, and `pd gen` writes them. The database is the work. `pd gen`
plans the rename like any other schema change, and `entschema.Check` refuses to
serve a database that does not match, so a deployment that has not run it will
say so rather than read an empty table.

Anything that names a table itself has to move by hand: raw SQL in your code,
a view, a trigger, a dashboard query, an entry in this file. A query written
against `roles` wants `role` now.

## A message-typed field is stored as protojson, and was storing nothing

**Applies if** an entity of yours has a field whose type is a message you
declared without `orm.message` — a value carried whole by the row that holds it,
with no table and no service of its own. If none does, nothing here applies, and
that is why this went unnoticed: payday's own reference app had no such field
until it was given one.

What was generated was an `encoding/json` column. Every message payday generates
uses the protobuf opaque API, whose fields are all unexported — so
`encoding/json` saw none of them, and every value round-tripped as `{}`. Written
whole, read back empty, with no error on either side.

It is `entpb.ValueScanner` now, which is protojson, in a `jsonb` column on
PostgreSQL and `json` on MySQL.

**What to do about the rows you have.** Nothing, almost certainly: they all hold
`{}`, because that is what the bug wrote. What they do *not* hold is anything to
migrate, so the column can be rewritten in place. If your app worked around this
— storing the same value a second way, or reading it from somewhere else — that
workaround is now the thing that disagrees with the column.

**And the field names travel.** protojson writes `displayName` where the schema
says `display_name`, so a SQL predicate reaching inside the column has to spell
it that way, and renaming a field of such a message is a silent migration
nothing checks.

## A reconnecting watch takes the snapshot again, whatever it asked for first

**Applies if** you run `<app> <entity> watch --retry` and set `skip_snapshot` on
the request, or you wrote a client that does the same thing.

`skip_snapshot` says *I know the current state*. After a gap that is no longer
true: whatever changed while the connection was down was sent to nobody, and a
watch has no backlog to catch up from. So a reconnect clears the flag and asks
for the snapshot, which is the only thing that says what was missed.

What changes for you is volume rather than correctness: a client that sized its
buffers for a delta-only stream gets a full snapshot on every reconnect. The
alternative is holding a row that is wrong until the next write happens to
correct it, which may be never.

## A batch operation is on the trail under its own name

**Applies if** you read the audit trail by `action` — a report, a retention
rule, a filter on a dashboard — and something in your app writes through
`payday.BatchService`. It applies as well to anything that groups writes by the
watch event they arrived in, which the outbox answers differently; see below.

The recorder does not know what the caller asked for, so it asks gRPC. Inside a
batch, gRPC's answer is the envelope — `/payday.BatchService/Do`, for every
operation of every batch — so a hundred different writes were filed as one
action, and "who renamed this" answered with a method that renames nothing.

Each operation is dispatched now in a context where gRPC answers with the
operation's own method, which is the name the caller wrote into the op. A batch
of two Adds is two rows whose `action` is `/app.RobotService/Add`, exactly what
the same two calls made one at a time would leave. The envelope files no row of
its own, because it writes nothing.

**A watch hears it differently down the two publishing paths**, and an app with
the outbox on has both. The in-process publish is one event per call — the
interceptor, once the handler has answered — so the batch arrives **whole**:
`Event.Method` is the batch, and each `Change.Method` inside it is the
operation's. The queue is a row per write and the drainer publishes rows, so the
same batch comes back out of it as one event per operation, each holding its
single change and each carrying the operation's method as `Event.Method`. The
grouping was never in the queue to hand on — a row is written inside the
transaction, and the call it belongs to only ends after the commit — so a
subscriber looking for the batch in `Event.Method` finds it on the immediate
event and never on the drained one.

**What to change.** A query or a retention rule matching
`/payday.BatchService/Do` matches nothing written from here on — those rows sit
under the operations' own methods now, beside the ones the same writes leave
outside a batch. Rows already written keep the action they were written with;
nothing rewrites the trail.

The seam is [`batch.AsOp`](../batch/batch.go), which the generated dispatch
calls once per operation, and which anything dispatching operations of its own
calls the same way. The queue's half is `Drain` in the generated `server/pd`,
which publishes a queued row at a time and has no call to group them by.

## A soft erase is on the trail under the row it erased

**Applies if** anything reads the trail for erasures, and especially if the
tenant whose row was erased is one of the readers.

`Audit.value` is the row as it was when the event was over, an Erase included,
and `Audit.tenant_id` is whose row it was. A soft erase was the one write where
neither held. The recorder reads the row back through the bare server, whose
every read narrows to the rows still here, so the row the erase had just stamped
answered NotFound and the record took the hard-erase path: the actor's tenant,
an empty value. The wall on the trail is the OR of `tenant_id`,
`actor_tenant_id` and `counterpart_tenant_id`, and an erase names the erased
tenant in none of them — the first two were the actor's and the third is unset
unless the operation said otherwise — so that made the record of an erasure the
one row the erased side could not read.

That read is retried past the erased filter now, inside the same transaction.
A soft erase is filed under the **erased row's** tenant, and its value is the
whole row with the stamp on — the only account of what a row held at the moment
it went.

**A hard erase is unchanged**, and is the exception on purpose: the recorder
runs inside the transaction after the delete, so there is nothing left to read.
It is filed under the actor's tenant with an empty value, which is the last
thing known about it.

**What to check.** Two things move. A reader that found erasures by an empty
`value` finds only hard ones from here on. And a tenant reading its own history
sees the erasures of its own rows, which it could not before — that is the
point of the change, and it is a widening of what that tenant may read.

## An overlay may not change what the file is

**Applies if** an entity overlay of yours — `proto/ext/payday/holder.ext.proto`
and the rest — carries a `features.` option at the top of the file, outside
any message.

The merge unions the file's options, so `option features.field_presence =
IMPLICIT;` written there lands on the **whole** copied entity: every field of
every message in it, payday's own included. What that costs is not obvious,
which is why it is refused rather than documented. On a field something calls
`Has` on, it stops the build, which is the lucky version; on a field nothing
calls `Has` on, it changes only what "not set" means on the wire, and nothing
says so.

The same refusal already covered contract overlays
(`proto/ext/<pkg>/*_svc.ext.proto`); it covers entity overlays now. The line is
easier to carry into one of those: the entity file an overlay is written against
begins with one, so copying its head to get started brings it along, and the
merge used to take it without a word.

**What to change.** Delete the line — `pd gen` refuses and names the file. An
option that is about one message goes **inside** that message, which is where it
is the app's to say and where generation leaves it alone.

## `ls --next` carries on from where it says it does

**Applies if** anything pages through a list with the commands `pdcmd` builds,
or if you mounted a hand-written list RPC into the tree.

The two halves have different names. An answer calls the cursor `next` and a
generated list request calls the same thing `after`, for where it goes rather
than where it came from — and the flag's value never reached the request at
all, so every page was the first page. A first page repeated is exactly what an
honest one-page list looks like, so nothing said so.

`--next` writes `after` now, and falls back to `next` for a hand-written service
that named the field the way the flag is named. A list request with neither is
refused by name rather than dropped:

```
acme.ThingListRequest: has no cursor for --next to carry on from
```

**What to change.** Nothing, for a generated list. A hand-written list RPC
needs a `string` field called `after` or `next` to page at all, and now says so
instead of answering the first page again.

## A grant's methods are patterns, so `*` in one now means something

**Applies if** anything you store and hand to `frame.Grant.To` — a role's
methods, an API key's, whatever your app calls them — could already contain a
string with `*` as a whole part, like `/app.RobotService/*`.

`Grant.Allows` was a membership test. A stored `/app.RobotService/*` matched
only a method named exactly `*`, which cannot exist, so such a string allowed
**nothing**. It is a pattern now and allows that whole service.

That is a widening, and it is the only backwards-incompatible thing here: a
grant of whole method names behaves exactly as it did, because `frame.Covers`
answers on equality first.

**What to check.** One query, against wherever you keep them:

```sql
SELECT * FROM role WHERE EXISTS (
  SELECT 1 FROM json_each(methods) WHERE json_each.value LIKE '%*%'
);
```

Anything it returns was allowing nothing and now allows something. Almost
certainly there is nothing — a string like that is one somebody typed expecting
this feature before it existed.

**What you gain**, and the reason for it: a list of methods is a snapshot of
something that grows. Write out every method of a service today and the next
release adds one the role does not allow — silently, to a role whose name says
it covers that service.

```
/app.RobotService/Get     one method
/app.RobotService/*       one service
/app.*/*                  one package
/app.*/Get                that method wherever it appears
/*.*/*                    everything
```

A whole part or nothing. `*Get*` is not a glob, it is a method name that
happens to contain asterisks — partial matching is where three string
comparisons become a regular expression.

And a pattern is covered by **one** pattern you hold, never by several
together. Holding `/app.A/*` and `/app.B/*` does not let you hand out
`/app.*/*` even where those are the only two services, because the third one
added next release would be covered by a grant made before it existed. See
[`frame.Covers`](../frame/method.go).

## payday's entities are copied inside your proto package

**Applies if** your schema has `import "payday/tenant.proto"` or any other
`payday/*.proto`, which is every app generated before this.

`pd gen` used to copy payday's entities to `proto/payday/`. A protobuf registry
is per process and keys files by their path, so two apps that both did that
registered one path twice and panicked before `main` — which is not
hypothetical, it is what the first app to import another payday app's generated
client did to its own binary. The copies land at `proto/<pkg>/payday/` now, so
the path carries the app's proto package and no two apps collide.
[Two apps in one process](guide/packages.md#3-two-apps-in-one-process) is that
rule in full.

**What to change**, in your hand-written schema and in your overlays:

```diff
-import "payday/tenant.proto";
+import "app/payday/tenant.proto";
```

where `app` is whatever your entities declare as their proto package. Then:

```sh
rm -rf proto/payday       # the old copies; pd gen writes the new ones
go tool pd gen .
```

Generated `*_svc.g.proto` are rewritten for you. If one is left holding the old
import, delete it and generate again — a stale one is an input to the next run.

**And check your proto package is your own.** The package name is the path on
disk, so distinct packages are distinct paths — and two apps that both kept
`pd new`'s `package app;` are distinct in neither, their messages being
`app.Holder` on both sides. Renaming it is one line per proto:

```diff
-package app;
+package acme;
```

`go_package` is separate and does not have to match.

The rule this leaves: **two payday apps can share a process when their proto
packages differ**, and two instances of the *same* app always could — one binary
links that app's files once, so nothing registers a path twice.

## `payday.Holder` is watchable, and has a list

**Applies to** every app, and needs no change from you.

`Holder` declares `watch: {}` and a `list:`, so `HolderService` gains `Watch`
and `List`. An app that does not open a stream pays for a generated method and
nothing else.

It is declared in payday's own schema because an app cannot: an overlay merges
**fields** and not the entity option, so there was no way for a deployment to
make payday's entity streamable.

## A field can say it is never answered with

**Applies if** you store a verifier — a password hash, an API key hash.

`(payday.field).secret` marks a field that is written and never handed back, and
`pd.Secret` is the generated layer that clears it on the way out. Stack it:

```diff
-app.Build(walled, core.Build(), pd.AuditBuild(), pd.GateBuild())
+app.Build(walled, core.Build(), pd.AuditBuild(), pd.SecretBuild(), pd.GateBuild())
```

The layer is only generated for an app that declared one, so a stack naming it
without one will not compile — which is the reminder.

**Ordering**: the extension is in payday's buf module, so an app depending on
`buf.build/payday/payday` needs a version of it that has this. Until then the
compile says `unknown extension payday.field`.

## A field can say a request may not assert it

**Applies if** you have a stamp — when something was verified, attested,
approved — that the deployment establishes rather than a caller.

`(payday.field).stamped` refuses it in the generated `Add`:

```proto
google.protobuf.Timestamp date_verified = 9 [
  (orm.field) = {nullable: true},
  (payday.field) = {stamped: true}
];
```

```
InvalidArgument: date_verified: is established by this deployment and not
asserted by a request
```

**`immutable:` is not this, and reaching for it is the mistake this exists to
end.** `orm.field.immutable` takes a field out of the **patch** request and
leaves it in `Add`, which is right for a tenancy stamp — set once, never moved —
and exactly backwards for this: nobody asserts a verification when the row is
created, and something writes it on the day the verification happens.

The refusal lands in the **gate**, which is exact rather than convenient: a
stamp is not a permission and not a column nobody may touch, it is a fact a
*request* may not assert. So an app's own work through a server without the gate
— `init`, a seed, the call that does the verifying — writes it without anybody
having listed those callers. `Patch` is untouched, because that is the road the
stamp is written by.

Two apps found this the hard way before the option existed, each closing it in a
layer of its own. A layer works and it is invisible: it is a line in a stack
rather than a property of the message, so the next app declares the same field
and does not know to write the same refusal.

## A field that lies about presence is refused

**Applies if** your schema has a message field with no `nullable`, no `default`
and no marker — a `google.protobuf.Timestamp` you meant to be optional, most
likely.

It generated a NOT NULL column while the API beside it had `Has…`, so a caller
asking whether a value was set was told yes and read a zero. Generation refuses
it now and names both fixes, because they mean different things:

```diff
-google.protobuf.Timestamp date_seen = 8;
+google.protobuf.Timestamp date_seen = 8 [(orm.field) = {nullable: true}];
```

or a `default`, if the value is always there.

`date_created`, `date_updated` and `date_erased` are exempt, and the rule is
their declarations rather than their names — an app whose version field is
called something else is not caught by a rule about spelling.

## `pdtest.DB` gives a second database to a second call

**Applies if** a test of yours calls it twice, which is a test that stands two
apps up.

It used to name the schema after the test, so two calls got one schema and the
second's `DROP SCHEMA` removed the first app's tables. It passed on SQLite and
failed only under `PDTEST_POSTGRES`. Nothing to change: no existing test could
have wanted the old behaviour.

## `order:`, `by:` and `with:` name a field as `{name, number}`

**Applies if** your schema declares a `list:`, which is every entity with a page
or a watch.

They took a bare string. A proto field is renamed without the wire changing, so
the **number** is the identity and the name is a label -- and a declaration
carrying only the label follows a rename onto whatever took the old name. That
is not the loud failure of "there is no such field" but the quiet one of "that
is a different field now", and a paging order that moved to another column is
exactly what this generator exists to refuse.

It is also how the rest of a schema already points at a field: `orm`'s indexes
carry `{name, number}`, and two conventions for one thing is one somebody has to
remember which is which for.

```diff
     list: {
-      order: [{field: "date_created"}, {field: "id"}]
-      by: ["ref", "tenant"]
-      with: ["tenant"]
+      order: [{field: {name: "date_created", number: 15}}, {field: {name: "id", number: 1}}]
+      by: [{name: "ref"}, {name: "tenant", number: 2}]
+      with: [{name: "tenant", number: 2}]
     }
```

Either half alone still works, so a small schema is not made verbose and an
existing one is not rewritten to be read. **Both together is what is checked**:
generation resolves by the number and refuses when the name no longer matches.

`{name: "ref"}` has no number, since it is not a field.

`tenanted: {field: ...}` is unchanged and still takes plain strings -- it names
a column the wall reads, not a filter.
