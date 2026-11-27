# Migrating

What an app has to change when payday does, newest first, each entry saying how
to tell whether it applies to you.

Until the first app built on payday launches, payday keeps nothing for an
earlier payday -- a store, a format, a lock, configuration or generated code an
earlier payday made is not carried forward -- and so nothing here says how to
bring one forward. What is entered before then is a change in what payday does
that an app's own code may have leaned on. From that launch on, every change
that asks something of an app is entered here.

What holds for every version is how a rename is carried, below.

## A service overlay no longer says a generated field again

**It applies to you** if a `proto/ext/<pkg>/*_svc.ext.proto` declares a
message the generated contract has -- an `Add` request, a `List` request --
and says in it a field that message already has: at a number it has, or under
a name it has at another number. `go tool pd gen` now refuses it, naming the
file, the message, the number and both lines:

```
machine_svc.ext.proto: message MachineAddRequest says field 9 again, which the contract's message already has.

    the contract's: int64 hours = 9 [features.field_presence = IMPLICIT];
    the overlay's:  int64 hours = 9 [deprecated = true];

The merge would take the overlay's field whole -- its type, its presence and its options -- and keep nothing the generator said of it: what the generator wrote, and every layer and client written against that, would be reading another field. Nothing would say so.

An overlay **adds**. Add what is wanted at a number MachineAddRequest leaves empty, under a name it does not use; or, if a different request is wanted, give it an RPC of its own under its own name
```

Before, the merge took such a line in place of the generator's field and `pd
gen` said nothing: the field had the overlay's type, presence and options, and
lost whatever the generator had said that the overlay did not.

**What to do:** take the line out. If something was wanted beside the
generated field, add it as a field of its own at a number and a name the
message does not have. If a different request was wanted -- another type,
another meaning -- declare an RPC of your own with its own request, as the
[schema guide](guide/schema.md#an-rpc-of-your-own) shows.

An overlay that only adds -- new fields at new numbers, messages and RPCs of
its own -- is untouched.

## The page package's floor is 0.1.6

**It applies to you** if the app has a page (`ts/package.json`) and its
`@lesomnus/payday` is older than 0.1.6. Until 0.1.6 was out, a caret of
`^0.1.2` through `^0.1.5` installed 0.1.5. `go tool pd doctor` says so:

```
ts has @lesomnus/payday 0.1.5, older than 0.1.6 -- payday's half of the page: on an older one a list opens a Watch the server refuses for every list over a tenant, an edge or a field, and reads each such list twice

    go tool pd upgrade
```

Before 0.1.6, a list query opened a `Watch` over whatever filters the list
had. A generated `Watch` takes only filters that each name a row and refuses
the rest with `InvalidArgument`. So every such list was refused at once, read a
second time, and never live. Nothing broke, but the server logged a refusal for
each list shown. From 0.1.6, a list opens a `Watch` only over the rows it names.

`go tool pd upgrade` moves the page to `^0.1.6`, installs it, and says why as
it does:

```
pd: @lesomnus/payday 0.1.5 -> ^0.1.6: below 0.1.6, payday's half of the page: on an older one a list opens a Watch the server refuses for every list over a tenant, an edge or a field, and reads each such list twice
```

The payday that raised this floor also renamed a grant's end: `date_expires`
is now `date_expired`, at the same number and of the same type. A page reads
it as `dateExpired` where it read `dateExpires`.

- **Where a page meets it:** its own generated code. `pd gen --ts` writes
  payday's `Entitlement` entity into `ts/gen/<app>/payday/entitlement_pb.ts`.
  The package's own copies of `EntitlementGrant` and `TokenIntrospectResponse`
  are not among its exports.
- **When:** the regeneration that `go tool pd upgrade --payday <commit>` runs
  brings the rename in, and the upgrade then meets this floor in the same run.
- **What to change:** a page that names the field changes one word.
  `npm run check` names each place, as a property `dateExpires` that does not
  exist on `Entitlement`.

## An RPC of your own no longer answers a secret

**It applies to you** if an entity declares a field `(payday.field) = {secret:
true}` and an RPC of yours answers that entity, itself or inside a message of
yours -- or a generated verb of another entity answers it through an edge.

The Secret layer cleared a secret from the generated verbs of the entity that
declared it, and passed everything else through. It now clears one from every
answer that can hold it, at any depth and in each message a stream sends; see
[a field about a person](guide/schema.md#a-field-about-a-person). So an RPC
that answered a secret through the entity's message no longer does, and a
client that read it there reads an empty field.

- **An RPC meant to hand a secret over once** -- a key, at the moment it is
  made -- answers it in a field of the answer's own, beside the entity rather
  than in the entity's secret field. Only an entity's field can be declared
  `secret`, so the layer leaves that one alone.
- **A handler that cleared the field by hand** before answering may stop: the
  layer clears it on the way past.

## A rename is moved before the migration looks for it

A migration finds a table, a column and an index each by its name, and drops
none of them. So a store an earlier build made holds what was renamed under its old
name, where the migration does not look, and it makes the new one, empty, beside
it: an entity's table with none of its rows, a column with none of its values,
an index built again from every row. Nothing fails, and the schema check a
server makes before serving passes. On SQLite a table whose index names a
renamed column is made again without the old column at all.

`pd gen` writes what was renamed, for an app's `Migrate` to move first:

| | what it holds | moved by |
| --- | --- | --- |
| `pd.TableRenames` | the table of each entity the schema renamed (`(payday.entity).formerly`) | `rename.Tables` |
| `pd.ColumnRenames` | each column of payday's own entities that payday renamed -- none so far | `rename.Columns` |
| `pd.IndexRenames` | each index stored under another name -- none so far | `rename.Indexes` |

`pd new`'s `Migrate` hands each over, in this order, before anything is made:

```go
if err := rename.Tables(ctx, s.Db, s.Dialect, pd.TableRenames); err != nil {
	return err
}
if err := rename.Columns(ctx, s.Db, s.Dialect, pd.ColumnRenames); err != nil {
	return err
}
if err := rename.Indexes(ctx, s.Db, s.Dialect, pd.IndexRenames); err != nil {
	return err
}
if err := valid.Indexes(ctx, s.Db, s.Dialect, entmigrate.Tables); err != nil {
	return err
}
if err := valid.Columns(ctx, s.Db, s.Dialect, entmigrate.Tables); err != nil {
	return err
}
return entmigrate.NewSchema(s.Drv).Create(ctx)
```

- **The tables first**, because the other two look for a column and an index on
  its table by the name the table has now, and find nothing on one still under
  its old name.
- **Each is the same call on any store**: one an earlier build made, one this
  build made, an empty one, and one it has moved already. What is not there
  under its old name is left alone.
- **A table** moves with its rows: on PostgreSQL with its indexes, its primary
  key, the foreign keys and (on 18) the NOT NULL constraints named after it,
  in one transaction of changes to the catalog; on SQLite renamed in place, its
  old indexes dropped for the migration to make again, while a foreign key keeps
  its name there, which nothing reads. A store holding both tables, the new one
  empty, has the empty one dropped; one with rows in both is refused
  (`rename.ErrBoth`).
- **A column** is renamed with what it holds: one change to the catalog on
  PostgreSQL, the table's definition rewritten in place on SQLite. One a
  migration already added beside the old takes what the old one held wherever
  it is empty, and the old one is dropped.
- **An index** is renamed on PostgreSQL, which builds nothing. SQLite renames
  none, so the old one is dropped and the migration makes the new one.
- **`pd doctor` says so** of an app with a renamed table or column whose own Go
  hands it to nothing.

What else an earlier build wrote down under an entity's old name reads as the
entity. `pdid.Formerly` registers each old word and full name, so `DomainOf`
and `Lookup` answer the entity's domain after what is registered now; the trail
reads a chunk of its archive labelled with an old word as the kind it was --
checked with its rows, cut by its window, and counted under its name now; and
`audit.by.<old word>` goes on configuring the kind, though a file that names one
kind under two of its words is refused where the process comes up.

[The schema guide](guide/schema.md#formerly--when-you-rename-an-entity) is how
to declare a rename.
