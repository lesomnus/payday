# Commands on your binary

Every app ends up needing the same commands — read a row, list a page, add one,
fix a typo in an alias — and every app has written them by hand. `payday/pdcmd`
builds them from what is already in the binary:

```go
t, err := pdcmd.New(to)
if err != nil {
	return err
}

if err := t.Mount(root); err != nil {
	return err
}
```

That is the whole of it. `robot get`, `robot list`, `holder add`, `tenant
patch`, and so on for every entity your schema declares — at the root of your
binary, or under a command of your own if that is where you mount it. Both
words are the schema's: the entity's name, and the RPC the verb calls.

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
	// r.c is filled in by now: `cfg.Load` ran on the root.
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
- **Nor does a wrong flag.** On a command the tree builds, and one `Unary`
  builds, a `-o` that names no format, a template that does not parse, a
  `--tz` that names no zone and an `--in` that names no input format are
  refused as a wrong flag is (an `xli.UsageError`) before the connector is
  asked for anything: a typo does not open a database or ask who is at the
  terminal.
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

One app can have several trees, and this is why. A deployment that answers
operators on one port and machines on another has two policies in front of one
app, and a tree per connection is a command tree per policy. An in-process
server over `bufconn` hands back a `*grpc.ClientConn` like any other, so an
embedded deployment needs no different code.

**The wall is untouched.** A command is a caller like any other: without a
credential the same call comes back `Unauthenticated`, from the server.

---

## 2. What you get, and what you do not

Six verbs, and only where the schema declared them:

| | |
| --- | --- |
| `get <REF>` | one row |
| `list` | a page, with `--size` and `--next`; `--reverse` reads the order the other way round, and `--order NAME` in another order the schema declared. `ls` runs it too |
| `watch [REF]` | the same rows, kept current — see [below](#watch-and-what-an-ending-means) |
| `add [NAME]` | a new row |
| `patch <REF>` | change one |
| `erase <REF>` | remove one |

Each is the RPC it calls, in the case a command is typed in: `list` for `List`,
with `ls` its alias, the shorthand a shell is used to.

`list` exists only for an entity that declared `list:`, and `watch` only for
one that declared `watch:`. In payday's own test app, seven of the eleven
entities in the `app` package have no list, so `robot list` is built and
`cell list` is not; `Tenant` has a list and no watch, so `tenant watch` is not
there either — and nobody decided any of that twice. The commands are read
from the descriptors your `.pb.go` files register at init, so a verb exists
exactly when the method does.

**Paging is a cursor and not a page number.** A `list` with more to give answers
with a `next`, printed under the rows by the default format, and `--next` hands
it back: the page carries on from the last row of the one before. A request
with no cursor field for the flag to write — a hand-written service that is a
`List` in name only — is refused rather than sent, because a `--next` that fell
out silently would answer the first page again, which reads as a one-page list
and not as a mistake.

One is deliberately absent:

- **`apply`** is one of payday's two general writes and is
  [closed at the transport](server.md#why-patch-and-apply-are-closed-at-the-transport)
  unless an app opts in. A command for it would fail on every app that took the
  default, and an app that did opt in means something particular by it.

It can be mounted anyway — see [§5](#5-an-rpc-of-your-own).

### `watch`, and what an ending means

`watch` is `list` kept current, and it is the only command here that does not
end on its own:

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
and a row that is **gone** arrives with its identifier and nothing else. Erased
and moved out of the filters this stream named are one thing to a watch, which
is [the schema's rule](schema.md#6-watch--the-page-kept-current) rather than
this command's.

```
$ app robot watch -o table @acme/arm-01
ACTION                        ALIAS    AGE   ID
-                             arm-01   3d    019ff7c9-8a1e-8c3d-9f07-2b6c1f0a4d51
/app.RobotService/Patch       arm-02   3d    019ff7c9-8a1e-8c3d-9f07-2b6c1f0a4d51
/app.RobotService/Erase       -        -     019ff7c9-8a1e-8c3d-9f07-2b6c1f0a4d51
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
$ app holder get 019ff7c9-8a1e-8c3d-9f02-2b6c1f0a4d51
$ app holder get @acme/alice
$ app holder get @acme/alice#holder
$ app tenant get @acme
```

The tenant travels with the alias because an alias is unique inside one and
names somebody else in every other. An entity that is not inside a tenant — a
Tenant itself — takes `@alias` alone.

The name is a [slug](../../slug), read by the same code that reads one out of
a header or a certificate, so it behaves the same way everywhere: the parts
fold the way the database folded them on the way in — `@ACME / Alice` names
what `@acme/alice` names — and the optional `#kind` is an assertion, checked
rather than obeyed. Both written forms carry one — a slug in its `#`, an
identifier in its ninth byte — and the command is where the check runs,
because the server cannot: it narrows to rows of its own entity, finds
nothing, and answers a NotFound that is true and says nothing about the
mistake. `holder get @acme/arm-01#robot` is refused with both kinds in the
message, and so is `holder get` of a robot's identifier.

A unique key can be explicit too. For a generated `PartRef` with a `number`
key, these select the same row:

```sh
$ app part get number=MOTOR-0042
$ app part patch number=MOTOR-0042 '{"name":"Spindle motor"}'
```

The key is a member of the generated reference's `key` oneof. A single-field
index accepts its scalar value; a composite index accepts JSON:

```sh
$ app part get 'slot={"tenant":"@acme","number":"MOTOR-0042"}'
```

Use the index name from the generated `PartRef`, which may differ from its
column name. Misspelled keys are errors. There is no implicit default key for
a bare word. A `key=value` selects an existing row; creation fields belong in
an `add` request's JSON.

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
$ app robot add @acme/arm-01 '{"cell":{"id":"01a0010f-fd1e-8f1b-a60a-44424d2ababd"}}'
$ app robot patch @acme/arm-01 '{"alias":"arm-02"}'
$ app holder add - < holder.json
```

The JSON wins where the two overlap, which is what makes it a complete escape
hatch: any field a command sets can be overridden without the command growing a
flag to unset it.

The cell is named by identifier because that is the only way a `CellRef` can
name one: what a reference may say is what the schema made unique, and `Cell`
declares no unique index over its alias — see
[`alias`](schema.md#4-alias--the-name-a-person-writes).

### References can be written directly

A field whose type is a generated `XxxRef` accepts the same spelling as `REF`:

```sh
$ app holder add '{"tenant":"@acme","alias":"bob"}'
$ app part list '{"filters":[{"ref":"number=MOTOR-0042"}]}'
```

The rule also applies in repeated fields and map values. A one-field unique
index can be written as `{"ref":{"number":"MOTOR-0042"}}`; its expanded
protobuf form is `{"ref":{"number":{"number":"MOTOR-0042"}}}`. Composite
indices keep their object shape. Ordinary messages and free-form `Struct`
values keep their own JSON contract. `--in protojson` disables these shortcuts.

### Identifiers can be written as uuids

```sh
$ app holder add '{"tenant":{"id":"01a0011c-5078-8018-ad01-940b1f868ded"}}'
```

A field declared `(orm.field).type = TYPE_UUID` accepts this spelling regardless
of its name: `subject` and `account` work as well as `subject_id`. Generated
Add/Patch requests, reference keys and List/Watch filters retain that type.
After updating payday, run `pd gen` to update these descriptors. Storage options
such as `unique`, `nullable` and `default` stay on the entity. An explicitly
`TYPE_BYTES` field remains base64 even if its name ends in `_id`.

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
| `protojson` | exactly protojson, but for an empty list (below) |
| `prototext` | exactly prototext, but for an empty list (below) |
| `name` | the identifier of each row, one per line |
| `table` | columns |
| `wide` | the same table, with the identifiers |
| `template=...` | `text/template` over the JSON |

`pretty` prints a `bytes` field as an identifier where it is one -- its
`(orm.field)` says `type: TYPE_UUID`, as every entity's key and edge does, or
a field of a message that is no entity is called `id` or `<something>_id` --
and as `(n bytes)` where it is not. An element of a list or a value of a map
is printed the same, and is an identifier only where the field says so: a
plural such as `sites` is no name the rule reads, so an answer carrying a list
of identifiers says it on the field:

```proto
repeated bytes sites = 1 [(orm.field) = {type: TYPE_UUID}];
```

A field declared `(payday.field) = {secret: true}` is never printed element by
element: a list of them is how many there are, `(2 values)`.

A name that is none of these, or a template that does not parse, is refused
before the command asks for a connection, as a wrong flag is (an
`xli.UsageError`): nothing is opened or sent, by a command that writes as much
as by one that reads.

```
$ app holder get @acme/bob            $ app holder list -o table
id           01a00104-e385-8595-…     ALIAS   NAME        AGE
tenant       01a00104-e380-8aa9-…     admin   -           3d
alias        bob                      bob     Bob Vance   2h
name         Bob Vance
date_updated 2026-08-14T16:04:52Z
date_created 2026-08-14T16:04:52Z
```

**An empty list is a list.** `json` and `protojson` print `{"items": []}` for a
list with nothing in it, the key a list with rows has, `prototext` prints
`items: []`, and a template reads the `items` `json` writes. The encoders
write an empty repeated field as nothing, which made an empty list `{}`; both
libraries read the key back. `table` and `wide` print `(none)`, and `pretty`
and `name` print nothing.

**There are two JSONs on purpose.** `-o protojson` is base64 for every
identifier, because that is what protojson is and a script that feeds it back
somewhere depends on it. `-o json` is the same document with the uuids written
out, which is what a person or a `jq` needs. Changing the first would have been
the easy answer and would have broken the round trip.

`pretty` is the default rather than `prototext` for a reason worth stating: every
payday identifier is a `bytes` field, and prototext prints bytes as an escaped
string. The exact format was also the one nobody could use.

`Struct`, `Value`, `ListValue`, `Any`, and wrapper messages retain their protobuf
JSON representation in both `json` and `pretty`, including when nested in lists
or maps. An `Any` payload keeps its canonical protojson representation, including
base64 identifiers inside the packed message. Singular timestamps and durations
keep their existing human-readable form in `pretty`.

### Choosing table columns

Declare the columns with the entity, then run `pd gen`:

```proto
option (payday.entity) = {
  domain: 19
  columns: [
    {field: "model"},
    {field: "hours"},
    {field: "running"},
    {field: "spec.axes", title: "AXES"},
    {field: "id", wide: true}
  ]
};
```

This is the test app's `Machine`. Its normal table shows MODEL, HOURS, RUNNING
and AXES; `-o wide` adds ID. These columns replace the defaults. Titles default
to the uppercase field path. Paths can cross singular messages and must end in
a scalar, timestamp, duration or scalar wrapper. Generation rejects missing
paths, collections, secrets and a declaration with only wide columns. A missing
optional value prints `-`; present zero and false values remain visible.

Table padding measures terminal cells, including Hangul, other wide text,
combining marks and joined emoji. The gap between columns stays three cells.

Human-readable timestamps use UTC unless you select a zone:

```sh
app robot get --tz Asia/Seoul @acme/zone
APP_TZ=Asia/Seoul app robot list -o table
app robot get --tz UTC @acme/zone
```

`--tz` takes precedence over `<APP>_TZ`, then UTC. `<APP>` is the root command
name in uppercase with punctuation replaced by underscores, so `my-app` reads
`MY_APP_TZ`, and a generated command's help names it as it names any other
variable a flag reads: `[$MY_APP_TZ]`. The process `TZ` is not an implicit
default. For example,
`2026-10-31T23:30:00Z` displays as `2026-11-01T08:30:00+09:00` in Seoul.
Named zones also apply their daylight-saving rules for the instant shown.
Put flags before positional arguments, as with `-o`.

This setting applies to pretty, table and wide timestamps. Relative age
columns still show elapsed time. JSON, protojson, prototext and templates keep
their existing serialization. A bad zone is rejected as a wrong flag is, before
the command calls the server, including commands that write.

An application's own command can add `pdcmd.TimeZoneFlag()` and call
`pdcmd.Location(cmd)` before doing work. Its help names no variable, since a
flag is made before it knows which root its command is under. Use the resulting
`*time.Location` in its renderer, or pass it to a built-in printer:

```go
loc, err := pdcmd.Location(cmd)
if err != nil {
    return err
}
printer, err := pdcmd.PrinterNamed("pretty", pdcmd.PrintOptions{Location: loc})
```

`pdcmd.Table(wide, pdcmd.PrintOptions{Location: loc})` accepts the same option.
Without an option, standalone printers retain UTC.

A custom RPC response without entity metadata prints its scalar fields as
columns. If it holds exactly one repeated message field, those messages become
the rows. If it holds multiple such fields, no list is guessed: its own scalar
fields are shown. An empty list prints `(none)`.

Templates use the same values as `-o json`, including UUID identifiers:

```sh
$ app holder get -o 'template={{.id}} {{.name}}' @acme/bob
```

A custom command can reuse a built-in printer by name:

```go
printer, err := pdcmd.PrinterNamed("table")
if err != nil { return err }
return printer.Print(w, response)
```

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

`pdcmd.Rows` is what makes a printer work for both a `get` and a `list`: a `Get`
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

The six verbs are the general ones: what an entity can be asked for without
knowing what it is. An operation that *means* something is not one of them:
payday closes the general writes on purpose, so "put this robot in another
cell" is an RPC you
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
$ app robot move @acme/arm-07 '{"to":{"id":"01a0010f-fd1e-8f1b-a60a-44424d2ababd"}}'
```

The verb is the RPC's name, as the six are: `Unary` calls the command what the
method is called in the case a command is typed in — `Move` is `move`,
`AddMany` is `add-many` — and `Add` calls it by its path, so the path is where
a method of yours is named in kebab-case too.

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
  every other RPC names one. What kind of thing the reference names comes from
  the field's own type — the `ref` of `RobotMoveRequest` is a `RobotRef` — so
  `robot move @acme/arm-07#holder` is refused the way it is on every verb that
  takes one: five of the six, since `list` takes none.
- **Whether it can be a command at all.** A stream is refused **while you are
  wiring it**, not when somebody runs it — and so is a name that is not there
  at all:

  ```
  pdcmd: app.RobotService.Watch: is a stream, and this builds one call; a
  server stream is what `watch` is, and what it needed was an opinion about
  ending -- see cmdWatch

  pdcmd: app.RobotService.Nope: app.RobotService has no such method
  ```

A custom RPC may accept no reference, for example when it resolves the caller's
current job. Opt in to omission explicitly; protobuf field presence does not
say whether the handler requires a reference:

```go
c, err := t.Unary("app.JobService.Current", pdcmd.OptionalArg())
// app job current
// app job current @acme/job-42
// app job current '{"otherField":"value"}'
```

For an RPC that looks up a part by its number, choose that request field as the
positional argument:

```go
c, err := t.Unary("app.PartService.Resolve", pdcmd.Arg("number"))
// app part resolve MOTOR-0042
```

`Arg` accepts a singular field, using its protobuf scalar type. Strings retain
leading zeros. A message field takes JSON or, for a generated reference, the
reference syntax above. Combine `Arg` with `OptionalArg()` when the handler
allows omission. The trailing JSON still overrides the positional value.

This is also how the absent verb is mounted, if your app wants it:

```go
c, _ := t.Unary("app.RobotService.Apply")
t.Add("robot/apply", c)
```

---

## 6. Changing the tree

What goes in is an `*xli.Command` and so is everything that came out, so a
hand-written command is not a lesser kind of command here:

```go
t.Command("robot/list")          // one of it, to mount elsewhere or to wrap
t.Replace("holder/erase", mine)  // this deployment means something else by it
t.Add("robot/transfer", mine)    // yours, beside the rest
t.Drop("audit")                  // the whole group, verbs included
t.Alias("tenant", "ws")          // another word that runs it
t.Hide("fact")                   // there, and not in the help
t.After("robot/add", note)       // kept, and one more thing said after its call
```

A path reads the way the command line does, by name or by alias at each step:
`robot/ls` is the `list` that `app robot ls` runs.

`Replace` refuses a path that is not there rather than adding it, because a typo
would otherwise be a command that silently never runs — and a typo and a
deliberate addition look identical at the call site. What it puts there takes
the place as it was: its name, its aliases, and whether it is hidden.

That everything is one type is deliberate, and it is the lesson of
`kubectl describe`: its typed describers are in-tree, a custom resource falls
back to a generic one whose output is noticeably worse, and so the fallback
exists and nobody wants it. `kubectl get` avoided it by letting the resource
declare its own columns.

### A line after the call

Sometimes what your app means by a verb is the verb, and a line after it: once a
role is written, a note that it hands over more than its name reads as. That is
not a reason to replace the command — the replacement would be the same command
written again, with one line more. `After` is the line:

```go
err := t.After("role/add", func(ctx context.Context, c pdcmd.Call) {
	if wide(c.Answered.(*app.Role)) {
		fmt.Fprintln(os.Stderr, "note: this role hands over more than it reads as")
	}
})
```

The function is called once the command has made its call, the call succeeded,
and its answer was printed. It is handed the connection the call was made on,
still open, and the command's context, so a second question it asks is asked of
the same server as the same caller; the method, as `/app.RoleService/Add`; and
the request as it was sent and what came back. It reads those two and does not
change them.

It returns nothing, on purpose. The call was made, and a note that could not be
written is not the write failing: a write that was made and then reported as
failed is one somebody makes twice. So the command exits as it would have. The
note goes to stderr, where it does not join an answer somebody is piping into
something else.

It is not called when nothing was sent — a flag that is none, an `-o` that is
no format, a `--help` — nor when the server refused the call.

The path reads as every other does, by name or by alias, and `Alias` and `Hide`
keep what was said of it. It is for a command that makes one call and is the
tree's: a verb it built, or one `Unary` made, wherever you added it. Refused,
naming the path, where it is said:

- **A path that is none**: `pdcmd: robot/addd: nothing to run after`.
- **A stream**, `watch`, which has no one answer to be after.
- **A group**.
- **A command of your own**, put there with `Add` or `Replace`: it is yours,
  and says what it likes after its own call.
- **A second `After` on one command.** One function, and when two things are
  to be done it does both.

Like `Alias`, it is said before `Mount`. And a command it was said of is not
replaced: `Replace` refuses it, since the function would go with the command and
never be called.

---

## 7. Where it goes, and the words it answers to

### Mount it where you want it

`t.Mount(at)` appends the tree to any command: your root, for `app robot list`,
or a command of your own — `app api robot list` under one called `api`.
Nothing in the tree names what it is under. The usage line and the name in the
help are xli's, read off the path a command was run by, and no brief spells a
parent.

```go
root := &xli.Command{Name: "app", Commands: xli.Commands{version, config, serve}}
if err := t.Mount(root); err != nil {
	return err
}
```

**Mount last.** At the root an entity sits beside `version`, `config` and
whatever you wrote yourself, and xli runs the first command a word matches and
says nothing about the second — so an entity, or an alias, that is also one of
your commands would be one that silently never runs. `Mount` refuses that,
naming both, before it moves anything:

```
pdcmd: mount on app: "tenant" already runs tenant there, and is the tree's tenant
```

It can only see what `at` holds when it is called, and the tree cannot be
changed after it: a group added once it is mounted would be in the tree and not
on `at`, so every method that would change it refuses instead.
`root.Commands = append(root.Commands, t.Commands()...)` still works, and checks
nothing.

### Another word for a command

A command is called what the schema calls it. An alias is another word that
runs it:

```go
t.Alias("tenant", "workspace", "ws")
t.Alias("tenant/configure", "conf", "config")   // after the Add that put it there
```

`ls` for `list` is payday's own, and the help lists them together:

```
Commands:
    get                  get one Robot
    list,ls              list Robot
    watch                watch Robot
```

What is refused, where it is said rather than when somebody types it:

- **A path that is not there**, which would be an alias of nothing:
  `pdcmd: cell/list: nothing to alias`.
- **A word another command at the same level already answers to**, by its
  name or by an alias of its own:
  `pdcmd: robot/get: "ls" already runs robot/list, as an alias`. `Add` and
  `Replace` hold the words they bring in to the same rule, so `t.Add("robot/ls",
  mine)` is refused rather than added where nothing would reach it.
- **A word that cannot be typed as one**: empty, with a space or a `/` in it,
  or beginning with `-`, which is read as a flag.

A word the command already answers to is nothing to add, and not an error.

An alias is for typing, and that is all it is. Completion offers each command
once, by its name; the documentation xli writes (`xli.WriteMarkdown`,
`xli.WriteMan`) names it by its name; and the `#word` of a slug is the schema's
name and nothing else, so `@acme#ws` is no reference. An alias that abbreviates
the name reads back to it — `ls`, `conf` — and one that does not is a second
vocabulary to keep in step, so it is worth being one the app means to have.

**Why in Go, and not in the schema.** The schema is the contract every client
is generated from, and none of the others has a use for `ws`. The entity an app
most wants a word for is as often payday's as its own — `Tenant` is copied in
whole from payday's protos, so an option on it is not the app's to set — and
the check could not be made there anyway: the words that collide are the ones
beside your own `version` and `config`, which `pd gen` never sees. The longer
version is on `Tree.Alias`.

### Kept out of the help

```go
t.Hide("fact")
```

A hidden command is left out of `--help`, `--help-all`, the help an AI agent's
`--help` prints, the shell's completion and the documentation xli writes, and
it runs by name. It is for the rows an app keeps that other commands write — the
facts an operation records, the allocation a reservation makes: real, readable,
and not what a person reading `app --help` came for. Its own help still lists
its verbs, and whether a call is allowed is the server's to say, as it is of
every other.

`Drop` is the other answer, for what a deployment does not serve at all: a
dropped command is gone, and a hidden one is there and not shown.

---

## 8. Two payday apps in one process

An app that embeds another is one binary with two payday apps in it: `dispatch`
serves its own entities in the `fleet` package, and `directory`, the identity
store it embeds, serves its own. `New` refuses there, and names them:

```
pdcmd: 2 payday apps are in this process (directory, fleet), so which one this
connection speaks to has to be said: use NewIn
```

A connection cannot say which of them it speaks to: it is a
`grpc.ClientConnInterface` and knows nothing about schemas. The proto package is
what says — an app may choose its own, which is
[whose names these are](../schema.md#3-whose-names-these-are) — so the app names
it:

```go
mine   := pdcmd.NewIn(local{c}, "fleet")
theirs := pdcmd.NewIn(embedded{c}, "directory")
```

Which is the right shape anyway: a process with two servers wants two trees.

That two apps *can* share a process, and what they share when they do, is
[two apps in one process](packages.md#3-two-apps-in-one-process).

---

## Where to go next

- [Declaring an entity](schema.md) — including
  [an RPC of your own](schema.md#an-rpc-of-your-own)
- [The server](server.md) — the stack that answers these calls, and the rest of
  the commands on your binary
- [More than one proto package](packages.md) — when a tree has to be told which
  app it is for
- [Permissions and the wall](permissions.md) — what a command may do, which is
  never this package's decision
