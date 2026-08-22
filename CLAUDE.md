# Working on payday

payday is a gRPC application framework. Most of what an app does is **generated
from its schema**, so a change here usually means changing a generator and then
proving it against `internal/apptest`, which is a real app.

## Two modules

`internal/apptest` is a module of its own — it owns the `ent` dependency payday
does not. **`go test ./...` at the root does not reach it**, and it is where
nearly every claim payday makes is actually demonstrated.

## Checking your work

```sh
./scripts/test.sh            # both modules, gofmt, vet
```

**Read the exit code. Do not grep the output.** `scripts/test.sh` aborts on a
vet error whose text does not start with `FAIL`, so a grep for `^FAIL` matches
nothing on a broken tree and reads as success. This has produced four false
"all green" reports; the exit code was correct every time.

After anything that touches a schema, a generator, or `schema/payday/`:

```sh
go tool pd gen --check --ts internal/apptest    # exit 0 or the commit is incomplete
```

On the database an app is deployed on, whenever a change touches what gets
written -- the trail, the outbox, a generated write path, a migration, anything
to do with nullability or ordering:

```sh
./scripts/with-postgres.sh                       # the gate, on PostgreSQL
./scripts/with-postgres.sh go test ./pdtest/...  # or anything narrower
```

It brings a database up on whatever Docker engine is reachable, or uses the one
`PDTEST_POSTGRES` names. Not every change is worth the minute -- SQLite needs no
server and that is what keeps the loop above fast -- but SQLite is permissive in
exactly the directions a write goes wrong: no real types, NULLs sort the other
way round, and a nil `[]byte` is an empty blob to it where pgx sends SQL NULL.
Twice now that last one has reached main. The long version is on
`pdtest.Postgres`.

The TypeScript halves:

```sh
npm --prefix ts run check && npm --prefix ts test
npm --prefix internal/apptest/ts run check && npm --prefix internal/apptest/ts test
```

Two are opt-in, one because it writes rather than checks and one because it is
slow:

```sh
PDTEST_UPDATE=1 go test -C internal/apptest ./...    # rewrite golden files
npm --prefix internal/apptest/ts run test:sandbox      # the script sets PDTEST_SANDBOX
```

`-C internal/apptest` because the golden files are the app's, and `./...` at the
root does not reach it.

`buf breaking` runs in CI against two baselines and both are worth running
locally before changing a `.proto`:

```sh
buf breaking --against '.git#ref=origin/main'
buf breaking --against buf.build/payday/payday:dev
```

## Do not edit

- `internal/apptest/**/*.g.go`, `*.pb.go`, `server/bare/`, `server/pd/`,
  `internal/ent/`, `ts/gen/` — regenerate instead
- `internal/apptest/proto/app/payday/*.proto` — these are **copies**. The source is
  `schema/payday/`, and `pd gen` copies and rewrites the package line

## Where things live

| | |
| --- | --- |
| `schema/payday/` | payday's own entities, as `.proto` sources apps copy |
| `internal/pdgen/` | the `protoc-gen-pd` plugin — layers, walls, `List`/`Watch` |
| `internal/pdcli/` | the `pd` command, and `template/` is what `pd new` writes |
| everything else at the root | a runtime package; the package comment is its documentation |

Upstream generators are `github.com/protobuf-orm/protoc-gen-orm-service` and
`protoc-gen-orm-ent`; `internal/apptest` is the app payday is tried against and
is the one in this repository.

## Documentation

Two kinds, and putting something in the wrong one is the usual mistake:

- `docs/guide/*.md` — **how to do something.** Task-shaped, for someone using
  payday.
- `docs/*.md` — **what a part is and why.** Reference, for someone deciding.

Both are English. Neither duplicates a package comment: godoc is the detail, the
docs are the shape of the whole. When adding a runtime package, its **package
comment** is the real documentation.

## Two habits this repository holds to

**Verify by running it.** A claim about what code does is not a claim until the
thing has been run. When adding a check, confirm it fails without the check —
this has caught a test that asserted nothing and a `pd doctor` fix that did not
compile.

**Comments say why, not what.** The reasoning lives next to the code, including
the alternatives that were rejected and what they would have cost. Do not
summarise one when moving it.
