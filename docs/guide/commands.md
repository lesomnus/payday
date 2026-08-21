# Commands on your binary

Every app ends up needing the same commands — read a row, list a page, add one,
fix a typo in an alias — and every app has written them by hand. `payday/pdcmd`
builds them from what is already in the binary:

```go
t, err := pdcmd.New(to)
if err != nil {
	return err
}

root.Commands = append(root.Commands, t.Commands()...)
```

That is the whole of it. `robot get`, `robot ls`, `holder add`, `tenant patch`,
and so on for every entity your schema declares.

For what these commands are and why they are in `pdcmd` rather than in `pd`, see
[the server guide](server.md#8-the-commands). This page is how to use them.

---

## 1. The connection is yours, and it is opened late

`pdcmd.New` takes a `Connector` — one method, answering with a
`grpc.ClientConnInterface` and how to let it go. It does not dial, does not
authenticate, and reads no configuration file.

```go
type Connector interface {
	Connect(ctx context.Context) (Conn, func(), error)
}
```

That is not an omission. **Where to connect, as whom, and what that credential
may do are the three decisions that make an admin command safe or unsafe**, and
they belong to the deployment. A package that made them for you would be making
them the same way for every app.

### Why this and not a connection

Because of *when*. The tree is built while the app is assembling its commands —
before a flag has been parsed and before the configuration file has been read —
and the address to dial is in that file. An app handing over a connection would
have to open one before it knew where to.

`xli` reads configuration in a handler on the root, so by the time a leaf runs
the answer is there. `Connect` runs then:

```go
// remote is this deployment's data plane, over the wire.
type remote struct{ c *Config }

func (r remote) Connect(ctx context.Context) (pdcmd.Conn, func(), error) {
	// r.c is filled in by now: `pdcmd.Load` ran on the root.
	conn, err := grpc.NewClient(r.c.Server.Addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, nil, err
	}

	return conn, func() { conn.Close() }, nil
}
```

It is an interface rather than a function type because what goes in it is not a
callback: it is which address, which credential, and whether the server is in
this process at all. Those want a name and a comment saying why, carried to the
place they are read. payday ships no `ConnectorFunc` for the same reason.

Two more things follow from it, and neither is available to a stored
connection:

- **`--help` opens nothing.** The connector is asked only when a command is
  actually running. Printing usage or completing a word must not be able to
  hang on a server that is not there.
- **Something closes it.** The second answer is called when the command is
  done. A connection stored in a tree belongs to whoever built the tree, and
  nothing says when they are finished with it.

The credential rides on the context the root is run with:

```go
return root.Run(as.Provide(ctx), os.Args[1:])
```

### When you already have one

A test, or an embedded server over `bufconn`, has the connection before there is
a tree and keeps it for the life of the process. `pdcmd.Static` is the connector
for that, and it closes nothing:

```go
t, err := pdcmd.New(pdcmd.Static(conn))
```

### A command of your own reaches the same one

Chain `t.WithConn()` in front of it and read `pdcmd.MustConn(ctx)`. Opening its
own would be a second socket, a second credential to get right, and — for a
connector that hands out an in-process server — a second server, which is a
different database.

```go
t.Add("robot/resend", &xli.Command{
	Name: "resend",
	Handler: xli.Chain(t.WithConn(), xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
		c := app.NewRobotServiceClient(pdcmd.MustConn(ctx))
		...
	})),
})
```

It is idempotent, so chaining it costs nothing where a parent already has it.

One app can have several trees, and this is why. roster has an operator port and
a data port with different policies in front of them; a tree per connection is a
command tree per policy. An in-process server over `bufconn` hands back a
`*grpc.ClientConn` like any other, so an embedded deployment needs no different
code.

**The wall is untouched.** A command is a caller like any other: without a
credential the same call comes back `Unauthenticated`, from the server.

---

## 2. What you get, and what you do not

Six verbs, and only where the schema declared them:

| | |
| --- | --- |
| `get <REF>` | one row |
| `ls` | a page, with `--size` and `--next` |
| `watch [REF]` | the same rows, kept current — see [below](#watch-and-what-an-ending-means) |
| `add [NAME]` | a new row |
| `patch <REF>` | change one |
| `erase <REF>` | remove one |

`ls` exists only for an entity that declared `list:`, and `watch` only for one
that declared `watch:`. In payday's own test app four of nine entities have no
list, so `robot ls` is built and `cell ls` is not; `Tenant` has a list and no
watch, so `tenant watch` is not there either — and nobody decided any of that
twice. The commands are read from the descriptors your `.pb.go` files register
at init, so a verb exists exactly when the method does.

One is deliberately absent:

- **`apply`** is one of payday's two general writes and is
  [closed at the transport](server.md#why-patch-and-apply-are-closed-at-the-transport)
  unless an app opts in. A command for it would fail on every app that took the
  default, and an app that did opt in means something particular by it.

It can be mounted anyway — see [§5](#5-an-rpc-of-your-own).

### `watch`, and what an ending means

`watch` is `ls` kept current, and it is the only command here that does not end
on its own:

```sh
$ app robot watch @acme/arm-01
$ app robot watch -o table '{"filters":[{"ref":{"slug":{"alias":"arm-01","tenant":{"alias":"acme"}}}}]}'
```

**Every filter has to name a row.** A watch runs its filters again for every
write that touches the entity, for as long as the stream is open, so one with no
filters is the whole table forever — the one shape with no cap at all, and the
one the server refuses. That is why the reference is an argument: naming a row
on the line is the shortest request there is.

The first message is what is already there. After that, one message per write,
and a row that is **gone** — erased, or moved out of the filters this stream
named — arrives with its identifier and nothing else. There is deliberately no
way to tell those two apart; a stream that distinguished them would be telling
you about rows that stopped being yours.

```
$ app robot watch -o table @acme/arm-01
ACTION                        ALIAS    AGE   ID
-                             arm-01   3d    019ff7c9-8a1e-7c3d-9f00-2b6c1f0a4d51
/app.RobotService/Patch       arm-02   3d    019ff7c9-8a1e-7c3d-9f00-2b6c1f0a4d51
/app.RobotService/Erase       -        -     019ff7c9-8a1e-7c3d-9f00-2b6c1f0a4d51
```

The identifier is a column here rather than an `-o wide` one, for the row that
is gone: every other column is read through a value that is not there. The
header is printed once for the whole stream rather than between every event.

**It fails when the stream ends.** A watch has no backlog — a notification
reaches whoever is listening and is then forgotten — so a stream that stopped
and a stream where nothing is happening look exactly alike on the screen. A
command that returned quietly would leave you reading an empty one and believing
it, so this exits non-zero instead.

`--retry` is the other answer. It reconnects and **takes the snapshot again**,
which is the only thing that says what was missed; a reconnect that resumed
without it would leave you holding a row that is wrong until the next write
happens to correct it. Neither half is the default by accident: exiting is what
a script needs, and reconnecting is what somebody watching one wants.

```sh
$ app robot watch --retry @acme/arm-01     # reconnection notices go to stderr
```

`--retry` is about the connection going, and only that. A request the server
refused — a filter naming a row that is not there, a credential that may not
read it — fails the same way it would without the flag, because asking again
with the same words is a command that never stops and never works.

---

### Naming a row

Anywhere a command takes a `REF`:

```sh
$ app holder get 019ff7c9-8a1e-7c3d-9f00-2b6c1f0a4d51
$ app holder get @acme/alice
$ app tenant get @acme
```

The tenant travels with the alias because an alias is unique inside one and
names somebody else in every other. An entity that is not inside a tenant — a
Tenant itself — takes `@alias` alone.

`add` takes the same syntax for the name the new row is to have:

```sh
$ app holder add @acme/bob
```

---

## 3. The request is an argument

A flag per field would be a second copy of your schema, and the copy is what
goes stale the day somebody adds a field. So the typed arguments cover what is
written constantly — which row, what it is called — and everything else the
request can hold is protojson, merged over them:

```sh
$ app robot add @acme/arm-01 '{"cell":{"alias":"floor-2"}}'
$ app robot patch @acme/arm-01 '{"alias":"arm-02"}'
$ app holder add - < holder.json
```

The JSON wins where the two overlap, which is what makes it a complete escape
hatch: any field a command sets can be overridden without the command growing a
flag to unset it.

### Identifiers can be written as uuids

```sh
$ app holder add '{"tenant":{"id":"01a0011c-5078-8018-ad01-940b1f868ded"}}'
```

protojson reads `bytes` as base64, so without this the app would print an
identifier one way and refuse to be told it that way. Only an exact uuid is
converted — base64 of sixteen bytes is 24 characters ending in `==` and never
parses as one — so nothing you meant as base64 is reinterpreted.

`--in protojson` asks for the stricter contract instead. It is worth knowing what
that gets you: protojson accepts URL-safe base64, `-` is in that alphabet, and a
uuid string decodes to 27 bytes of nothing. The refusal then comes from the
server, about a value nobody wrote:

```
id: invalid UUID (got 27 bytes)
```

---

## 4. Output

`-o` on any command:

| | |
| --- | --- |
| `pretty` | the default. Identifiers as uuids, timestamps as times, a nested entity on one line |
| `json` | protojson with the identifiers as uuids |
| `protojson` | exactly protojson |
| `prototext` | exactly prototext |
| `name` | the identifier of each row, one per line |
| `table` | columns |
| `wide` | the same table, with the identifiers |
| `template=...` | `text/template` over the JSON |

```
$ app holder get @acme/bob            $ app holder ls -o table
id           01a00104-e385-8595-…     ALIAS   NAME        AGE
tenant       01a00104-e380-8aa9-…     admin   -           3d
alias        bob                      bob     Bob Vance   2h
name         Bob Vance
date_created 2026-08-14T16:04:52Z
```

**There are two JSONs on purpose.** `-o protojson` is base64 for every
identifier, because that is what protojson is and a script that feeds it back
somewhere depends on it. `-o json` is the same document with the uuids written
out, which is what a person or a `jq` needs. Changing the first would have been
the easy answer and would have broken the round trip.

`pretty` is the default rather than `prototext` for a reason worth stating: every
payday identifier is a `bytes` field, and prototext prints bytes as an escaped
string. The exact format was also the one nobody could use.

### Adding a format

An app's own format is not a lesser kind of format — it is the same one-method
interface the built-in ones implement:

```go
t, err := pdcmd.New(to, pdcmd.Options{
	Printers: map[string]pdcmd.Printer{
		"csv": pdcmd.PrinterFunc(func(w io.Writer, m proto.Message) error {
			for _, row := range pdcmd.Rows(m) {
				// ...
			}
			return nil
		}),
	},
})
```

`pdcmd.Rows` is what makes a printer work for both a `get` and an `ls`: a `Get`
answers with the entity and a `List` with the response that holds them, and this
hands back rows either way.

### Rendering one message your own way

`Options.Renderers` is a format for a single message type, used **only when the
person did not ask for a format**:

```go
pdcmd.Options{
	Renderers: map[protoreflect.FullName]pdcmd.Printer{
		"app.Robot": myRobotPrinter,
	},
}
```

Only then, and that line is worth keeping: `-o json` means JSON, and a renderer
that changed what `-o json` produced would make a script's output depend on which
app it ran against. kubectl draws the same line — its typed print handlers shape
the human-readable table and never the serialisations.

---

## 5. An RPC of your own

The five verbs are the ones every entity has. An operation that *means*
something is not one of them: payday closes the general writes on purpose, so
"put this robot in another cell" is an RPC you
[declare in an overlay](schema.md#an-rpc-of-your-own) and implement in
[a layer](server.md#3-writing-a-layer).

This is payday's own test app, and it is a real one:

```proto
// proto/ext/app/robot_svc.ext.proto
service RobotService {
  rpc Move(RobotMoveRequest) returns (Robot);
}

message RobotMoveRequest {
  RobotRef ref = 1;
  CellRef  to  = 2;
}
```

`Robot.cell` is field 3 — the set a row is in, payday's second narrowing. A
`Patch` could write it, which is exactly why the general writes are closed: a
rule attached to one field is a rule a general write walks past.

Nothing can generate a command for it, because nothing knows what it means. What
*can* be shared is everything around it — the reference argument, the trailing
protojson, `-o`, `--in`, the call, the printing — and that is `Unary`:

```go
c, err := t.Unary("app.RobotService.Move")
if err != nil {
	return err
}

if err := t.Add("robot/move", c); err != nil {
	return err
}
```

```sh
$ app robot move @acme/arm-07 '{"to":{"alias":"floor-2"}}'
```

**It does not care where the method came from.** A method you declared in an
overlay and a method `pd gen` wrote land in the same `ServiceDescriptor` — the
overlay is merged before generation — so there is no second path for a
hand-written RPC. It is the same property that lets the tree know `Robot` has a
`List` and `Cell` does not.

Two things it works out for itself:

- **Whether to take a `REF`.** A request with a `ref` field takes one; a request
  without takes only the trailing JSON. That is not a convention this package
  invented — it is the shape `pd gen` writes, and the shape
  `RobotMoveRequest` follows, because an RPC about a row names it the way
  every other RPC names one.
- **Whether it can be a command at all.** A stream is refused **while you are
  wiring it**, not when somebody runs it:

  ```
  pdcmd: app.RobotService.Watch: is a stream, which is not this shape
  pdcmd: app.RobotService.Nope: app.RobotService has no such method
  ```

This is also how the two absent verbs are mounted, if your app wants them:

```go
c, _ := t.Unary("app.RobotService.Apply")
t.Add("robot/apply", c)
```

---

## 6. Changing the tree

What goes in is an `*xli.Command` and so is everything that came out, so a
hand-written command is not a lesser kind of command here:

```go
t.Command("robot/ls")            // one of it, to mount elsewhere or to wrap
t.Replace("holder/erase", mine)  // this deployment means something else by it
t.Add("robot/transfer", mine)    // yours, beside the rest
t.Drop("audit")                  // the whole group, verbs included
```

`Replace` refuses a path that is not there rather than adding it, because a typo
would otherwise be a command that silently never runs — and a typo and a
deliberate addition look identical at the call site.

That everything is one type is deliberate, and it is the lesson of
`kubectl describe`: its typed describers are in-tree, a custom resource falls
back to a generic one whose output is noticeably worse, and so the fallback
exists and nobody wants it. `kubectl get` avoided it by letting the resource
declare its own columns.

---

## 7. Two payday apps in one process

`New` refuses when there is more than one, and names them:

```
pdcmd: 2 payday apps are in this process (hday.oasys, roster), so which one
this connection speaks to has to be said: use NewIn
```

This is not an edge case. Two payday apps can share a process whenever their
proto packages differ — which is
[why an app may choose its own](../SCHEMA.md) — and kamino2 is such a process:
it embeds roster, so `roster.Holder` and `hday.oasys.Holder` are both in the
registry. A connection cannot say which of them it speaks to.

So the app says:

```go
mine := pdcmd.NewIn(mineLocal{c}, "hday.oasys")
theirs := pdcmd.NewIn(theirRoster{c}, "roster")
```

Which is the right shape anyway: a process with two servers wants two trees.

---

## Where to go next

- [Declaring an entity](schema.md) — including
  [an RPC of your own](schema.md#an-rpc-of-your-own)
- [The server](server.md) — the stack that answers these calls, and the rest of
  the commands on your binary
- [Permissions and the wall](permissions.md) — what a command may do, which is
  never this package's decision
