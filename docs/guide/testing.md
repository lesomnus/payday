# Testing

Most of a payday app's behaviour is generated, which changes what is worth
writing a test for. The wall, the minter, the trail and the page are covered by
payday's own suite; what your tests are about is your rules, and whether the
declarations you wrote mean what you thought.

`payday/pdtest` holds the parts of a harness that do not know what app they are
for: a database for one test, a clock, assertions that know about gRPC, and a
connection that never leaves the process. What stays yours is the stack and who
a test travels as, because those are the things your app decides.

- [1. The harness](#1-the-harness)
- [2. Travelling as somebody](#2-travelling-as-somebody)
- [3. Asserting on a refusal](#3-asserting-on-a-refusal)
- [4. Through a real connection](#4-through-a-real-connection)
- [5. The two seams that make an answer comparable](#5-the-two-seams-that-make-an-answer-comparable)
- [6. Golden files](#6-golden-files)
- [7. Testing the wall](#7-testing-the-wall)
- [8. The browser](#8-the-browser)

## 1. The harness

Build the app twice: once as it is served, and once with no wall.

```go
type App struct {
	Db      *ent.Client
	Walled  app.Server   // what a caller reaches
	Ungated app.Server   // for arranging state
}

func New(t *testing.T) *App {
	x := require.New(t)

	drv, dsn := pdtest.DB(t)
	db, err := sql.Open(drv, dsn)
	x.NoError(err)

	dia := dialect.Postgres
	if drv == "sqlite3" {
		db.SetMaxOpenConns(1)
		dia = dialect.SQLite
	}

	c := ent.NewClient(ent.Driver(entsql.OpenDB(dia, db)))
	t.Cleanup(func() { c.Close() })
	x.NoError(c.Schema.Create(t.Context()))

	walled, err := bare.NewServer(c, bare.WithMinter(pd.Minter()), bare.WithScope(pd.Wall()))
	x.NoError(err)

	ungated, err := bare.NewServer(c, bare.WithMinter(pd.Minter()))
	x.NoError(err)

	return &App{Db: c, Walled: walled, Ungated: ungated}
}
```

Note what is **not** written by hand here: the minter comes out of
`(payday.entity).domain` and the wall out of the tenancy declared beside it. If
a test has to state either of them, something in the schema did not say what you
thought.

### The database is one test's own

`pdtest.DB(t)` answers a driver and a DSN. With nothing in the environment that
is an in-memory SQLite database of this test's own, so tests are independent and
there is nothing to tear down. `SetMaxOpenConns(1)` for that case only, because
such a database belongs to the connection that opened it — PostgreSQL has no
such rule and pooling is the point of it there.

Name a server and the same suite runs on it, with no test changed:

```sh
$ PDTEST_POSTGRES='postgres://app:app@127.0.0.1/app?sslmode=disable' go test ./...
```

Each test then gets a **schema of its own** inside that database, created when
it starts and dropped when it ends, named after the test so that a leak says
which one leaked.

Worth doing, because everything payday generates is SQL and SQLite is permissive
in the directions that hide a mistake: no real types, NULLs sorted the other way
round, and a driver that turns a nil `[]byte` into an empty blob where pgx sends
SQL NULL. That last one was not hypothetical — the first time payday's own suite
met PostgreSQL, ten tests failed on two NOT NULL columns the trail and the
outbox had been writing nil into since they were written. It stays opt-in
because SQLite needs no server, and a suite nobody can run without one is a
suite nobody runs.

Which leaves knowing a DSN and having somewhere to get one, and that is enough
friction that the answer arrives from CI rather than from a desk. payday's own
[`scripts/with-postgres.sh`](../../scripts/with-postgres.sh) is that gap closed:
it uses the database `PDTEST_POSTGRES` already names, or starts one on whatever
Docker engine is reachable and takes it away afterwards. An app is welcome to
copy it — there is nothing payday-specific in it.

If your harness builds from your app's own configuration rather than from
`sql.Open`, the two answers go straight in: `config.DbConfig{Driver: drv, Dsn: dsn}`.

### `Ungated`, and why the harness goes beneath `pd`

`Ungated` is not a privilege. It is a server instance the wall was never
installed on, and a test needs it for the same reason a deployment does: there
is nobody to be inside a tenant before there are tenants. Why that is two
instances rather than a superuser flag is
[the server guide §2](server.md#2-the-two-servers-and-why-there-are-two).

This harness says `bare.NewServer` where a deployment says `pd.NewSink`, and the
two are not alternatives: `NewSink` calls exactly this constructor and wraps what
it answers with, adding the parts that are payday's rather than the ORM
generator's — the namer that decides an alias, the keyset paging behind a `List`,
and publishing to a `Watch` — after checking that the payday in the binary is the
one that generated the code. `pd.NewSink` is therefore the canonical way to build
a server. A harness may go beneath it while what a test is about is what `Add`,
`Get`, `Patch` and `Erase` do to a row; the moment it touches a `List`, a
`Watch` or an alias, it has to build the sink.

Naming is entirely up there — the name payday makes up when the request carried
none, the folding that makes `"  Acme "` and `"acme"` one row rather than two,
and the refusal of a given name that is not a name. Beneath the sink an alias is
whatever string arrived, stored as it arrived, so a test that asserts on a name
through a bare server is asserting on something the served app would never have
kept. A test about the interceptors builds the whole stack your `cmd` builds —
see [4](#4-through-a-real-connection).

## 2. Travelling as somebody

Every read through `Walled` narrows by what the frame says. So a test says who
it is:

```go
func As(ctx context.Context, ids ...pdid.Id) context.Context {
	f := frame.New(pdid.New(pd.TenantDomain), pdid.Nil, frame.Whole())

	return frame.Into(ctx, f.WithScope(frame.Only(ids...)))
}
```

```go
ctx := As(t.Context(), acme)
vs, err := a.Walled.Robot().List(ctx, req)
```

A context with no frame is a request nobody vouched for, and the generated
servers refuse it with `Unauthenticated`. That is the intended behaviour and it
is worth having one test that asserts it, because it is the failure mode of
forgetting to wire the auth interceptor.

## 3. Asserting on a refusal

`pdtest.X` is `require.Assertions` plus the one thing gRPC needs:

```go
x := pdtest.NewX(t)

_, err := a.Walled.Robot().Get(ctx, ref)
x.ErrCode(codes.NotFound, err)
```

`x.ErrCode` says which code came back when it is not the one you wanted, rather
than `Error(t, err)` passing for the wrong reason. Most of the interesting
assertions in a payday app are about a code: the wall answers `NotFound` rather
than `PermissionDenied` (a row you may not see does not exist as far as you are
concerned), a closed method answers `Unimplemented`, a stale patch answers
`FailedPrecondition`.

The last of those is worth a test of its own, and the test is two halves: the
patch whose `date_updated` no longer holds is refused, and **the same patch with
the version as it is now lands**. Without the second half a refusal proves only
that something was wrong, not that the premise was.

## 4. Through a real connection

When what you are testing is the interceptors — auth, limits, what is closed —
calling the server directly skips the thing under test.

```go
g := grpc.NewServer(pdtest.Logging(t), /* your chain */)
app.RegisterServer(g, s.Walled)

conn := pdtest.Serve(t, g)
c := app.NewRobotServiceClient(conn)
```

`pdtest.Serve` gives a `*grpc.ClientConn` over an in-process listener — a
channel rather than a port, so tests can run in parallel and nothing needs a
free port. It is a real client and a real codec: a message that does not
round-trip fails here and not in staging. The server is stopped when the test
ends, because one left running leaves its goroutines running too, and what they
break lands in whatever test happens to be next.

`pdtest.Logging(t)` attaches what the server logged to the test that ran it, so
a failure comes with the server's side of it — including a recovered panic and
its stack, which is otherwise an `Internal` error and no hint of where.
`pdtest.Logger(t)` is the same thing as an `*slog.Logger` if you need to hand one
somewhere else.

## 5. The two seams that make an answer comparable

Two things differ on every run, and both can be handed in:

```go
func told(t *testing.T, db *ent.Client) app.Server {
	ks := map[string]pdid.Id{
		"app.Tenant": pdid.MustParse("0199c3f4-2a10-8001-8a03-9f2e1c4d5b01"),
		"app.Robot":  pdid.MustParse("0199c3f4-2a10-8007-8a03-9f2e1c4d5b07"),
	}

	s, err := bare.NewServer(db,
		bare.WithClock(pdtest.Clock()),
		bare.WithMinter(bare.MinterFunc(func(_ context.Context, entity string, _ uuid.UUID, _ bool) (uuid.UUID, error) {
			v, ok := ks[entity]
			require.Truef(t, ok, "this test has no identifier for %s", entity)

			return v.Uuid(), nil
		})),
	)
	require.NoError(t, err)

	return s
}
```

**`WithMinter`** decides the identifier a row gets. **`WithClock`** decides what
a timestamp holds; `pdtest.Clock()` answers `pdtest.At` — 2001-02-03 04:05:06
UTC — forever.

A minter answers with a `uuid.UUID`, so a map of `pdid.Id` hands back `v.Uuid()`:
an identifier that says what kind of thing it names is a distinct type, and that
is the point of it. The missing key is asserted rather than shrugged at, because
the zero `pdid.Id` is a real UUID as far as the database is concerned — an entity
nobody thought of would be minted the nil one, quietly, and the golden file would
record it as if it had been meant.

Without a clock, the most a test can say about a stamp is that it is near now,
and the interesting questions are not near now. They are *did the version move*,
*does an erase stamp both fields together*, *is what came back the row that was
written*. Each is a comparison, and a comparison needs a value the test chose.

A constant rather than something that advances, because what these are for is
the answer and not the ordering. A test **about** ordering wants a clock it
moves itself, which is a closure over a variable and needs nothing from here:

```go
var now time.Time
s, _ := bare.NewServer(db, bare.WithClock(func() time.Time { return now }))

now = at
v, _ := s.Robot().Add(ctx, req)
now = at.Add(time.Hour)
u, _ := s.Robot().Patch(ctx, patch)
// u.DateUpdated moved, v.DateCreated did not
```

This is not about clock skew. What a deployment's clocks say to each other is
the deployment's business.

### Keying the minter by entity, not by a counter

A counter means a test that adds rows in a different order gets different
identifiers — so adding a setup line at the top changes every recorded answer
below it. Answering off the entity's name makes the setup order irrelevant.

The key is `app.Tenant` and not `payday.Tenant`: `pd gen` copies payday's
entities into your schema and rewrites their `package` line, so the name a
minter is asked about is the app's. See
[the generation contract §3](../SCHEMA.md#3-whose-names-these-are).

## 6. Golden files

With both seams handed in, the whole answer is comparable:

```go
t.Run("the robot", func(t *testing.T) { pdtest.Golden(t, robot) })
```

```sh
$ PDTEST_UPDATE=1 go test ./...
```

That writes `testdata/TestWhatever_the_robot.txtpb`; without it, the message is
compared against what is recorded, and a mismatch prints both — what was
recorded and what was answered, whole.

### Why the whole message

Because the fields a test asserts on are the ones somebody thought of. A
generated server answers with everything the schema declared, and the way that
answer goes wrong is a field nobody was asserting — an edge that came back
empty, a version that did not move, a column an overlay renamed. **A comparison
of the whole message is the only assertion that covers a field added after it
was written.**

### Two details

It is prototext rather than bytes, which is what makes those two blocks
something a person can read against each other. Bytes are read by nobody.

And it is an environment variable rather than a `-update` flag, which is the
usual Go idiom, because a flag has to be *registered* — `go test` refuses one it
has not been told about, before any of this runs. Registering it in `pdtest`
would put it on every binary that imports the package, including the ones that
never call `Golden`.

## 7. Testing the wall

The wall is generated, so what is worth testing is that you declared what you
meant. One test per entity, in the shape "two tenants, and one cannot see the
other's row":

```go
acme := a.tenantOf(ctx, x, "acme")
other := a.tenantOf(ctx, x, "other")
arm := a.robotOf(ctx, x, acme, "arm-01")

_, err := a.Walled.Robot().Get(As(ctx, other), ref(arm))
x.ErrCode(codes.NotFound, err)
```

If that test **fails**, the entity is not behind the wall: it said `global: {}`
and you did not mean it to. Leaving tenancy out is `tenanted: {via: "tenant"}`,
so the wall is on unless the dangerous answer was written down — and an entity
whose `tenant` edge is missing or goes somewhere that is not the tenant is
refused at generation rather than served openly. See
[what you get by saying nothing](permissions.md#1-what-you-get-by-saying-nothing).

The other half of the same declaration is worth one test too: an entity that
*did* say `global: {}` is readable from every tenant, and that test passing is
what says the word meant something. A pair like that turns a line of schema into
behaviour somebody can read.

An entity two steps from its tenant — `via: "robot.tenant"` — deserves the first
test more than the others do, because that is the case a hand-written wall gets
wrong quietly: easy to write for the entity that holds a tenant, easy to forget
for the one that reaches it through something else.

## 8. The browser

The TypeScript half runs under vitest, and what each layer is tested against is
a decision rather than a convenience.

The **store** is tested against no transport at all. It is a local copy of the
rows one caller may see, and nothing it does is a call; a transport in that test
would be scenery.

The **React bindings** and the **IndexedDB mirror** run against a fake transport,
deliberately. What those tests are about is who re-rendered and what landed in
the database, and a real server would make each of those a question about timing
as well. The mirror's IndexedDB is not faked in the same sense: it is
`fake-indexeddb`, an implementation of the specification rather than something
that agrees with whatever the test believes.

The **query layer** and the **client** start a real Go server and send it bytes.
A mock would agree with whatever the TypeScript believes, which is the one thing
not worth checking; what is worth knowing is whether the descriptors protobuf-es
wrote and the ones protoc-gen-go wrote describe the same thing, and when a row
changes, whether everything drawing it is told.

The sandbox — the whole app compiled to wasm, running in a page — is a test too,
and it has a script of its own rather than a place in `npm test`:

```sh
$ npm run test:sandbox
```

The script is what sets `PDTEST_SANDBOX`, and the test file skips unless that
variable is set, so it can sit beside the others and cost nothing on a plain
`npm test`. What it costs when it does run is a wasm build of the entire server,
a chromium and a dev server; CI builds the wasm and installs the browser first,
then runs the script exactly as written above.

That is the minute that buys the one thing neither half's own tests can say. The
wasm build says the app still compiles for the browser; this says the binary it
built actually runs, and that the two halves are the same program. Why that
distinction is worth paying for, and what a sandbox costs measured, are in
[the browser](../CLIENT.md#2-the-whole-app-in-a-page).

## Where to go next

- [The server](server.md) — the stack a harness builds.
- [Permissions and the wall](permissions.md) — what the wall test above is
  asserting.
- [Declaring an entity](schema.md) — where the minter and the wall come from.
- [The browser](../CLIENT.md) — the client the TypeScript suite is about.
