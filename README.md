# payday

A framework for gRPC applications: declare an entity once, and what follows from
it is generated rather than remembered.

- [**Permissions and the wall**](docs/guide/permissions.md) — who sees what, and
  where each part of that answer is enforced. Start here.
- [**Declaring an entity**](docs/guide/schema.md) — the options, the field
  numbers, `list:` and `watch:`, and everything generation refuses.
- [**The server**](docs/guide/server.md) — the stack and why it is ordered that
  way, writing a layer, the interceptors, configuration, the commands.
- [**The page**](docs/guide/client.md) — reads that keep themselves current,
  writes that need no invalidation rule, and what persists across a reload.
- [**Commands on your binary**](docs/guide/commands.md) — `get`, `ls`, `add`,
  `patch` and `erase` for every entity, from a connection you opened; the output
  formats, and a command for an RPC of your own.
- [docs/](docs/) — the rest: [batch](docs/guide/batch.md),
  [more than one proto package](docs/guide/packages.md),
  [refusals](docs/guide/errors.md), [signing somebody in](docs/guide/signing-in.md)
  and [testing](docs/guide/testing.md), and the
  references behind all of it — [the runtime](docs/RUNTIME.md),
  [tenancy](docs/TENANCY.md), [the generation contract](docs/SCHEMA.md),
  [the browser](docs/CLIENT.md).

```sh
$ go get -tool github.com/lesomnus/payday/cmd/pd
$ go tool pd new github.com/acme/widget .
```

That writes a schema, a server, a config file and a React page, and prints the
handful of commands that turn them into a running app.

## What a schema says

```proto
message Robot {
  bytes  id     = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string alias  = 4;

  google.protobuf.Timestamp date_erased = 14 [(orm.field) = {erased: {}}];

  option (orm.message)   = {rpc: {crud: true}};
  option (payday.entity) = {domain: 7};
}
```

Two things had to be said and one of them is a field.

`domain: 7` is what this entity's identifiers carry, and generation refuses an
entity without one: an identifier that says nothing about what it names is one
nothing can check.

`date_erased` says `Erase` stamps the row rather than destroying it. That is
refused too, in both directions — an entity that says neither this nor
`erase: {hard: {}}` does not generate, because payday cannot add a field to your
schema and so cannot make "soft" the silent answer.

**Tenancy is the silence.** Saying nothing means behind the wall, by the edge
called `tenant`, and the two ways of being wrong are why: assume a wall that is
not there and every row disappears within minutes; assume none that should be
and everything is visible to everybody, nothing breaks, and no test goes red.
The dangerous answer is the one you write down (`global: {}`), and grepping for
it finds every one of them.

## What comes out

```go
sink, err := pd.NewSink(db,
    bare.WithMinter(pd.Minter()),   // every row named by what it is
    bare.WithScope(pd.Wall()),      // every read behind the tenant it belongs to
)
```

Nothing else. There is no `wall.go` to write and no method per entity to
remember: an entity added to the schema arrives with its wall already on it.

Also generated, from the same declaration: the CRUD service, a `List` with a
cursor that cannot skip a row, a `Watch` that keeps a client's copy current, the
layer that refuses an `Add` into a tenant the caller cannot see, the audit trail
written inside the transaction that changed something, and the TypeScript half.

## The identifier

A UUIDv8 carrying one byte that says what kind of thing it names.

```
0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a
                    ^^ robot
```

It buys three things a plain UUID cannot: a reference checked before the
database is, a row that can still be named after it is erased, and a lookup that
does not have to try every table. See [`pdid`](pdid).

## Whose names these are

`Tenant`, `Holder`, `Audit` and `Outbox` are payday's entities and they land in
**your** proto package. A caller of your app says `/app.TenantService/Get`, not
`/payday.TenantService/Get` — a tenant and a holder are your customers and your
people, and nobody calling your API should have to learn the name of the
framework it was built with.

One name survives on the wire, `payday.BatchService`, and it is a transport
rather than a domain concept: several writes as one transaction, taking `Any`
and taking no position on what is in it. It keeps its name the way
`grpc.health.v1.Health` does, so a client written once finds it in any payday
app.

## Layout

| | |
| --- | --- |
| [`pdid`](pdid) | the identifier, its domain, and the registry generated code fills |
| [`slug`](slug) | `@acme/arm-01#robot` — the human-readable way to name a row |
| [`frame`](frame) | who a request is from, and what they may see |
| [`auth`](auth) | reading a credential: a header, a certificate, a token |
| [`gate`](gate) | what a caller may do, and the seam a deployment injects |
| [`audit`](audit) | the trail, written in the transaction that changed something |
| [`watch`](watch) · [`spin`](spin) | publishing what changed, and the loops that do it |
| [`batch`](batch) | several writes as one transaction |
| [`web`](web) | the port a browser reaches |
| [`migrate`](migrate) · [`version`](version) | refusing a server whose schema or payday moved |
| [`config`](config) · [`grpcx`](grpcx) · [`pderr`](pderr) | what an app was configured with, how it is served, how it refuses |
| [`ts`](ts) | [`@lesomnus/payday`](https://www.npmjs.com/package/@lesomnus/payday) — the client half |
| [`cmd/pd`](cmd/pd) | `new`, `gen`, `entity`, `sandbox`, `doctor` |
| [`internal/apptest`](internal/apptest) | the app payday is tried against |

```sh
$ ./scripts/test.sh -count=1      # both modules; `go test ./...` does not reach apptest
$ go tool pd gen --check --ts internal/apptest
```

## The schema

`buf.build/payday/payday`, pushed under the **`dev`** label while the option is
still moving.

```sh
$ buf push --exclude-unnamed --label dev
```

An app pins it in `buf.lock`, and `pd gen` only writes that lock when nothing
has. So an app that has generated once stays where it was pinned until
`buf dep update` is run — which is the app's decision to make and not payday's.

## Licence

[Apache 2.0](LICENSE).
