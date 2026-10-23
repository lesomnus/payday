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
operation and the facts it writes, reads them back at a moment, says what
happened in the reader's language, corrects it, takes a reason out, and lets
what a tenant's window no longer reaches go.

- [1. Declaring a past](#1-declaring-a-past)
- [2. What an event holds](#2-what-an-event-holds)
- [3. Rows outlive the code that wrote them](#3-rows-outlive-the-code-that-wrote-them)
- [4. The lock](#4-the-lock)
- [5. Reading an event back](#5-reading-an-event-back)
- [6. Recording an operation](#6-recording-an-operation)
- [7. Facts](#7-facts)
- [8. Reading the past](#8-reading-the-past)
- [9. Saying what happened](#9-saying-what-happened)
- [10. Correcting it](#10-correcting-it)
- [11. Taking a reason out](#11-taking-a-reason-out)
- [12. Letting it go](#12-letting-it-go)

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

### Nothing else writes them

Your Event and Fact have one writer, and it is the package. The generated
`Audit` layer refuses every write of either that `pd.Past`'s store did not hand
the servers -- `Add` and `AddMany` of both, `Patch` and `Apply` of a fact --
however it arrives: a call over the wire, an operation of a batch, the ungated
stack, your own code. The answer is `PermissionDenied`, to every caller alike:

```
a past is written by the past package: an event by past.Begin, and a fact by the op it answers
```

A fact written by hand is a row no read of the past can be sure of -- not the
canonical JSON of its value, not withheld where it should be, superseding
nothing -- and an event whose `actor_id` the request says is who did it,
forged. So which of their verbs are internal is payday's to say, and not left
to your policy as an entity of your own is
([the server guide](server.md#except-the-pasts-which-payday-seals)).

- **Wherever the layer that records it is.** The store marks each context it
  hands the servers with a key only the generated `pd` package can make, and
  the mark goes down with the write: a layer above the gate records the past
  as one beneath `Audit` does. A layer beneath `Audit` is beneath its
  refusals, as it is beneath the trail's.
- **Only what it hands the servers.** The context `Begin` answers, and the one
  `pd.PastIn` hands its work, carry no mark: a row written by hand with either
  is refused like any other, on the very transaction the store writes on.
- **Your own store writes nothing.** A `past.Store` of your own making is
  refused as any other writer is; one that wraps `pd.Past`'s, as
  `past.WithClock` does, writes through it.
- **Reads are as they were**, and so are the writes the package makes beneath
  the servers: a reason taken out (§11) and what an expiry takes (§12).

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

## 9. Saying what happened

An event keeps no sentence (§2). The app says, for each kind and in each
language it is read in, what an event of it says -- once, where it starts -- and
a sentence is made from the event as it is read: in the reader's language and
zone, with the people in it named as they are called then.

```go
s := past.NewSentences("en") // what a reader of a language nothing is said in reads
s.TimeLayout("en", "Jan 2 15:04")
s.TimeLayout("ko", "1월 2일 15:04")

// An identifier as an event message holds one.
id := func(b []byte) pdid.Id { v, _ := pdid.From(b); return v }

past.Say(s, "en", func(r *past.Reading, ev past.EventRow, m *app.MachineServiced) string {
	return fmt.Sprintf("%s serviced %s, due %s",
		r.Name(id(m.GetBy())), r.Name(id(m.GetMachine())), r.Time(m.GetDue().AsTime()))
})
past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.MachineServiced) string {
	return fmt.Sprintf("%s: %s 정비, 다음 정비 %s",
		r.Name(id(m.GetBy())), r.Name(id(m.GetMachine())), r.Time(m.GetDue().AsTime()))
})
```

An answer made of events reads them with a reading of its own:

```go
r := s.Read(ctx, pd.PastNames(drv), locale, zone)
for _, ev := range events {
	e.SetSummary(r.Say(ev))
}
if err := r.Err(); err != nil {
	return nil, err
}
```

- **Which language.** `r.Say(ev)` says an event as the reader's locale says it,
  then its language -- `en` for `en-GB` -- then the default locale and its
  language. Where none of them says it, it is the event's kind and what its
  message holds, field by field, each identifier as itself:
  `machine.service {machine: 0199…, by: 0199…, mode: MODE_SERVICE}`. That names
  nobody -- nothing there knows which identifier is a person -- and no event is
  ever said as nothing. A sentence that answers empty is none, and the next one
  in that line says the event. `r.Time(t)` writes a moment in the reading's
  zone, as the locale the sentence is said in writes one: an English sentence
  said to a reader of French writes its dates as English does.
- **People as they are called now, things as they were.** A person is in an
  event by identifier (§2), and a sentence names them with `r.Name(id)`: as
  they are called when it is read, so somebody renamed reads by the new name
  and somebody forgotten by the pseudonym they were left with. A thing the
  message holds by value -- a serial, a tag, what it was called -- is said as
  the message holds it: that is a record, and nobody's data. `r.Name` of an
  identifier nothing names says the identifier, and of none -- the actor of the
  deployment's own work -- says nothing, for the sentence to say in its own
  words.
- **Every name said is on the access record.** A name put in a sentence is that
  person's data shown, and no declaration of the answer it goes out in can see
  it there. So `r.Name` puts whose it is on the access record of the call the
  reading is for (`access.Saw`), every time a sentence says it. A robot's name
  is nobody's, and records nothing.
- **`pd.PastNames(drv)`** is the `past.Names` `pd gen` writes into an app that
  declares a past: a row of any entity with an alias or a name, by
  `header.Of(row).Title()` -- its name, else its alias -- read through the wall,
  so that a sentence names nobody its reader could not read for themselves. A
  title read off a field the entity declares `personal`, a holder's, is whose
  data it says; one of a row gone, behind the wall, or of a kind with nothing
  to be called by is named by nothing. An app that names somebody another way
  -- a membership by the person record it is of -- writes its own: `Name(ctx,
  id)` answers the name and the rows it read it off, as whose data it is.
- **A reading is for one answer.** It asks each name once, however often its
  sentences say it, and the next answer is a reading of its own, which asks
  again. A name it could not read is said as its identifier and is the
  reading's `Err`: an answer asks it once its sentences are said, and answers
  the error rather than sentences that name nobody.
- **Every kind in every language.** `past.Unsaid(s, locale, pd.EventKinds)` is
  the kinds the locale's own language says nothing of -- not counting the
  default, which is a language the reader did not ask for. An app's test holds
  it empty, so that a kind it declares later is not read as its fields:

  ```go
  for _, l := range []string{"en", "ko"} {
  	require.Empty(t, past.Unsaid(s, l, pd.EventKinds), l)
  }
  ```

- **Rows from before the past.** An app that adopts its own table of events as
  it stood has rows that hold no message, and keep the sentence they were
  written with in a field of its own, which an overlay adds (§1).
  `s.Legacy(f)` says them: the past neither writes nor reads the app's own
  fields, so `f` reads them of the row `ev.Id` names, with `r.Context()`, and
  says what went wrong with `r.Fail(err)`. Without it, such a row is said as its
  kind.

## 10. Correcting it

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

## 11. Taking a reason out

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

## 12. Letting it go

A tenant keeps its past for as long as its **window** says, and what was over
before the window begins goes: `now` less the window is the cut. How long a
tenant's window is -- a plan, a contract, a setting of its own -- and whether a
legal hold is on it is your data, and you answer it, from the same source as
the trail's windows (see [one window](#one-window-for-the-past-and-its-trail)
below). `past.Expire` takes the rest, in one transaction:

```go
err := pd.PastIn(ungated, drv, content)(ctx, tenant, func(ctx context.Context, st past.Store) error {
	// Your own rows that name events first, on this transaction (below).

	_, err := past.Expire(ctx, st, tenant, past.Window{Keep: keep, Holds: holds}, time.Now(), past.Expiry{
		KeepKinds: []string{"robot.add"}, // the operation that made one
		KeepFacts: []past.FactKeep{{
			Domain: pd.RobotDomain,
			Field:  16,            // its status
			Values: []string{"4"}, // STATUS_SCRAPPED, which took it out of use
		}},
		Receipt: receipt,
	})
	return err
})
```

`pd.PastIn(ungated, drv, content)` opens the transaction, frames the context as
the deployment's work in the tenant -- nobody's, and of that tenant alone -- and
builds the store on it, through the stack with no wall: an expiry is the
deployment acting on itself, so hand it the ungated stack and never one a
caller reaches. `Expire` refuses a store on no transaction (`past.ErrNotInTx`)
and a context framed in another tenant than the one it expires.

### What goes

- **A fact** superseded by the cut -- a correction replaced it -- or one a later
  fact of its field replaced by then: one that had begun by the cut, was written
  down by it, and still stands. So what each field held when the window began
  stays, and what anything was at a moment inside the window reads as it did,
  as known now and as known at any moment inside it (`past.StateAt`). What it
  was before the window is what goes.
- **An event** that happened and was written down before the cut, of a kind no
  exception keeps, that **no row that stays names**: no fact it wrote or
  superseded, no event that is part of it, and no row of yours whose column says
  it names an event. The facts go first, so an event only they named goes with
  them, and an operation's event goes with the last of its children.
- **Nothing** while a legal hold is on, whatever the window says, and nothing
  for a window of zero, which is forever.

### What names an event

A column says it does: `(payday.field) = {event: true}`, on a `bytes` column of
`TYPE_UUID` ([the schema guide](schema.md#a-column-that-names-an-event)).
payday's templates mark the past's own -- a fact's `event_id` and
`superseded_by`, an event's `cause_id` -- and an app marks the ones of its rows
that name the operation that wrote them: a time row's, a stock movement's.
`pd gen` lists every one in the store (`Store.Pointers()`), and an expiry asks
each of them about each event before it takes it, so give each an index that
begins with it.

**Your own rows go first**, in the same transaction: what of them your rules say
is over, through your servers, so that the trail has each as the erase it is --
`enttx.Rebind(ungated, st.Driver())` is your stack on the expiry's transaction.
Then `Expire`, which finds the events only those rows named unnamed, and takes
them. Done the other way round, such an event stays until the next pass.

### What no window ends

- **`KeepKinds`**: the kinds of event that are a record whatever their age --
  the operation that made a thing, the one that disposed of it. A kind no
  message declares any more is one rows were written under once, and may be
  kept.
- **`KeepFacts`**: facts of one field of one kind of resource -- every value of
  it, or the ones listed -- that a later value does not make history: the
  status that took a thing out of use. Each value is the text a fact keeps, as
  `past.Encode` writes it -- `"retired"` for a string, `4` for an enum -- and one
  written any other way is refused rather than matching nothing. It keeps a fact
  from going for having been replaced; one a correction superseded goes as any
  other, since what was corrected was never the record.

### Each row on the trail

Neither Event nor Fact has an Erase, so the rows are deleted beneath the
servers: by identifier, a page at a time, read in the order an index keeps --
and each page put on the trail, in the same transaction, as taken by
`payday.past.expire` (`Expiry.Act` says another), by whoever the context is
framed as and filed under the tenant (`pd.RecordErased`). So a check of the rows
(`pd.Digests`) finds each as the trail says it is: gone. A delete done past this
is a row the trail says is there, which is what the check is for.

What a pass took is not there to take again: a second pass takes nothing. On
PostgreSQL an expiry holds the tenant until its transaction ends, so two of one
tenant take turns rather than taking the same rows side by side.

### A receipt

`Expiry.Receipt` makes the event an expiry records of what it took: a message of
your own, from `past.Expired` -- the cut, and how many facts and events went.
It is asked whenever a window applied, after the rows went, and `nil` records
nothing that time; the receipt is written through your servers like any event,
as the deployment's own. Say how many of what and never what they said: the
rows went, and a receipt that kept them would be where they went. An app whose
own rows went first says how many in the same message.

### Before a window is shortened

```go
x, err := past.Preview(ctx, st, tenant, next, time.Now(), expiry, map[pdid.Domain]past.Going{
	pd.StintDomain: func(cut time.Time) func(*sql.Selector) { return stint.And(stint.ValidToNotNil(), stint.ValidToLTE(cut)) },
})
```

`Preview` counts what `Expire` would take, and takes nothing: no row is deleted
or held, so a tenant's owner may ask it before a shorter window is saved, as
often as they like. It counts by the predicates an expiry deletes by, with the
chain of events an event that stays keeps -- which it says as one `WITH
RECURSIVE` statement. Your rows are still there when it asks, so it is told, by
the domain of each of your tables that name events, the predicate your own
expiry deletes that table's rows by; a table it is told nothing of keeps every
row, and every event one of them names. Write the predicate so that it holds
under a NOT: a comparison of a nullable column says `IS NOT NULL` too.

A **dry run** is `Expire` in a transaction you roll back: what it says is what a
run does, to the row, and what it took comes back with the rollback.

### The loop

```go
s.Spin = append(s.Spin, past.Expirer{
	Tenants: pd.PastTenants(client),          // every tenant with a past
	Window:  windows.Of,                      // yours, and the trail's from the same source
	In:      pd.PastIn(s.Ungated, drv, content),
	Every:   time.Hour,
	Expiry:  expiry,
})
```

`past.Expirer` is `Expire` in every tenant with a past, each in a transaction of
its own, every `Every`: a `spin.Spinner` called `payday.past.expire`, which a
lease runs in one process of the deployment. A tenant whose window cannot be
read keeps everything that pass, as the trail's does, and the rest go on; one
kept forever or under a hold opens no transaction. It is for an app whose past
is what it expires. One with rows of its own that name events runs its own loop
that takes those and then calls `Expire`, as above -- the way rove's does.

### One window for the past and its trail

The trail keeps the writes that made the past -- every event and fact was
written through the servers -- and its windows are answered per tenant too
(`trail.Policy.Tenants`). Answer both from **one** source: a trail that
outlives the past it records keeps, as values, what the past let go, and a
past the trail has forgotten the making of is history nobody can vouch for.
[The operator's guide](operator.md#3-the-trail-asks-it) has the shape.
