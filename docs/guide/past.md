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
server and a page read events with. Recording events and facts, reading facts
at a moment, making a sentence of an event and letting history expire are the
`past` package, which comes in the parts of payday#151 after this one; until
then the two are entities like any other, written through their generated
servers.

- [1. Declaring a past](#1-declaring-a-past)
- [2. What an event holds](#2-what-an-event-holds)
- [3. Rows outlive the code that wrote them](#3-rows-outlive-the-code-that-wrote-them)
- [4. The lock](#4-the-lock)
- [5. Reading an event back](#5-reading-an-event-back)

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
