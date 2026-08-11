# Migrating

What an app has to change when payday does. Newest first, and each entry says
how to tell whether it applies to you.

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
SELECT * FROM roles WHERE EXISTS (
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

`pd gen` used to copy payday's entities to `proto/payday/`. Two apps in one
process then both registered `payday/holder.proto`, and a protobuf registry is
per process and keys files by path — so linking them panicked before `main`,
with nothing to catch it. That is not hypothetical: it is what happened the
first time one payday app imported another's generated client, and the importing
app's own binary stopped starting.

The copies land at `proto/<pkg>/payday/` now, so the path carries the app's
proto package and no two apps collide.

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

**And check your proto package is your own.** `pd new` writes `package app;`,
and two apps that both kept it cannot share a process whatever their file paths
are — their messages are both `app.Holder`. Renaming it is one line per proto:

```diff
-package app;
+package roster;
```

`go_package` is separate and does not have to match.

The rule this leaves: **two payday apps can share a process when their proto
packages differ.** Two instances of the *same* app always could.

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
