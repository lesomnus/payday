# Testing

Most of a payday app's behaviour is generated, which changes what is worth
writing a test for. The wall, the minter, the trail and the page are covered by
payday's own suite; what your tests are about is your rules, and whether the
declarations you wrote mean what you thought.

`payday/pdtest` holds the half of a harness that does not know what app it is
for. The other half — an empty database, the stack, who a test travels as — is
yours, because those are things your app decides.

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
	dsn := memdb.TestDB(t, url.Values{"_pragma": {"foreign_keys(1)"}})
	db, _ := driver.Open(dsn)
	db.SetMaxOpenConns(1)

	c := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	t.Cleanup(func() { c.Close() })
	c.Schema.Create(t.Context())

	walled, _ := bare.NewServer(c, bare.WithMinter(pd.Minter()), bare.WithScope(pd.Wall()))
	ungated, _ := bare.NewServer(c, bare.WithMinter(pd.Minter()))

	return &App{Db: c, Walled: walled, Ungated: ungated}
}
```

`Ungated` is not a privilege. It is a server instance the wall was never
installed on, and a test needs it for the same reason a deployment does: there
is nobody to be inside a tenant before there are tenants.

An in-memory SQLite database per test, so tests are independent and there is
nothing to tear down. `SetMaxOpenConns(1)` because a memdb is per-connection.

Note what is **not** written by hand here: the minter comes out of
`(payday.entity).domain` and the wall out of the tenancy declared beside it. If
a test has to state either of them, something in the schema did not say what you
thought.

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
servers refuse it. That is the intended behaviour and it is worth having one
test that asserts it, because it is the failure mode of forgetting to wire the
auth interceptor.

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
round-trip fails here and not in staging.

`pdtest.Logging(t)` attaches what the server logged to the test that ran it, so
a failure comes with the server's side of it. `pdtest.Logger(t)` is the same
thing as an `*slog.Logger` if you need to hand one somewhere else.

## 5. The two seams that make an answer comparable

Two things differ on every run, and both can be handed in:

```go
s, _ := bare.NewServer(db,
	bare.WithClock(pdtest.Clock()),
	bare.WithMinter(bare.MinterFunc(func(_ context.Context, entity string, _ uuid.UUID, _ bool) (uuid.UUID, error) {
		return ks[entity], nil
	})),
)
```

**`WithMinter`** decides the identifier a row gets. **`WithClock`** decides what
a timestamp holds; `pdtest.Clock()` answers `pdtest.At` — 2001-02-03 04:05:06
UTC — forever.

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

```go
ks := map[string]pdid.Id{
	"app.Tenant": pdid.MustParse("0199c3f4-2a10-8001-8a03-9f2e1c4d5b01"),
	"app.Robot":  pdid.MustParse("0199c3f4-2a10-8007-8a03-9f2e1c4d5b07"),
}
```

A counter means a test that adds rows in a different order gets different
identifiers — so adding a setup line at the top changes every recorded answer
below it. Answering off the entity's name makes the setup order irrelevant.

The key is `app.Tenant` and not `payday.Tenant`: payday's entities are generated
into **your** proto package. See [the schema guide](schema.md#7-adding-to-paydays-entities).

## 6. Golden files

With both seams handed in, the whole answer is comparable:

```go
t.Run("the robot", func(t *testing.T) { pdtest.Golden(t, robot) })
```

```sh
$ PDTEST_UPDATE=1 go test ./...
```

That writes `testdata/TestWhatever_the_robot.txtpb`; without it, the message is
compared against what is recorded and a mismatch is printed as a diff.

### Why the whole message

Because the fields a test asserts on are the ones somebody thought of. A
generated server answers with everything the schema declared, and the way that
answer goes wrong is a field nobody was asserting — an edge that came back
empty, a version that did not move, a column an overlay renamed. **A comparison
of the whole message is the only assertion that covers a field added after it
was written.**

### Two details

It is prototext rather than bytes, so a failure is a diff a person reads.

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

If that passes without the entity having said `tenanted:`, the entity is
global and you did not mean it to be.

## 8. The browser

The TypeScript half runs under vitest, and most of it does not need a server —
the store and the query layer are tested against a fake transport.

What does need one is the two halves agreeing, and that test starts a real Go
server and sends it bytes. A mock would agree with whatever the TypeScript
believes, which is the one thing not worth checking.

The sandbox — the whole app compiled to wasm, running in a page — is a test too,
skipped unless asked for:

```sh
$ PDTEST_SANDBOX=1 npm run test:sandbox
```

It costs a 65 MB build, a chromium download and a dev server, so it does not run
on every `npm test`. It runs in CI. The first time it was ever loaded in a page,
three things were wrong — no broker named, nothing seeded, no interceptors — and
every one of them compiled, linked and started.

## Where to go next

- [The server](server.md) — the stack a harness builds.
- [Permissions and the wall](permissions.md) — what the wall test above is
  asserting.
- [Declaring an entity](schema.md) — where the minter and the wall come from.
