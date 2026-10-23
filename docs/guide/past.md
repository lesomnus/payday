# Keeping a past

Some of what an app knows is history people read in the product: what was done
to a thing, by whom, when and why, and what the thing was from when. payday
keeps it for an app that asks, in two tables of its own (payday#151):

- an **Event** for each operation the app records -- its kind, who asked, when
  it happened, why, what it was about, and a message the app declares for that
  kind of operation;
- a **Fact** for each field of a resource from a moment on, pointing at the
  event that wrote it.

This page is declaring a past, the messages an event holds, the rules that keep
old events readable, the lock that holds an app to them, and the registries a
server and a page read events with; then the `past` package, which records an
operation and the facts it writes, reads them back at a moment, corrects them,
and takes a reason out. Making a sentence of an event and letting history
expire come in the parts of payday#151 after this one.

- [1. Declaring a past](#1-declaring-a-past)
- [2. What an event holds](#2-what-an-event-holds)
- [3. Rows outlive the code that wrote them](#3-rows-outlive-the-code-that-wrote-them)
- [4. The lock](#4-the-lock)
- [5. Reading an event back](#5-reading-an-event-back)
- [6. Recording an operation](#6-recording-an-operation)
- [7. Facts](#7-facts)
- [8. Reading the past](#8-reading-the-past)
- [9. Correcting it](#9-correcting-it)
- [10. Taking a reason out](#10-taking-a-reason-out)

## 1. Declaring a past

Say it once, on the file that says the package is yours, with the two domains
the tables take:

```proto
// proto/app/app.proto
option (payday.app) = {past: {event: 17, fact: 16}};
```

Two numbers of your own, in `7`–`247`, different, and held by no other
entity: a table every app has gets a number payday fixes, and a table an app
opts into is numbered by the app. `pd gen` writes `proto/app/payday/event.proto`
and `fact.proto` and everything generated from them on every run, and takes
them out on the first run after the line goes; a migration makes the tables.

What the two hold, what an overlay may add to them and what is refused is the
schema guide's [§7](schema.md#your-past-paydays-event-and-fact).

## 2. What an event holds

An event keeps no sentence. It keeps a **message** you declare for its kind of
operation, in its `value`, and a sentence is made from that when the event is
read -- in the reader's language, in the tenant's zone, naming the people in
it as they are called then. A sentence written down at the time could do none
of that: it is frozen in one language and one zone, and it goes on naming
somebody by the name they had.

Mark the message with `(payday.event)`:

```proto
// What a robot's move holds.
message RobotMoved {
  option (payday.event) = {kind: "robot.move"};

  bytes robot = 1;
  bytes from = 2;
  bytes to = 3;
  Pace pace = 4;
}

// Its kind is its full name, `app.RobotStopped`, since it names none.
message RobotStopped {
  option (payday.event) = {};

  bytes robot = 1;
  google.protobuf.Timestamp at = 2;
}
```

The **kind** is what the event's rows are written under, which a list of events
is filtered by and which says what `value` holds. It is the message's full name
unless the option says one, and like a field's number it is chosen once. A
message may be declared anywhere in your files, inside another message too.

### What one may hold

Scalars, enums, messages of your own -- declared inside it or not -- lists and
maps of any of them, and protobuf's own types: a `Timestamp`, a `Duration`.
Each of the things below is refused by `pd gen`, because a row would keep it
for as long as the event is kept:

| | |
| --- | --- |
| a `(payday.field)` anywhere in it, or in a message it holds | an event names people by identifier only |
| a field holding an entity | a row kept whole, its personal fields included |
| a field holding a message of a generated contract -- a `RobotRef` | the API's message, which moves with the entity; a Ref carries a slug, which is somebody's name for the row |
| a `google.protobuf.Any` | a message nothing declared, which no rule here can be held to |
| an entity that says it is an event message | a table where a row's value was meant |
| two messages of one kind | a row that says it holds either |
| an event message in an app that declares no past | an event with no table to be kept in |

### People, by identifier

An event names whoever it is about -- the holder who did it, the person it was
done to -- by identifier, and holds nothing of theirs. That is what lets the
trail, the access record and erasure leave history alone: there is nothing of a
person in it to find. A sentence names them when it is read, as they are called
then, and says so to the access record.

The one free text an event keeps is the Event's own `reason`, why it was done,
in the words of whoever asked. Nothing can tell whether somebody is named in
it, so it is declared `personal`: the trail records that it was written, never
what it said.

## 3. Rows outlive the code that wrote them

A row of an event keeps its value as the **canonical protobuf JSON** of its
message, `{"@type": "type.googleapis.com/app.RobotMoved", "robot": "…"}`, and
reads it back **by name**, for as long as the tenant keeps its history --
which is longer than any version of the code. So:

- **An event message is never deleted.** A row whose type is gone does not
  read, and a list of events that includes it fails with it.
- **A field is never removed, and never changes its JSON name, its kind, the
  message or enum it holds, or whether it is a list or a map.** A rename is
  allowed only if `json_name` keeps the name rows were written with:

  ```proto
  bytes moved_by = 2 [json_name = "by"];
  ```

- **A value of an enum one holds is never renamed or removed.** JSON writes an
  enum by its name.
- **A kind is never changed.**
- **A message ships one release before anything writes it.** A binary older
  than the message cannot read rows of it, so a rollback across the release
  that started writing one is a rollback to a server that fails on them.

Adding is always fine: a message, a field, a value.

## 4. The lock

`buf breaking` says some of that and forgets it: it compares one change with
the one before, so a deletion that lands is the next change's baseline. Rows do
not forget. So an app that keeps a past has a **lock**,
`proto/<pkg>/payday/past.lock`, beside the copies of payday's entities: the
record of every event message it has declared, and of what a row of each holds.
In part:

```json
{
  "events": {
    "app.RobotMoved": "robot.move"
  },
  "messages": {
    "app.RobotMoved": {
      "1": {"json": "robot", "kind": "bytes"},
      "4": {"json": "pace", "kind": "enum", "type": "app.Pace"}
    }
  },
  "enums": {
    "app.Pace": {"0": "PACE_UNSPECIFIED", "1": "PACE_WALK", "2": "PACE_RUN"}
  }
}
```

Each event message by full name with its kind; each field by number, with its
JSON name, its kind, the message or enum it holds, and whether it is a list or
a map -- of a map, the kind of its key; every message an event message holds,
recorded the same way; and every enum one holds, by number, with the name a row
is written with. Protobuf's own types are named and not looked inside.

`pd gen` builds the schema, holds it to the lock and writes the lock back with
what is new added. It **only adds**. A schema that lost or changed anything in
the lock is refused, before any code is generated from it, with a line for each
thing:

```
the event messages, held to the past's lock: proto/app/payday/past.lock holds
what this schema no longer declares as it was. ...

  app.Pace: 2 was PACE_RUN, and is PACE_RUNNING now
  app.RobotMoved (2): was written as "from", and is "origin" now
```

Commit it. `pd gen --check` regenerates it like everything else it writes, so
a lock behind its schema fails CI the way a stale generated file does.

**Taking something back is a hand edit**, of the schema and the lock together,
in one change a reviewer reads. It is right for one thing only: a type no
deployment ever wrote a row of -- a message added on a branch and dropped before
it shipped. For anything a row exists of, it is a row that stops reading.

An app that stops declaring a past keeps its lock: rows of the past may still
be in the database, and a past declared again is held to what they were
written with.

## 5. Reading an event back

**In Go**, `pd gen` writes `pd.EventKinds` into `server/pd/pd.g.go` of an app
that declares a past: every event message by its kind.

```go
typ, ok := pd.EventKinds[ev.GetKind()]
if !ok {
	// A row written before its kind had a message.
}

v := typ.New().Interface()
if err := ev.GetValue().UnmarshalTo(v); err != nil {
	return err
}
```

**In TypeScript**, `pd gen --ts` writes `ts/gen/events.ts`, for every app:

```ts
import { events, kinds } from '../gen/events.js'

anyUnpack(ev.value, events)                // the message, whichever kind it is
anyUnpack(ev.value, kinds['robot.move'])   // typed as RobotMoved
```

`events` is a protobuf-es registry of every event message, and `kinds` is each
by its kind. An app with no past has both, empty.

**Hand the registry to the transport.** Connect's JSON names the message an Any
holds by its type, and protobuf-es reads one only with that type registered --
so without it, a List of events fails whole on the first row that holds a
value. `pd new` writes it into both transports it makes:

```ts
const transport = createConnectTransport({
	baseUrl: ADDR,
	jsonOptions: { registry: events },
})
```

A page made before this has transports without it; add the line there. See
[the page](client.md#the-registry-a-connect-transport-is-handed).

## 6. Recording an operation

`pd gen` writes `pd.Past` into an app that declares a past: its Event and Fact
as the `past` package writes and reads them, through the app's own servers --
so the wall, the trail and every recorder meet a write of the past as they
meet any other.

```go
drv, tx, err := dialect.BeginTx(ctx, s.drv)
if err != nil {
	return err
}
defer tx.Rollback()

next, err := enttx.Rebind(s.Next(), drv) // what the operation's own writes go through
if err != nil {
	return err
}
st, err := pd.Past(next, drv, s.content)
if err != nil {
	return err
}

ctx, op, err := past.Begin(ctx, st, past.Event{
	Subject: robot,
	Reason:  req.GetReason(),
	Value:   app.RobotMoved_builder{Robot: robot.Bytes(), From: from, To: to}.Build(),
})
if err != nil {
	return err
}

// ... the operation's own writes, through `next` ...

if err := op.Set(ctx, past.Fact{Subject: robot, Values: past.Changes(before, after)}); err != nil {
	return err
}

return tx.Commit()
```

- **One transaction.** An event and everything it is about commit together or
  not at all, so the transaction is the operation's to begin and `pd.Past` is
  built on it: `next` is put on `drv` there, and a stack with a layer that
  cannot be put on another driver is an error rather than a write outside the
  transaction. `Begin` refuses a store on no transaction (`past.ErrNotInTx`).
  A layer that records operations holds the driver its stack talks to, as
  apptest's `server/core` does, and writes `WithDriver` to carry it: inside a
  batch it is the batch's transaction, and the operation joins it.
- **What `Begin` reads rather than is told.** Who did it and in which tenant
  from the caller's frame -- a call with none is refused, and so is a frame of
  no tenant -- the trace from the span, the kind from the message's own
  `(payday.event)`, which the app's schema must declare too. The moment it is
  written down is taken by `Begin`, to the microsecond; `Event.At` says when it
  happened, when that is another moment.
- **The subject is of the caller's tenant.** One of another tenant is refused as
  not found, whatever path the row reaches its tenant by; a row behind no wall
  is everybody's to be about. An identifier that names no row is taken at its
  word -- an operation records its event before the row it makes -- so read the
  subject through the wall first, as an operation does anyway: this is the
  check behind that one.
- **Part of another.** An operation another one calls in the same transaction,
  with the context `Begin` answered, is part of it: its event's cause is the
  caller's. One begun on a context an operation left behind after it committed
  is not. `past.Detach(ctx)` says an operation is part of nothing, in the same
  transaction too.
- **Many things.** An operation over many records an event of its own and a
  child for each thing it changed: `op.Child(ctx, ev)` for one, and
  `op.Batch(ctx, children)` for many -- the events a page of AddMany at a time,
  then their facts the same way, every subject checked in one read a page at a
  time. Each child is an `*Op` of its own, whose facts point at it.
- **Sent twice.** A client that may send an operation twice names it:
  `past.Event{Id: op}`, an event's identifier. `Begin` holds that identifier
  until the transaction ends -- on PostgreSQL a copy sent while the first runs
  waits for it, on SQLite the one writer does the same -- and answers
  `past.ErrDone` when the event is there: the operation happened, and the copy
  writes nothing. `op.Made(domain, n)` names the `n`th thing it makes after it,
  so a copy names them alike. Take any lock an operation waits at before
  `Begin`, so that one that waited is written down after the one it waited for.

`past.WithClock(st, now)` tells a store the time by an app's own clock: the
servers' (`bare.WithClock`), or a test's.

## 7. Facts

A **fact** is one statement about one resource, its subject: from a moment on,
these of its fields held these values -- `{status: "repair", firmware: "2.4"}`
from 9/15. It is written as a row for each field, in one AddMany, every row
carrying the statement's event, subject and moment, and read back as the
statement it was.

```go
op.Set(ctx, past.Fact{
	Subject: machine,
	From:    serviced,                      // when it became true; the operation's moment when zero
	Values:  past.Changes(before, after),   // what the write changed
})
```

`past.Changes(before, after)` is every field `after` holds differently from
`before`: set, cleared, or -- of a map -- an entry set or gone. `nil` before is
a row made. The key, the version and a field that cannot change once written --
an immutable field or edge, the tenant and `date_created` among them -- are no
part of what a thing was, and never a fact. By hand, `past.To(fd, v)`,
`past.Entry(fd, key, v)` and `past.Cleared(fd)`, of the subject's own field
descriptors.

**A row is the field's number, the map entry's key, and the one value as JSON**,
written one way for each value so that two facts of one value are equal as
text (`past.Encode`):

| | |
| --- | --- |
| a string, an object, a float | as RFC 8785 writes them: the fewest escapes, keys in UTF-16 order, a number as ECMAScript writes it (`0` for -0; `"NaN"`, `"Infinity"`, `"-Infinity"` as protobuf's JSON writes those) |
| an integer | its exact decimal, a 64-bit one too |
| an enum | its number, which a renamed value keeps |
| bytes; a `TYPE_UUID` column and an edge | standard base64; the identifier as its canonical string |
| a `Timestamp` | RFC 3339 in UTC, 0, 3, 6 or 9 digits of a second |
| any other message | an object keyed by field number, which a renamed field keeps |
| a list | one array |
| a map | a fact for each entry |
| nothing | `null`: the field cleared |

A field with no presence holds its zero when it is not set, so a count of 0 is a
count; a field with presence, a list, a map, an identifier or an edge holding
nothing is `null`.

**A personal or secret value is withheld.** A fact of a field its subject's
entity declares `personal` or `secret` records that the field changed and never
what to: its value is empty, which is no JSON at all, and `null` when the field
was left holding nothing. A map's keys can be somebody's as much as its values,
so a change to a withheld map is one fact of the field, with no key.

**A value said again for a moment replaces it.** `Set` supersedes, field by
field, the facts standing for that field of that subject from exactly the same
moment -- by the operation that said it again -- and nothing else: a fact from
another moment stands, and says what the field was from then. A subject is a
row of the operation's tenant, or of no tenant; one of another, and an
identifier that names no row, are refused.

## 8. Reading the past

Every read is narrowed to the tenants the caller may see, as a server's reads
are.

```go
states, err := past.StateAt(ctx, st, []pdid.Id{robot}, at, nil)    // as known now
states, err := past.StateAt(ctx, st, []pdid.Id{robot}, at, &known) // as it was known then

was := &app.Robot{}
err = states[robot].Into(was)

changed := past.Diff(states1[robot], states2[robot])
```

- **`StateAt(subjects, at, known)`** is what each subject was at `at`: for each
  field, the fact that holds from latest by then. By what is known now -- the
  facts no correction or later fact replaced -- or, with `known`, by what had
  been written down by then and not yet replaced: what the system said, at the
  moment somebody looked. Where a robot was, recorded on 10/1, moved on 10/5,
  and the move corrected on 10/9 -- the example rove's design gives (§3.3):

  ```
  10/1 recorded: in A from 9/1          #1 A from 9/1
  10/5 recorded: moved to B on 9/15     #2 B from 9/15
  10/9 corrected: the move was on 9/12  #2 superseded, #3 B from 9/12

  StateAt(9/13)          = B (#3)
  StateAt(9/13, 10/6)    = A (#1)
  StateAt(9/20, 10/2)    = A (#1)
  ```

- **`State.Into(m)`** puts a state into its message: `m` is reset, and holds the
  subject's identifier and each field the state holds a value of -- not a
  withheld one, which it leaves empty.
- **`Diff(before, after)`** is what changed between two states, field by field.
- **`Facts(query)`** reads facts back as the statements they were written in,
  regrouped by event, subject and moment -- every row carries those three -- as
  `Known` takes them: standing now, as they stood at a moment, or every one
  ever written.
- **`Timeline(subject, known)`** is one subject's history, newest first: its
  statements, each with the event that wrote it, and the events about it.
- **`Events(query)`** reads events back by identifier, subject or cause. An
  event's reason is personal and no RPC answered with it, so one read with a
  reason is a row of the access record of the call that read it
  (`access.Saw`). What a page shows of it is an RPC of the app's, whose answer
  declares the field `personal` too.

## 9. Correcting it

Nothing is changed in place. A correction supersedes what it corrects and
writes what is right as the correcting operation's, so what the system said
before stays readable as it was known then.

| | |
| --- | --- |
| `op.Correct(ctx, fact, from)` | one field was true from another moment: its fact is superseded and written again from `from` -- a statement of its own, the correction's |
| `op.Set(...)` of the same moment | a value that was wrong: the right one, from the moment the wrong one said, supersedes it |
| `op.Retract(ctx, fact)` | a fact that never held: superseded, and nothing in its place |
| `op.CorrectEvent(ctx, event, from)` | "that operation happened on 9/28": everything it wrote that still stands, written again from `from`, a statement for each subject |
| `op.RetractEvent(ctx, event)` | an operation that never happened: everything it wrote that still stands, superseded |

A correction is an operation like any other, with an event message of the app's
saying what it corrected. A supersession is held to the version of the row it
read, so two operations that read one fact cannot both replace it.

## 10. Taking a reason out

An event's reason is the one free text the past keeps, and nothing can tell
whether somebody is named in it -- which is why the Event declares it
`personal`, so the trail records that it was written and never what it said.
When a person asks for a text that names them to be taken out:

```go
err := past.Redact(ctx, st, event) // in a transaction, as Begin is
```

An event has no Patch, so this is a write beneath the servers, and it is on the
trail as the act it is, `payday.past.redact`, by whoever the context is framed
as: the line keeps the event's digest as it is now, and its row's seal is made
again of it (`pd.Record`). So a check of the rows (`pd.Digests`) finds the event
as the trail says it is -- and the same blanking done in the database past the
servers is a row that differs, which is what the check is there to find.
