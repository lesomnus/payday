# payday

A framework for gRPC applications: declare an entity once, and what follows from
it is generated rather than remembered.

Where it stands: **CP1 of [DESIGN.md](DESIGN.md) is done.** An identifier says
what kind of thing it names, and the tenant wall is generated from what the
schema declared. See [docs/](docs/) for the rest of the plan.

## What a schema says

```proto
message Robot {
  bytes  id     = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string alias  = 4;

  option (orm.message)   = {rpc: {crud: true}};
  option (payday.entity) = {
    domain: 7                    // what its identifiers name
    tenanted: {via: "tenant"}    // and whether its rows are behind the wall
  };
}
```

Two lines, and neither can be left out. `protoc-gen-pd` refuses to generate a
schema that does not say them, because both fail quietly when they are left to
be noticed later — an entity with no domain hands out identifiers that say
nothing, and an entity that never said whether it is walled sits outside the
wall while everything goes on compiling.

## What comes out

```go
sink, err := bare.NewServer(db,
    bare.WithMinter(pd.Minter()),   // every row named by what it is
    bare.WithScope(pd.Wall()),      // every read behind the tenant it belongs to
)
```

Nothing else. There is no `wall.go` to write and no method per entity to
remember: an entity added to the schema arrives with its wall already on it.

## The identifier

A UUIDv8 carrying one byte that says what kind of thing it names.

```
0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a
                    ^^ robot
```

It buys three things a plain UUID cannot: a reference checked before the
database is, a row that can still be named after it is erased, and a lookup that
does not have to try every table. See [`pdid`](pdid).

## Layout

| | |
| --- | --- |
| [`pdid`](pdid) | the identifier, its domain, and the registry generated code fills |
| [`frame`](frame) | who a request is from, and which tenants it may see |
| [`proto`](proto) | the `payday.entity` option |
| [`cmd/protoc-gen-pd`](cmd/protoc-gen-pd) | reads that option and writes what follows |
| [`internal/apptest`](internal/apptest) | the app payday is tried against |

```sh
$ go test ./... && (cd internal/apptest && go test ./...)
$ ./scripts/gen-apptest.sh   # what `pd gen` will be
```

## The schema

`buf.build/payday/payday`, pushed under the **`dev`** label while the option is
still moving. Nothing consumes `main` yet, and nothing should until an app other
than `internal/apptest` depends on it -- a published schema that then changes is
the one cost this whole design is arranged to avoid paying twice.

```sh
$ buf push --exclude-unnamed --label dev
```

## Licence

[Apache 2.0](LICENSE).
