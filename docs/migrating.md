# Migrating

What an app has to change when payday does, newest first, each entry saying how
to tell whether it applies to you.

There is no entry before the first app built on payday launches. Until then
payday keeps nothing for an earlier payday -- a store, a format, a lock,
configuration or generated code an earlier payday made is not carried forward
-- and so nothing here says how to bring one forward. From that launch on, a
change that asks something of an app is entered here.

What holds for every version is how a rename is carried, below.

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
