# Migrating

What an app has to change when payday does. Newest first, and each entry says
how to tell whether it applies to you.

## An app may number payday's Archived and Access

**Applies to an app with an entity of its own at domain 5 or 6**, which `pd
gen` has refused since payday's `Archived` took 5 (2026-10-11) and its `Access`
6 (2026-10-21) -- *two entities declare one domain*. It keeps its entity where
it is, and gives payday's a number of its own (payday#163). **Every other app
changes nothing**: with nothing declared the two stay at 5 and 6, and a
regeneration brings only payday's `app.proto` with `payday.Domains` in it.

- **To declare them**, move the app's `buf.build/payday/payday` pin to a `dev`
  that has `payday.Domains` (`buf dep update`), and say it on the file that says
  the package is the app's -- in the one `option (payday.app)`, beside a past if
  it declares one:

  ```proto
  option (payday.app) = {domains: {archived: 240, access: 241}};
  ```

  Either may be said alone, the other staying at payday's number. Each is in
  7..247, the two different, and neither held by another entity or by the
  past; only these two are numbered, and a `domains:` naming another of
  payday's -- `owed:`, `row_seal:` -- is refused. `pd gen` writes the copies at
  these numbers and everything generated follows; an entity at 5 or 6 with
  nothing declared is now refused with this line, at numbers nothing holds.
- **Nothing to migrate.** An app that numbers either has no row of it under 5
  or 6: with an entity at one of them it could not generate with the table at
  all. The migration that takes it to this payday makes the two tables, and
  their rows carry the declared numbers from the first.
- **Choose them once.** A domain is in every identifier of the rows it names,
  so the line is not taken out again once a row of either is written: the
  rows would be under a number nothing registers. The archive files the access
  record as `access` whatever its number, and `audit.by.access` names it so.
- **A pin from before this** answers the declaration with `cannot resolve
  message field name for payday.App`, and `pd gen` says that is the pin.
- `pd entity add` skips the declared numbers, and does not count them as the
  highest of the app's own.
- [The schema guide's §7](guide/schema.md#when-yours-hold-5-or-6-paydays-archived-and-access)
  says how; [the generation contract](schema.md#domains-come-from-the-schema)
  why these two and no others.

What the apps on payday change:

- **kamino** keeps `Robot` at 5 and `Model` at 6, whose numbers are in every
  robot's identifier and in its device certificates, and declares both.
- **khala** keeps `Robot` at 5 and declares `archived:`. Its own entities at
  248..254 move into its range on its side, which is not this: Owed and RowSeal
  keep 254 and 255.
- **rove** and **roster** have nothing at 5 or 6, and change nothing.

## `pd doctor` asks what an app's past gets wrong

**Applies to an app that declares a past**, which `pd doctor` now asks four
things of -- the seventh part of payday#151. Nothing regenerates, `pd gen`
refuses nothing new, and there is no migration; `pd doctor` exits non-zero on a
schema its lock refuses, which `pd gen` stops on. An app with no past is asked
nothing new.

- **A lock out of step with its schema.** The comparison `pd gen` makes of the
  event messages and `past.lock`, made without writing it. A schema the lock
  refuses is said as `pd gen` refuses it, as a finding that stops a generation.
  A lock that does not hold all the schema declares -- not there beside event
  messages, or committed without what a change added -- says what it lacks: run
  `pd gen`, and commit it.
- **A column named for an event that does not say it names one**: a `bytes`
  column of `TYPE_UUID` called `event_id` or `…_event_id`, not named for an
  entity of the app's, without `(payday.field) = {event: true}`. The expiry asks
  only the columns that say so. Say it, and index it.
- **A field of an event message named for something about a person**, in the
  message or in one it holds: `email`, `phone`, `first_name`, the words `pd
  doctor` asks an entity's fields about. An event names people by identifier;
  hold that instead.
- **A page whose transport cannot read an event**: a file of `ts/src` that makes
  a transport that speaks JSON and says `jsonOptions` nowhere, in itself or in a
  module of the page it imports. Hand it the registry, as the second part's
  entry below says: `jsonOptions: { registry: events }`.
- [Keeping a past](guide/past.md#13-what-pd-doctor-asks) is the guide.

What the apps on payday change:

- **rove** keeps no past of payday's yet. When it adopts one, `pd doctor` asks
  about the `event_id` of its time rows and stock movements until they say
  `event: true`, as the fifth part's entry has them do, and names
  `ts/src/api.ts`, whose `createConnectTransport` is handed no `jsonOptions`.
- **roster** keeps no past, and changes nothing.

## Only the `past` package writes the past

**Applies to an app that declares a past**, which regenerates -- the sixth part
of payday#151 -- and whose `Audit` layer then refuses every write of its Event
and Fact that `pd.Past`'s store did not make. No schema changes and no
migration; an app with no past regenerates nothing new.

- **A write by hand is `PermissionDenied`**: `EventService` `Add` and
  `AddMany`, `FactService` `Add`, `AddMany`, `Patch` and `Apply`, from a caller
  over the wire, inside a batch, through `Ungated`, or from your own code
  through the stack -- *a past is written by the past package: an event by
  past.Begin, and a fact by the op it answers*. Look for each of those calls,
  and those methods in a batch, a seed or a tool: each moves to `past.Begin`
  and the `*past.Op` it answers, and a test that needs a row as one was before
  the past writes it through a server built without the layers
  (`pd.NewSink`).
- **A layer that records the past may be anywhere in the stack**: the store
  marks what it hands the servers for the `Audit` layer, and the mark goes down
  with each write. A layer beneath `Audit` writes past it, as it writes past
  the trail's refusals. The store marks nothing else -- not the context
  `past.Begin` answers, nor the one `pd.PastIn` hands its work -- and a
  `past.Store` of your own making writes nothing.
- **Reads are as they were**, and so are the package's writes beneath the
  servers, `past.Redact` and `past.Expire`'s.
- [The server guide](guide/server.md#except-the-pasts-which-payday-seals) says
  why payday decides this one, and [keeping a
  past](guide/past.md#nothing-else-writes-them) what it means for an app.

What the apps on payday change:

- **rove** keeps no past of payday's yet. When it adopts one, its domain
  layer's hand-written writes of its history move to the package in the same
  change -- `t.next.Event().Add` in `begin` and `child`, `addFact`'s
  `Fact().Add` and `AddMany`, `supersedeFact`'s `Fact().Patch`, and
  `register`'s `AddMany` of both -- or the first of them is refused: the
  layer is above the gate, so every write it makes meets `Audit`. Its `tenant
  import` writes the tables through ent, beneath the servers, and is not
  touched.
- **roster** keeps no past, and changes nothing.

## A tenant's past is let go by its window

**Applies to an app that declares a past**, which regenerates -- the fifth part
of payday#151 -- and gets a migration: payday's Fact gains an index on
`superseded_by`, which the expiry asks of every event it would take. An app with
no past regenerates nothing new. It needs a `buf.build/payday/payday` pin with
`(payday.field).event`, since payday's Event and Fact now say it: until the pin
moves (`buf dep update`), the copies are refused with `cannot resolve message
field name for payday.Field`, and `pd gen` says that is the pin.

- **A column of yours that names an event says so**: `(payday.field) = {event:
  true}`, on a `bytes` column of `TYPE_UUID` -- a time row's `event_id`, a stock
  movement's. An event the expiry would take stays while a row that stays names
  it, and only a column that says so is asked. Give it an index that begins
  with it. `pd gen` refuses it on any other type, a list, an edge or the key, in
  an app with no past, and on payday's Event beside its `cause_id`.
- **`past.Expire(ctx, st, tenant, past.Window{Keep, Holds}, now, past.Expiry{...})`**
  takes what the window no longer reaches, in your transaction, after your own
  rows that name events: facts superseded or replaced by the cut, and events
  before it that nothing that stays names, each row on the trail as
  `payday.past.expire` (`pd.RecordErased`). `Expiry` holds what no window ends
  -- `KeepKinds`, and `KeepFacts{Domain, Field, Key, Values}`, each value as
  `past.Encode` writes it -- and `Receipt`, the event of yours it records of what
  went.
- **`past.Preview`** counts what an expiry would take and takes nothing, told
  by the predicate your own expiry deletes by what of your tables goes
  (`map[pdid.Domain]past.Going`). A dry run is `Expire` in a transaction rolled
  back.
- **`past.Expirer`**, a `spin.Spinner` called `payday.past.expire`, is the loop
  of an app whose past is what it expires, built on what `pd gen` now writes:
  `pd.PastTenants(client)` and `pd.PastIn(ungated, drv, content)`, the
  tenant's transaction as the deployment's work in it.
- **Answer the past's window and the trail's from one source**
  ([the operator's guide](guide/operator.md#and-the-pasts-window-from-the-same-row)).
- [Keeping a past](guide/past.md#12-letting-it-go) is the guide.

What the apps on payday change:

- **rove** keeps no past of payday's yet. When it adopts one, its time rows'
  and stock movements' `event_id` say `event: true`, and its expiry
  (`server/domain/expire.go`) calls `past.Expire` once its own rows and
  documents are gone, keeping `asset.add` and the statuses that took an asset
  out of use, with `retention.expired` as the receipt; its windows answer both
  from `server/retention`, as they do the trail's today.
- **roster** keeps no past, and changes nothing.

## An event is said when it is read

**Applies to an app that declares a past**, whose `pd.g.go` gains
`pd.PastNames` when it regenerates -- the fourth part of payday#151. An app
with no past regenerates nothing new. No schema changes and no migration.

- **An app says its events** once, where it starts: `past.NewSentences(locale)`
  -- the language a reader of one nothing is said in reads -- and a sentence for
  each kind in each language, `past.Say(s, locale, func(r *past.Reading, ev
  past.EventRow, m *app.RobotMoved) string)`, with how a moment is written in
  each, `s.TimeLayout(locale, layout)`.
- **An answer of events reads them** with a reading of its own,
  `s.Read(ctx, names, locale, zone)`, and says each with `r.Say(ev)`: in the
  reader's locale, its language, the default or the default's language, and as
  the event's kind and fields where none of them says it. It asks `r.Err()`
  once they are said: a name it could not read is said as its identifier.
- **A sentence names a person with `r.Name(id)`**, as they are called now, and
  that puts them on the access record of the call -- `access.Saw`, which an
  app no longer writes for a name the past says. A thing the message holds by
  value is said as it holds it.
- **`pd.PastNames(drv)`** is the `past.Names` an app has without writing one: a
  row's `header` title -- name, else alias -- read through the wall, whose data
  it is when that field is declared `personal`. An app that names somebody
  another way implements `Name(ctx, id) (string, []access.Subject, error)`.
- **A test that every kind is said**: `past.Unsaid(s, locale, pd.EventKinds)`,
  empty for every language the app is read in. It does not count the default,
  which is another language.
- **Rows from before the past**, which hold no message and keep their sentence in
  the app's own overlay field, are said by `s.Legacy(f)`: `f` reads that field
  of the row `ev.Id` names with `r.Context()`.
- [Keeping a past](guide/past.md#9-saying-what-happened) is the guide.

What the apps on payday change:

- **rove** keeps no past of payday's yet. When it adopts one, its `namer` is its
  `past.Names` -- a membership named by the person record it is of, which the
  default does not do -- its Korean sentences move to `past.Say` with a test of
  `past.Unsaid(…, "ko", pd.EventKinds)`, and today's `namer.say` of `desc` and
  `payload` is its `Legacy`.
- **roster** keeps no past, and changes nothing.

## The past is recorded and read through the `past` package

**Applies to an app that declares a past**, whose `pd.g.go` gains `pd.Past`
when it regenerates -- the third part of payday#151. An app with no past
regenerates nothing new. No schema changes and no migration: the tables are the
ones the first part made.

- **`pd.Past(next, drv, content) (past.Store, error)`** is the app's Event and
  Fact as the `past` package writes and reads them: written through `next`, the
  stack an operation writes through, put on `drv`, its transaction; read through
  ent on `drv`. `content` is the trail's (`c.Audit.Contents()`), for the one act
  of the past beneath the servers.
- **An operation is recorded** with `past.Begin(ctx, st, past.Event{...})`, in
  its own transaction -- refused outside one -- and writes facts with
  `op.Set(ctx, past.Fact{...})`; `op.Child` and `op.Batch` for an operation over
  many things, `past.Event.Id` for one a client may send twice
  (`past.ErrDone`). A layer that records operations holds the driver its stack
  talks to and carries it in `WithDriver`, as apptest's `server/core` now does.
- **Write facts through the package, not the generated Fact server.** A fact's
  value is `past.Encode`'s canonical JSON, a personal or secret field's
  withheld, and what it supersedes is the package's to say; a row written by
  hand is one no read can be sure of. Sealing the two tables to the package is
  the sixth part of payday#151.
- **Read** with `past.StateAt`, `State.Into`, `past.Diff`, `past.Facts` (the
  statements they were written in), `past.Timeline` and `past.Events`, each
  narrowed to the caller's tenants. **Correct** with `op.Correct`, `op.Retract`,
  `op.CorrectEvent` and `op.RetractEvent`. **Take a reason out** with
  `past.Redact`, which is on the trail as `payday.past.redact`.
- [Keeping a past](guide/past.md#6-recording-an-operation) is the guide.
## The panel writes an event's value out, given the registry

**Applies if** a page of yours mounts `<Devtools>`, which has something to
pass once the panel shows events. Nothing is refused, `pd new` writes no panel,
and no floor moves. It comes with `@lesomnus/payday` 0.1.4.

- **`<Devtools registry={events} />`** hands the panel the registry
  `gen/events.ts` exports, the one the page's Connect transport is handed:

  ```tsx
  import { events } from '../gen/events.js'

  <Devtools entities={entities} registry={events} />
  ```

  An event keeps its value as an `Any`, and the panel writes a row out as
  protobuf JSON, which can write what an `Any` holds only with its type
  registered. Without it the `get` tab cannot show an event that holds a value
  -- it says `… is not in the type registry`, as it did before this -- and a
  table shows the type the value names and how big it is. Pass it over a
  sandbox's transport too, which needs none: binary reads an `Any` as bytes,
  so the panel is the first thing that writes one out.
- **A cell holding a message is that message's protobuf JSON**, as the `get`
  tab writes it. It was `JSON.stringify` of the object protobuf-es builds:
  `$typeName` and every field, set or not, and bytes as an object of numbered
  keys.

What the apps on payday change:

- **roster** mounts the panel (`ts/lib/panel.tsx`) and keeps no past, so it
  changes nothing. `registry={events}` is a line to add once its page is on
  0.1.4 and generated by a payday that writes `gen/events.ts`.
- **rove** mounts no panel, and changes nothing.

## A read that carries on from a cursor starts at it

**Applies to every app**, which regenerates: `pd.g.go` changes, and nothing
else does -- no schema, no migration, no API. `pd gen --check` says so until it
is regenerated.

- **The trail store's reads and a check of the rows page by one comparison of
  the pair**, `(date_created, id) > (a, b)`, where they paged by
  `date_created > a OR (date_created = a AND id > b)`. The rows are the same.
  What changes is that PostgreSQL starts each page at its cursor, where it read
  the kind's or the tenant's lines from the first one and filtered: every read
  of the whole was quadratic. `pd.Digests` -- a `trail rows` command -- over a
  million lines of the trail took three minutes; a page from the middle of one
  kind is 143 ms before and 0.18 ms after, and a pass's batch from the middle
  of one tenant's lines 141 ms and 0.36 ms. The reads are a pass's (`Older`,
  `Heads`), the access record's beside them, the erasures owed (`Owed`), and
  the trail read by kind in a check of the rows.
- **A `trail.Store` of your own** pages as it likes. One that copied the OR
  from the generated store reads the same way, and is fixed the same way.

What the apps on payday change:

- **rove** and **roster:** regenerate with the payday that has this, and
  nothing else.

## An event holds a message the app declares, and a lock keeps it readable

**Applies to every app with a page**, which regenerates: `pd gen --ts` now
writes `ts/gen/events.ts` for every app, and a page `pd new` made before this
hands its transport nothing from it. And to an app that keeps a past, whose
events hold messages it now declares -- the second part of payday#151.

- **A page's Connect transport is handed the event registry.** `pd new`'s
  `main.tsx` and `session.tsx` now say so; a page made before adds the line to
  each `createConnectTransport`:

  ```ts
  import { events } from '../gen/events.js'

  createConnectTransport({ baseUrl: ADDR, jsonOptions: { registry: events } })
  ```

  Without it, a List of events fails whole once one of them holds a value:
  JSON names the message an `Any` holds by its type, and protobuf-es reads one
  only with the type registered. An app with no past has the file, empty, and
  loses nothing by passing it. No `@lesomnus/payday` release is needed:
  `jsonOptions` is connect-web's.
- **An event message is marked**, and its rows are written under its kind:

  ```proto
  message RobotMoved {
    option (payday.event) = {kind: "robot.move"};
    bytes robot = 1;
  }
  ```

  Move the app's `buf.build/payday/payday` pin to a `dev` that has
  `payday.EventMessage` (`buf dep update`) first. A pin from before answers
  `cannot find payday.event in this scope`, and `pd gen` says that is the pin.
- **`pd gen` refuses** an event message with a `(payday.field)`, an entity, a
  message of a generated contract or an `Any` anywhere in it; an entity that is
  one; two of one kind; and one in an app that declares no past.
- **`proto/<pkg>/payday/past.lock` is new**, beside the copies, in every app
  that declares a past -- `{}` until it declares an event message. Commit it.
  `pd gen` only adds to it, and refuses a schema that deleted an event message,
  removed a field of one or changed its JSON name, kind, type or shape, renamed
  or removed a value of an enum one holds, or changed a kind. `pd gen --check`
  covers it. Taking a type back is a hand edit of the lock, for a type no
  deployment ever wrote a row of.
- **`pd.EventKinds`** is written into `server/pd/pd.g.go` of an app that
  declares a past: every event message by its kind, to read a row's value back
  with.
- [Keeping a past](guide/past.md) is the guide to all of it.

## An app may declare its past, and `pd gen` writes Event and Fact into it

**Applies to every app**, which regenerates -- the README `pd gen` writes beside
payday's copies says more -- and to one that wants payday's `Event` and `Fact`,
the first part of payday#151. Nothing else changes for an app that declares no
past: it has neither.

- **To declare one**, move the app's `buf.build/payday/payday` pin to a `dev`
  that has `payday.Past` (`buf dep update`), and say it once, on the file that
  says the package is the app's:

  ```proto
  option (payday.app) = {past: {event: 17, fact: 16}};
  ```

  Two domains of the app's, in 7..247, different, and held by no other entity.
  `pd gen` writes `proto/<pkg>/payday/event.proto` and `fact.proto` and
  everything generated from them, on every run, and takes them out on the first
  run after the line goes. A migration makes the two tables.
- **An app with its own `Event` or `Fact`** -- or a file of its own called
  `event.proto` or `fact.proto` -- takes it out before declaring: `pd gen`
  refuses the declaration beside one, before buf runs. What the app's had that
  payday's do not goes in an overlay, `proto/ext/payday/event.ext.proto` or
  `fact.ext.proto`, at a number payday's leave empty -- 6 and 17 of an Event, 11
  of a Fact; the schema guide's §7 has the rest.
- **A Fact's field is `field_number`**, its number in the subject's descriptor.
  Not `field`: ent's generated mutation already has methods by that name, which
  take any field, and a column of it does not compile.
- **A pin from before this** answers a declared past with `cannot resolve message
  field name for payday.App` and `cannot find OWN_EVENT in this scope`, and
  `pd gen` says that is the pin. It had stopped saying so of any option: the buf
  it pins words the failure differently from the one the explanation was written
  against, and the explanation matched nothing.
- `pd entity add` skips the two numbers, and refuses `Event` and `Fact` in an
  app that declares a past.
- What the two are written with comes in the parts after this one -- an event's
  message and the lock that keeps it readable, then the `past` package. Until
  then they are entities like any other: written and read through their
  generated servers, behind the wall, and in the trail with `reason` personal.

## `pd new`'s store deletes everybody else's mirror, and a page needs payday 0.1.2

**Applies if** your app was made with `pd new` -- its `ts/src/store.ts` -- or
its page has `@lesomnus/payday` below 0.1.2.

- **The store `pd new` writes calls `deleteOtherDisks(at)`** once the caller's
  mirror is open, and does not wait on it, as the 0.1.2 entry below asks of
  every page. A page made before this has an `open` that does not, and keeps
  everybody else's mirror on the browser until it is added:

  ```ts
  const store = Store.open(entities, { ...at, disk: await openDisk(entities, at) })
  void deleteOtherDisks(at)
  await store.hydrate()
  ```

- **The page's floor for `@lesomnus/payday` is 0.1.2**, the release that
  brought `deleteOtherDisks`. `pd doctor` says so of a page below it, and
  `pd upgrade` moves it to `^0.1.2`; nothing is refused.

## A store closes after what it was told, and a forget says how it went

**Applies if** a page of yours calls `Store.forget` or `Store.close` -- `pd new`'s
`main.tsx` and `session.tsx` both do, when somebody signs out -- or implements
`Disk`.

- **`close()` closes the mirror once the writes queued for it have gone**, and
  answers a promise for when it has. It used to close it at once, under the
  writes still queued -- and a forget is one of them, so `forget(); close()`,
  which is how a page signs out, left the caller's rows and answers on the
  disk with nothing saying so. The writes they never sent were cleared, by a
  transaction of their own. Nothing has to change in a page for the fix to
  apply, awaited or not. A row written after `close()` stays in memory.
- **`forget()` answers a promise**: for when the mirror is cleared, rejecting
  with why it could not be. Called on a store already closed, it rejects,
  since nothing is left to clear the mirror with; `deleteDisk` deletes it.
- **`close()` rejects when the forget before it did not reach the mirror**, and
  closes it all the same. Not awaited, that is an unhandled rejection, which a
  browser prints. Await it where you would rather say so to the person, as
  `pd new`'s `session.tsx` now does. A row that did not reach the mirror is
  still `flushed()`'s to say, and not `close()`'s.
- **A mirror another page deleted counts as forgotten.** One another page moved
  to a later version does not, since it is still there.
- **`Disk.close` is unchanged**, and still synchronous: the store calls it
  once the queue has drained. A `Disk` of your own needs nothing.
- `pd new`'s pages now wait for `close()` before they start again, and
  `session.tsx` tells the person when signing out left something on the disk.
  A page made before this keeps working as it was written, and is fixed by the
  upgrade; copy those lines in if you would rather the person were told.

## An erasure decided in a transaction is owed, then paid

**Applies to every app** (a new table), and to one that calls
`trail.Policy.Forget` inside its own transaction, or hand-writes a
`trail.Store`. Part of payday#115. Regenerate: payday's entities gain `Owed`
(`OWN_OWED`, domain 254), which `pd gen` copies in and a migration makes a
table of.

- **`Policy.Forget` on a store inside a transaction** (`Store.InTx`) blanks the
  database's rows as before and owes the archive's half, one `Owed` row per
  object in the same transaction, writing nothing to the archive:
  `Forgotten.Owed` says how many. A rollback takes the debt with it. Outside a
  transaction it owes and pays at once, as before but crash-safe. Inside one
  it takes no turn on `Policy.Lease` — a turn given back before the commit
  kept nothing apart, and two erasures in one transaction beside a pass waited
  on each other for ever — and the rows' seals are owed with the archive's
  half: `Forgotten.Seals` is zero there, and `Paid.Seals` counts them.
- **`Policy.Pay(ctx, s, objects...)`** pays what is owed for the objects you
  forgot — both halves, `Forget`'s receipts, a checkpoint — after your commit,
  and a pass pays it before it moves anything if you do not. Name them: naming
  none pays every debt, as a pass does, including those a hold keeps. A debt a
  legal hold keeps part of stays until the hold lifts, and the pass after pays
  the rest; `Policy.Owing` says what is owed, since when, and how much under
  whose hold.
- **Every other act inside a transaction is refused** with `trail.ErrInTx`
  before it writes anything: a pass, `Archive`, `Collect`, `Purge`,
  `PurgeTenant`, `RetireTenant`, `Return`, `ExportTenant`, `Accept`, `Pay`.
  Questions are answered, and `Retired` is one now.
- **`trail.Store` gains `InTx`, `Owe`, `Owed`, `Paid` and `Kept`.** A generated
  store has them; a hand-written one adds them, and a test's fake answers
  `InTx` false.
- **Code that walks your tables leaves `owed` to the trail**, as it leaves
  `rowseal`: it is the deployment's, of no tenant, and no export carries it
  (`owed.Table`).

What the apps on payday change:

- **rove:** pseudonymize and departure keep calling `Forget` in the domain
  transaction, and the trail's erasure is now atomic with them; they may call
  `Pay` with `pd.TrailStore` on the deployment's client after the commit,
  naming the objects, for the archive's half at once, or leave it to the next
  pass. The `held` count an event records is the database's half's unless
  they pay. Its offboarding walks every table, so `owed` goes beside `rowseal`
  among the trail's and the withheld.
- **roster:** `forget` commits its transaction and then calls `Forget` with
  the deployment's client, which still works and now owes and pays at once.
  A crash between that commit and the call loses the trail's erasure; calling
  `Forget` with the transaction's client before the commit owes it atomically
  instead, and `Pay` naming the same objects after the commit, or the next
  pass, pays. Both apps get the new table on their next migration.

## A tenant's trail can be taken out

**Applies if** you call `Policy.Retired`, or give an operator a way to answer a
demand to preserve a tenant's trail. Part of payday#115.

- **`Policy.Retired` answers a `Retirement` for each tenant**, `At` and
  `Until` — when it retired, and until when its floors keep what they keep, by
  the floors as they are — where it answered a time. `TenantReturn.Retired` is
  still a time.
- **`Policy.ExportTenant(ctx, s, tenant, trail.Export{By, Why, To}, w)`** writes
  every row of the trail that names the tenant as one bundle, encrypted to
  `To` (`age` recipients) and never otherwise: the database's rows, its own
  chunks as kept, each chunk's rows of it opened, and a signed manifest naming
  the newest checkpoint and its witnesses' receipts. It is an act: it needs
  `Policy.Signer`, takes the trail's turn, writes a receipt `export-tenant`
  (`By`, `Why`, and the manifest's digest in `Of`) and a checkpoint after. Sealed
  rows need the master's private half (`audit.keys.identity`), or it is
  refused with `ErrSealed`.
- **`trail.OpenTenantExport(r, identities, trusted, fn)`** reads a bundle and
  answers its manifest once every file is what it says and a trusted key
  signed it; anything else is `ErrExportAltered`.
- **Record an export as a download** of people's data, with its reason, as
  your access record does any: payday does not know who a row is about.

## A Rekor log is a witness

**Applies if** you name witnesses (`audit.checkpoints.witnesses`), or write a
`trail.Witness` of your own. Part of payday#115.

- **`rekor:<url>?key=<file>`**, `&v=2` for a log on tiles, logs each
  checkpoint's digest to a Rekor transparency log: `import _
  "github.com/lesomnus/payday/config/witnessrekor"`. Your own log, or public
  Sigstore by choice; read the runtime docs' Rekor part first.
- **An Ed25519 key in a device is refused** when a `rekor:` witness is named:
  the log takes a signature over the digest (Ed25519ph), which payday's device
  signers do not make. Name an ECDSA or RSA deployment key, or keep to `store:`
  and `log:`.
- **Two optional interfaces for a witness of your own**: `trail.Prehashed`, one
  that keeps a statement's digest and is told a signature over it
  (`trail.Signed`'s new `Prehash`, `PrehashKey` and `PrehashSig`), and
  `trail.Checker`, one that holds the chain to itself in a bounded number of
  questions. A witness that implements neither is as it was.
- **On the log's head**: set a monitor of the log to the deployment's key, and
  the alert the runtime docs give.

## The trail's digest covers a secret, and a row's seal what is personal

**Applies if** you keep digests (`audit.digest.key`), give an operator a command
that runs `pd.Digests` (`trail rows`), or write or remove rows beneath your
servers. Part of payday#115. Regenerate: payday's entities gain `RowSeal`,
which `pd gen` copies in and a migration makes a table of.

- **The digest covers a field declared `secret`.** A keyed MAC of a verifier or
  a token's hash is neither a copy of it nor a way to test a guess at it without
  the key, so a credential rewritten in the database past the servers is a row
  that differs. A digest made before this left secrets out, says so in the
  fields it lists (`digest_fields`), and is checked over what it covered.
- **A digest is version 3**: a byte of flags after the version, bound into the
  line's MAC, saying whether the write sealed the row (`audit.Sealing`). It is
  82 bytes where it was 81, and 42 for a write that left no row. Versions 2 and
  1 are still checked, and carried forward as version 3 saying they sealed
  nothing.
- **A field declared `personal` is covered by the row's seal**: a MAC of every
  field of the row, kept beside it in `RowSeal` -- in the database alone, never
  on the trail and never in the archive -- for as long as the row is. The
  recorder writes it in the transaction of every write that leaves a row of a
  kind that declares a field personal, kept for the commit with the trail: a
  statement for every few hundred rows. Its key is derived from
  `audit.digest.key`; there is nothing to configure. A personal field changed
  past the servers is in `Rows.Differs`, and so is a row whose seal was taken
  off; a seal of an earlier write is in `Rows.Forged`.
- **`personal` no longer decides what a check covers.** It decides what the
  trail keeps of a field, what the access record reads, and which kinds are
  sealed.
- **Rows from before seals are listed, and are no finding**: `Rows.Unsealed`,
  the rows of a sealed kind whose newest line's write sealed nothing -- written
  before this, or before the kind declared the field personal. `Rows.Ok()` is
  true with them, and they are in `Rows.Digest`. Until they are sealed, a
  personal field changed in one of them is not found. Seal them once, as an
  operator who has looked: your `trail rows --accept` puts `Rows.Unsealed` on
  the trail with `pd.Record`, beside what it records already, bound to the
  digest the operator reviewed as before. So have the command accept when
  `Unsealed` is not empty although nothing was found, and print how many there
  are and that a check does not vouch for their personal fields yet.
- **`pd.Record` seals what it records, and `pd.RecordErased` removes the seal**,
  in the transaction they write the trail in. A row removed beneath the servers
  without `RecordErased` keeps its seal, a MAC of what the person was, until a
  forgetting or the tenant's purge takes it.
- **`trail.Policy.Forget` blanks the seals of the rows it names**, whatever a
  hold keeps of the trail, leaving one that says the person was forgotten
  (`audit.Digest.Forgotten`, counted in `Forgotten.Seals`); a check counts such
  a row in `Rows.Forgotten` and vouches for what its digest covers. The next
  write through the servers seals the row again. Forget after the writes that
  blank the row, as before. `PurgeTenant` and `RetireTenant` remove every seal
  filed under the tenant (`TenantPurge.Seals`), and a pass carrying a line
  forward carries its row's seal.
- **`trail.Store` has `BlankSeals` and `PurgeSeals`**, which `pd gen` writes for
  you; a store of your own adds them.
- **Code that walks your tables leaves `rowseal` to the trail**, as it leaves
  the trail's own: a purge of a tenant's rows leaves their seals to
  `trail.Policy.PurgeTenant`, which takes them, and an export of the rows never
  carries one -- a seal is a MAC of what people are, kept in the database
  alone (`rowseal.Table`).
- **`pd entity add` numbers an app's entity below 248.** payday numbers what it
  adds now down from 255, where no app's entity is: `RowSeal` is 255. An app
  whose own entity is 255 is refused by `pd gen` (two entities declare one
  domain), which is how it finds out.

## The newest checkpoint is heard outside the database: witnesses

**Applies if** you keep the trail's archive (`audit.archive`). Part of
payday#115. Nothing is required but reading what changed; witnesses are named
when you are ready.

- **The checkpoint store is read for what each blob holds**, never by its
  labels. A checkpoint relabelled or with its labels taken off counts as one --
  it makes the database `behind`, and an archive whose checkpoint store keeps
  any blob is no longer filled from what it holds. A blob that is neither a
  checkpoint nor a witness's receipt is a finding (`forged`, "it does not
  read"). A store kept for checkpoints alone, as `audit.checkpoints.dir` and the
  archive's `_checkpoints` are, has none.
- **`audit.checkpoints.witnesses`** names where every checkpoint is heard
  (`trail.Policy.Witnesses` in code): `log:` and `store:<dir>` are payday's, each
  linked in like a signer -- `import _ "github.com/lesomnus/payday/config/witnesslog"`,
  `.../config/witnessstore`. An app made by `pd new` links both in `cli`; add the
  same file (`cli/witness.go`) to an app of an earlier scaffold. A scheme this
  binary does not open, a witness named twice, or witnesses without an archive,
  are refused where the process comes up.
- **What a verification finds of them**: one a witness heard that the store does
  not keep (`unkept`) or keeps as something else (`rewritten`), each pair of
  statements of one number a witness heard -- the count wound back below the
  head, `Heard.Twice` -- (`rewritten`, until an acceptance takes that pair in,
  `Accepted.Twice`),
  the newest the store keeps that no witness that answers heard within
  `audit.checkpoints.hear_within` (`unheard`, a new kind; twice `audit.every`
  when empty), and an answer whose signature is not good (`forged`).
  `Verified.Heard` is what each witness that answers heard last: print it with
  the findings.
- **No act takes a checkpoint under a number a witness heard** and the database
  has not numbered, and an acceptance is numbered past every one a witness
  heard. A database behind what a witness heard is accepted as a restore
  (`Acceptance.Restored`), as one behind the store is.
- **Set the alerts** on the log witness the runtime docs give: the gauge
  `payday.trail.checkpoint` going down, and two log lines of one `k` with two
  digests. Without them it tells nobody.

## A retired tenant can come back

**Applies if** you restore a tenant under the identifier it had into a
deployment it may have retired from (`Policy.RetireTenant`). Part of
payday#115.

- **`Policy.Return(ctx, s, tenant)`** takes a retired tenant back: the records
  of its retirement are erased through the manifest, signed, with a receipt
  `return-tenant` and a checkpoint after. Call it before you write anything of
  the tenant back. Until then every pass takes it to have left, whatever your
  callback answers: what it writes goes at the next pass, and its whole trail
  and its key once its last floor has passed.
- **`Policy.Retired(ctx, s)`** is the tenants that have retired, when, and
  until when their floors keep what they keep, for a restore's dry run to say
  so.
- **A purged tenant needs nothing.** The mark its key left is of what was
  written before it left, and what it writes after is sealed to a key of its
  own; `Return` answers it with nothing done.
- Both are acts: they need `Policy.Signer` where there is an archive, as every
  act does.

## An acceptance is of what was reviewed, and is kept

**Applies if** you give an operator a command that accepts what `Verify` found
(`trail.Policy.Accept`), or one that records rows a check of them found
(`pd.Record` after `pd.Digests`). Part of payday#115.

- **`Policy.Accept(ctx, s, why)` is `Policy.Accept(ctx, s, trail.Acceptance{Of,
  By, Why, Full, Restored})`.** `Of` is the digest of the findings the operator
  looked at, which `Verify` now answers (`Verified.Digest`): print it beside the
  findings in your `verify`, and have your `accept` take it back from the
  operator (`--of`). Never fill it from a verification of the command's own,
  which is the acceptance of whatever is there that this replaces. The
  acceptance verifies again, the same way (`Full` for a `--full` one), and is
  refused with `trail.ErrUnreviewed` when that finds anything else. `By` is who
  accepts, as your command knows them, and is required like `Why`.
- **What signing rows brings to light is not taken in.** A row whose signature
  was a finding is signed by the acceptance, and its blob compared after;
  what that finds is the next verification's, and an acceptance of its own. An
  archive kept without a key may take two.
- **A database behind the checkpoints the store keeps is accepted only as a
  restore**, naming the checkpoint its backup holds (`Restored`, zero for a
  backup from before the first): give `accept` a `--restored` that takes the
  number, and `trail.ErrRestored` refuses one with none, one with the wrong
  number, and one of a database that is not behind.
- **A new finding, `lost`**, while the database is behind: what the newest
  checkpoint the deployment vouches for says the archive held, and it holds no
  more. A runbook for a restore says to look at each before accepting.
- **`Checkpoint.Accepted` is a `*trail.Accepted`**, and no longer a string: who,
  why, the digest, `Restored`, and what was accepted -- `Kinds`, the first
  hundred `Keys` and how many `More`. A checkpoint from before keeps only why,
  and reads as `Accepted{Why: ...}`. `Verified.Accepted` lists the latest ten,
  newest first; print them in your `verify`.
- **An acceptance is an act**: in the trail's turn on `Policy.Lease`, and it
  writes a receipt, `accept`, with `By`, `Why` and `Of` (new fields of
  `trail.Receipt`).
- **A check of the rows has a digest too**, `audit.Rows.Digest`. Print it with
  what `pd.Digests` found, and have the command that records the rows as
  accepted (`pd.Record`) take it back and refuse when a check now answers
  another.

## A tenant may retire, keeping what the deployment's floors are on

**Applies if** you take a tenant's trail out when it leaves (`Policy.PurgeTenant`)
and the deployment keeps floors (`audit.min`, `audit.by.<kind>.min`). Nothing
changes until you call the new one (part of payday#115).

- **`Policy.RetireTenant`**, beside `PurgeTenant`, and `Policy.PlanTenantRetire`
  beside `PlanTenantPurge`. It takes what a purge takes but for what a floor is
  still on, and leaves that -- and the tenant's key -- for the passes to destroy
  as each floor passes; a pass purges what is left once the last has.
  `TenantPurge.Kept` says what stayed, by kind, and `Until` when it goes.
- **Which one is yours to choose**, per contract or by the law a tenant is
  under: a field of your contract entity, read where you offboard. PIPA's
  access record is kept a year whoever leaves, which is a retirement.
- **Expose both** where an operator takes a tenant out by hand -- a `retire`
  beside `purge` -- with their plans as the dry run.
- **The callback is still asked about a retired tenant**, for its hold. Answer
  without an error once its contract is gone: an error keeps everything, as it
  always has. Its windows are not asked.
- **It needs the archive**: with floors and no `audit.archive` it is refused.
  Receipts are `retire-tenant`, with `kept`, `kinds` and `until`, and the
  archive gains a namespace, `_retired`, of one blob a retired tenant.

## An archive is kept with a key

**Applies if** you keep the trail's archive (`audit.archive`, or
`trail.Policy.Archive`). Part of payday#115.

- **`audit.archive` with no `audit.checkpoints.key` is refused** where the
  process comes up, with `trail.ErrNoKey`, which says how to make one: `openssl
  genpkey -algorithm ed25519 -out checkpoints.pem`, or `trail.GenerateKey`,
  mounted as a secret and named there -- or a key in a device, by its URI. Keys
  to trust and none to sign with are refused too: a deployment's process acts,
  and a verifier is a policy built in code with `Trust` alone.
- **There is no keyless path.** A row of the manifest or a checkpoint without a
  good signature is a finding and nothing more, whatever the policy says; an
  act asked of a policy with an archive and no `Signer` is refused before it
  writes anything (`trail.ErrNoKey`); `trail.Policy.Valid` refuses an archive
  with no key at all. The window in which unsigned checkpoints decided, until
  the store kept a signed one, is gone.
- **An archive kept without a key until now** sets one, and then runs `trail
  accept --why ...` once. Until then every row and every checkpoint it has is
  `unsigned`, nothing is destroyed by them, and no act takes a checkpoint after
  them -- each says so in its error, after doing what it does (`no trusted key
  signed the checkpoints the store keeps`). The acceptance signs the rows as
  they stand, and names the checkpoint the database numbered last as the one
  before it -- of two, the one the database's rows add up to -- which takes in
  the history before the key; `trail verify` is clean after it. A deployment
  that changed its key without trusting the old one is told the same, and puts
  the old one in `audit.checkpoints.trust` instead.
- **Development and tests.** `pd new`'s `init` writes a key of the deployment's
  own beside an archive that names none (`<archive>-checkpoints.pem`, ignored
  by the scaffold's `.gitignore`) and prints where; name it as
  `audit.checkpoints.key` before serving. An app of an earlier scaffold adds the
  same to its `init`, or makes one by hand. In a test, `pdtest.Key(t)` is the
  PEM document `audit.checkpoints.key` reads and `pdtest.Signer(t)` the signer
  `trail.Policy` takes, fresh each time; policies that share an archive share
  one. Nothing that signs is committed.
- **A store keeping two checkpoints of one number is `rewritten` only when both
  are the deployment's word**; one nobody vouches for beside another is the
  `unsigned` or `forged` it is.

## An index is stored under the name it declares, and `rename.Indexes` moves it there

**Applies if** you generate with `pd gen` and migrate a database an earlier
build made: every index your schema names -- and payday's own, `Audit`,
`Access`, `Outbox`, `Holder`, `Tenant` among them -- is stored as
`<table>_<name>`, `robot_page`, where it was stored under ent's name for its
columns, `robot_tenant_id_date_created_id`
(protobuf-orm/protoc-gen-orm-ent#15).

- **Call `rename.Indexes` in your `Migrate`, before `Create`**, with what `pd
  gen` writes for your schema, `pd.IndexRenames` -- `pd new`'s `Migrate` does:

  ```go
  if err := rename.Indexes(ctx, s.Db, s.Dialect, pd.IndexRenames); err != nil {
  	return err
  }
  return entmigrate.NewSchema(s.Drv).Create(ctx)
  ```

  A migration finds an index by name, so without it the first one made every
  index again from every row, holding each table against writes while it did,
  and left the old one beside it, since it drops no index. With it, PostgreSQL
  renames each one, which is a change to the catalog and builds nothing;
  SQLite renames no index, so the old one is dropped and `Create` makes the new
  one. An index already made under the new name has the old one dropped
  instead. On a database this build made, or one already renamed, it does
  nothing.
- **And then a reordered index needs no list.** It is found under its name and
  made again where it stands, so a list of replaced names such as rove's and
  roster's `Replaced` has nothing more to say once every deployment has
  migrated past the build that replaced them; delete it then.
## A checkpoint nobody vouches for decides nothing

**Applies if** you keep the trail's archive (`audit.archive`, or
`trail.Policy.Archive`), rotate the checkpoint key, or sign through Vault. Part
of payday#115. Nothing in your code changes.

- **A checkpoint nobody signed, or signed by a key nobody trusts, decides
  nothing** once the store keeps one your key signed: not whether the database
  is `behind`, not what the latest rows are compared with, not what the next
  checkpoint is numbered after or names, and not which numbers an acceptance
  quieted. It is `unsigned` or `forged`, and that is all. A checkpoint one of
  yours names as the one before it counts whatever signed it, and so does
  every one before that, which is why a checkpoint is now read only while its
  bytes are its digest: one that is not is `forged`, "it does not read".
  `Policy.ReadCheckpoints` reads the ones that count.
- **A number the store keeps is no gap.** Two acts that take a checkpoint at
  the same moment can be written the other way round, and both name the one
  before the first; that was `unkept` for the number between, and is no
  finding. The next checkpoint names the newest below it by what each says,
  whatever its labels say.
- **A key rotated out can leave `trust`.** Keep it there until the next key has
  signed a checkpoint and the rows the old one signed have been accepted again
  (`trail accept`), then take it out: the checkpoints it signed are what the
  chain says they were without it, and what it signs after that is `forged`.
  Left in `trust`, it is believed for whatever it signs, as before.
- **Vault over plain `http` is refused** but on this machine (`localhost`, a
  loopback address). `VAULT_ADDR`, or the URI's `addr=`, names an `https://`
  address for a Vault on another host.

## A check of the rows finds what a database writer can make of the trail

**Applies if** you call the generated `pd.Digests`, give an operator a command
that runs it, or write or remove rows beneath your servers (payday#115). The
schema is unchanged and nothing needs migrating; regenerate.

- **What used to be counted is found.** `Rows.NoDigest` and `Rows.KeyNotHeld`
  are the rows now, not counts, and `Rows.Ok()` is false while there are any:
  a line with no digest, and a digest under a key the check was not given, are
  what anybody who can write the database can make of a line. A check given the
  wrong key, or of a deployment that wrote without one, no longer passes.
  Have the command exit non-zero on every list `Rows` has, and print
  `Rows.Unheld`, the keys those were under.
- **What may be counted is said beside the key.** `audit.digest.since` is when
  the deployment began keeping digests: a line written before it with none is
  counted in `Rows.BeforeDigests`. A deployment that was given a key after its
  first write sets it, to the moment it did -- the earliest digested line of the
  trail is no later -- and takes it out once those rows are accepted. A since
  later than a line that keeps a digest is refused by the check.
  `audit.digest.retired` is the fingerprints of keys let go; their digests are
  counted in `Rows.Retired`.
- **A digest is bound to its line** (version 2): its key, its moment, its
  action. One copied onto another line is `Rows.Forged`. Version 1 is still
  checked and no longer made; nothing has to be rewritten.
- **An erased row is read.** A soft Erase keeps a digest of the row it left, and
  a check reads erased rows as it reads live ones, so one brought back or
  changed past the servers is in `Rows.Differs`. A hard Erase keeps a digest
  that says the row went. An erase recorded by a build before this keeps none,
  so a row erased since the key was given is in `Rows.NoDigest`, or in
  `Rows.Missing` when it was erased hard, until it is accepted once.
- **What was removed is found.** `Rows.Missing` is the things the trail says
  are rows and that are no row. **`pd.RecordErased(ctx, db, contents, action,
  ids)`** is `pd.Record` for rows you removed beneath the servers -- what a
  person forgotten took with them, a workspace destroyed -- called in the
  transaction that removed them. `pd.Record` takes a row erased softly now, and
  refuses one that is no row (`is no row`; that is `RecordErased`'s).
- **A pass carries forward** the newest line about a row that is still there
  before it destroys it at the end of its window, so a row nobody wrote since is
  still on the trail. It needs the key: `trail.Policy.Digest`, which
  `config.AuditConfig.Policy` sets from `audit.digest`. A policy made by hand
  sets it. `trail.Store` has a method more, `Carry`, which `pd gen` writes.
- **`Rows.Skipped`** names every kind kept off the trail, with its why: print
  it, so that a check that found nothing says what it did not look at. `pd
  doctor` asks about a kind kept off the trail that holds a `secret`.
- **`(payday.field)` on a field of a message that is not an entity**, or on one
  the ORM does not map, is refused: nothing read it, and a `personal: true`
  there kept nobody off the trail. Say it on the entity's column that holds the
  message.
- **Field 4 of `payday.Field`** and the name `impersonal` are reserved.

## A transaction's end is its owner's: a write that fails partway refuses its commit

**Applies if** you generate with `pd gen`, or call `dialect.BeginTx`.
Regenerate; change code that holds what `BeginTx` hands back as a
`dialect.Tx` (#84, protobuf-orm/ent#16, protobuf-orm/protoc-gen-orm-ent#14).

- **A generated write that fails partway, in a transaction it joined, refuses
  that transaction's commit** (`ent.ErrPartial`). It cannot take back what it
  wrote -- there are no savepoints, and the transaction is not its to end -- so
  an operation that goes on past the error finds its commit refused, and
  nothing of the write holds. Each write says when it begins writing
  (`ent.Joined.Writing`); one refused before then refuses nothing.
- **An `AddMany` records the rows of each statement as soon as it holds**, so
  the trail never lags the rows a statement wrote.
- **What `dialect.BeginTx` hands back is a `driver.Tx`**: Commit and Rollback,
  no statements. A statement goes through the driver it answered with, which
  is what writes what the transaction kept ahead of a statement about it.
- **`dialect.BeginTx` refuses to begin inside a transaction it did not begin**
  (`dialect.ErrForeignTx`) -- a generated client's `Client.Tx`, say. Begin with
  `BeginTx`, and put the client on it with `WithDriver`.
- **An AddMany's rule hands the request on whole**: a field of an app's own on
  `<E>AddManyRequest` reaches the server behind the gate and the Sink
  (protobuf-orm/protoc-gen-orm-ent#13).

## A call costs what it asks for

**Applies if** your `serve` calls `grpcx.LimitUnary` or `grpcx.Limit`, which
`pd new`'s does, or code of yours implements `grpcx.Limiter` or calls
`batch.Guard.Allow`. Regenerate, then change those calls (#84).

- **An `AddMany` costs one call for each of its items** (`grpcx.Cost`). It was
  one call however many rows it carried, so a caller limited to a call a second
  could write a thousand rows a second, and a batch of a hundred and
  twenty-eight `AddMany`s was a hundred and twenty-eight operations.
  `pd gen` writes `pd.Cost`, which says what each RPC of the schema costs.
- **Hand it to the interceptor**: `grpcx.LimitUnary(c.Server.Limiter(),
  gate.ByTenant(), pd.Cost)`, and `grpcx.Limit` takes it the same way. `pd.Batch`
  puts it in a guard that names no `Cost` itself.
- **`grpcx.Limiter.Allow` takes the cost**: `Allow(ctx, key, n)`. A limiter
  answers `grpcx.Never` for a call it will never let through as it is; the
  in-process one does for a call that costs more than its burst, and the caller
  is told to ask for less. A deployment that limits and takes large `AddMany`s
  sets `server.limit.burst` at least as large as the largest.
- **A batch is bounded by the rows it asks to write** as well as by its
  operations: `batch.Guard.Rows`, a thousand when nothing said
  (`batch.MaxRows`), and `server.max_batch_rows`. A batch over it is refused
  before anything is charged or written. `Guard.Allow(ctx, method, cost)`
  takes what the operation costs (`Guard.CostOf`).
- **A batch reads every operation's request before it lets any through**, so an
  operation of a method nothing serves, or with a request of another message,
  is refused before anything is charged or written.

## A write to a row forgets what a transaction was told about it

**Applies if** you generate with `pd gen`. Regenerate; nothing else has to
change, unless code of yours calls `seen.Tenant`.

- **The Sink forgets what a transaction was told about a row before a
  `Patch`, an `Apply` or an `Erase` writes it** (`seen.Forget`), so the next
  question about the row is asked again (#84). Before, an answer outlived a
  write to its row for the rest of the transaction: a row erased was still one
  the caller had seen, a row moved to a set the caller cannot see was still in
  reach, and a key a hard erase had freed and an `Add` had given another row,
  in another tenant, still arrived at the old row's tenant. On the Sink, so a
  write through the server with no wall forgets as one through the gate does;
  an entity the Sink had nothing else to say about is wrapped for it.
- **A write around the servers is not seen** -- ent itself, or SQL. A
  transaction that makes one to a row and then names the row through the
  servers is answered from before it.
- **`seen.Tenant` takes the entity a walk starts at and the walk apart**:
  `seen.Tenant(drv, "Robot", "tenant", ref, ask)` where it was
  `seen.Tenant(drv, "Robot.tenant", ref, ask)`. Only generated code calls it.

## An order is over columns a cursor can carry

**Applies if** an entity's `list:` orders by a column that may be null, a
`secret:` field, a list, a map, JSON or bytes -- in `order` or in a named
order. `pd gen` refuses it now, naming the column; nothing changes for any
other list.

- **A nullable column** skipped rows: a null is neither past nor before a
  cursor. Order by a `default`ed column, or leave it out of the order.
- **A `secret:` field** went out in every page's `next`.
- **A cursor is read back as the column's type.** It was read into `any` for
  every column but a string, a time and an identifier, so an int64 past 2^53
  came back as a neighbour and a page repeated its last row. A cursor a client
  holds carries on: its bytes are what they were.

## The gate asks an `Apply` about the edges it moves

**Applies if** an entity has an edge that can move onto a row behind the wall
-- one that is not `immutable`. Regenerate; nothing else changes.

- **The generated `Gate` has an `Apply`** beside its `Patch` for each such
  entity, payday's holder among them when an overlay gave it one. It compiles
  the document as the server behind does and reads, through the wall, the row
  each moved edge is given; one the caller cannot see is `NotFound`, as at
  `Patch`. Before, a document moved an edge onto any row by its key, and the
  row was read back through the caller's own one hop later.
- **A document the gate cannot compile is refused there**, with the code the
  server behind gives it: `InvalidArgument`, or `Unimplemented` for what this
  engine does not do.
- `Apply` is still closed at the transport unless `allow_general_writes` opens
  it; this is the rule about the row that holds wherever it is called from.

## A broker and a lease are built on the process's context, and `PlainInProcess` believes only this process

**Applies if** your `cmd.Build` or your serve command builds a broker or a
lease -- every app `pd new` made does -- if you register a broker or a lease of
your own, or if you serve a server with `auth.PlainInProcess()`.

- **`c.Watch.Build(ctx, c.Db)` and `c.Spin.Build(ctx, c.Db)`.** Both take the
  context first: `cmd.Build`'s for the broker and for the trail's lease
  (`p.Lease`, the entry below), the serve command's -- after `Telemetry` -- for
  the lease the loops run under. What they say later, a listener that stopped
  or a hold lost with its connection, is said in the log that context carries;
  it went to the OpenTelemetry global, which nothing installs.
- **A registration is handed that context first**:
  `config.RegisterBroker(name, func(ctx context.Context, c config.WatchConfig, db config.DbConfig) (watch.Broker, error))`,
  and the same for `config.RegisterLease`. Dial under it, and keep what it
  carries rather than its cancellation (`context.WithoutCancel`).
- **`auth.PlainInProcess()` refuses a name told from outside this process.** A
  call is believed when its peer is `bufconn`; one over a port, a socket or the
  HTTP transcoder is Unauthenticated (`auth.ErrFromOutside`). A server served on
  `bufconn`, which is what it was for, changes nothing.
- **A loop you rename can say what it was called**: `spin.Formerly(old, s)`.
  Under a lease it holds that name as well as its own, so a rolling upgrade
  that runs the build before beside this one runs the loop in one of them.
  Nothing to do for a loop you have not renamed.

## A pass and what erases from the trail take turns, and a tenant that left has no key made again

**Applies if** you keep the trail's archive and erase from it -- `Policy.Forget`,
`Policy.PurgeTenant`, `Policy.Collect` -- or seal it (`audit.keys`). Part of
payday#115.

- **Hand the trail's policy the deployment's lease**: `p.Lease =` the
  `spin.Lease` that `spin.lease` names, built beside the policy -- `pd new`'s
  `Build` does, unless it is `none`. A pass then holds `trail.Turn` for each
  chunk's worth of rows it moves, and the erasures change the database's rows
  in their own turn, so a pass can no longer write back what one blanked or
  removed a moment before. Without it nothing takes turns, which `p.String()`
  says. Never hand it `spin.Nowhere()`: an act would wait for ever.
- **An erasure called inside a transaction of yours waits for a pass's chunk**
  while it holds that transaction. On SQLite that can make the pass's chunk
  fail once (`database is locked`), to be moved again by the next pass.
- **A tenant that leaves leaves a mark in the key store** where its keys were.
  A row written before it is sealed to the tenants it names that are still
  there; one that names nobody still there stays in the database, for the
  tenant's purge, and is not archived. Nothing to change; a key store's
  listing shows one more blob per tenant that left.

## The trail's archive takes in only what it can account for

**Applies if** you keep the trail's archive (`audit.archive`, or
`trail.Policy.Archive`). Nothing in your code changes shape; four things the
archive did change (part of payday#115).

- **Files of the directory format are adopted by the first pass alone**: the
  one before the archive's first checkpoint. A file of that format put in
  `audit.archive` after it is `unaccounted` to `trail verify`, and is read,
  filed and signed by nothing until an operator accepts it
  (`policy.Accept`). A deployment that upgraded from the directory format has
  had its first pass; one that restores an old file into the directory now
  accepts it once.
- **A chunk whose bytes are not its digest is written again by nothing.** A
  fold, `Policy.Forget`, `Policy.PurgeTenant` and the filing of an old file
  read what they copy through its digest, and stop at a chunk that is not it:
  `trail verify --full` says `altered`. `Forget`, `PurgeTenant` and `Purge` go
  on with the rest, write their receipts, and answer an error naming each such
  chunk, which is `trail.ErrAltered` -- what a verified read answers for one.
  An erasure that answers it has done everything else: look at the chunk, and
  erase again once it is settled.
- **A discard window destroys what the archive holds of it**, once it is as
  old as `retain`: what a legal hold sent there instead of discarding it, once
  the hold lifts, and what a longer window kept there before it was shortened
  to a discard. It used to be kept for ever. `policy.Preview` counts it.
- **`pd gen` refuses a field of `Audit` or `Access` named or JSON-named
  `sealed`**, the member a sealed row keeps what it said in
  (`trail.SealedMember`). An overlay with one renames it.

## A page's store is named by what it sees, and every call it makes says so

**Applies if** a page of yours uses `@lesomnus/payday`'s store or query layer,
or your server serves `auth/authsession` -- `Sessions.Handler`, `Serve`, or
`IdentityOf`.

- **`authsession.IdentityOf(v Session)`** digests what the session lets its
  caller see -- the actor, the tenant, the grant -- where it took the key.
  `GET /session`'s `identity` is that, so one person's sign-ins with one grant
  share a store. Mirrors named the old way are other names now, and go with the
  rest below.
- **Every call `Queries` makes carries `payday-identity`**
  (`authsession.IdentityHeader`, `IDENTITY_HEADER`), and `Sessions.Handler`
  refuses a call whose session that store is not named by, as
  `ErrOtherIdentity` -- `Unauthenticated` on the wire, with the cookie kept. A
  call that names no store is served as before. `web.Cors` allows the header;
  a CORS list of your own has to.
- **A write refused `Unauthenticated` waits** rather than being refused, and
  goes once its caller signs in again. So a page that forgets its store on a
  401 throws away what this keeps for it: let go of the store and ask again who
  is signed in, and forget only on a sign-out, in every tab. `pd new`'s
  `ts/src/session.tsx` is the shape.
- **`deleteOtherDisks(at)`** in `@lesomnus/payday/store/idb` deletes every mirror
  of the app but `at`'s. Ask for it once the caller's own is open, and do not
  wait on it.
- **A mirror lets go of itself** when another page deletes it or moves it to a
  later version, and what then needs the disk fails saying so. `openDisk` and
  `deleteDisk` reject, rather than wait, while a page from before this holds one
  open.
- **`send` refuses a patch** -- a request carrying a `patch.Patch`, as `Apply`
  does -- to an entity that declares a secret. Send those with `call`.
- **`Sessions.KeyOf`** answers nothing for two cookies of its name, and reads a
  cookie line a pair at a time. The `GET` and the `DELETE` read through it too.

## The access record: a write's row in its transaction, a download alone, a read's request only

**Applies if** you wired the access record (the entry below). Regenerate; what
changes is what it does, and one thing of your `serve` (payday#111).

- **A write's row is in its transaction even where nothing waits for a
  commit** -- a write the servers make in a transaction of their own. A write
  whose row cannot be written is refused, rather than made and recorded
  nowhere.
- **A download is refused as an operation of a batch.** It says what it is
  for and is recorded before its answer leaves, which an operation cannot do.
  Call it by itself.
- **A row keeps a read's or a download's request, and nothing of a call that
  wrote or was refused.** What the request's message does not declare is
  dropped, and one larger than `access.MaxRequest` is not kept.
- **A reason is text**, at most `access.MaxReason` bytes; anything else is
  refused.
- **An `Any` is read only where payday packs one** -- a batch's operations and
  answers, a watch's events -- and once. One your rows hold is not read for
  whose data it carries.
- **The records payday keeps are no operation of a batch**: the trail, the
  outbox, the archive's account, the access record.
- **Write the queue once more after the server has stopped.** `pd new`'s
  `Serve` waits for `GracefulStop` and calls `s.Access.Flush`: what the server
  answers while it drains is queued after the writer's own last write.

## An access record beside the trail

**Applies if** you generate with `pd gen`: `Access` is a new payday entity, copied
in like `Audit`, so migrate. Nothing is recorded until it is wired (payday#111).

- **The recorder**: `pd.AccessRecorder()` in the `bare.Recorders` the servers
  are built with, after the trail's. It writes a write's row at its commit.
- **The writer**: `w := &access.Writer{Sink: pd.AccessSink(client)}`, appended
  to what `spin.Run` is handed -- it is each process's own, and names itself.
  A read's row waits in it for a second.
- **The interceptor**: `access.Config{Subjects: pd.Subjects, Refs:
  pd.SubjectRefs, Address: ..., Downloads: ..., Writer: w}.Interceptor()` in the
  chain, after `auth` and the limiter and before `gate.Interceptor`.
- **`Address`** is where a call came from. `access.Peer` is the connection's,
  which is the caller's only when nothing stands in front of the app; behind a
  proxy, answer with what the deployment trusts of its headers.
- **`Downloads`** are your methods that hand personal data out to be kept
  somewhere else, by full name. Each is refused without a `payday-reason-bin`
  header, so a client of one sends it: a gRPC client sets the text, and a
  page or `curl` writes it base64, since it is a `-bin` key (connect-es has
  `encodeBinaryHeader`).
- **`access.Saw(ctx, ...)`** in whatever shows a person's data where no
  `personal` field carries it: a name rendered into text, a column of a file.
- **Retention**: rows are a kind of the trail's, `access`, on `audit.by.access`.
  A deployment under PIPA says `profile: pipa` or `pipa-sensitive` there.
- **`pd new`'s `cmd/serve.go` has all of this**, to compare against.

## An entity no server writes says how its writes reach the trail

**Applies if** an entity of yours has no `Add`, `AddMany` or `Patch`: `pd gen`
refuses it until it says (payday#108).

- **`trail: {recorded: {}}`**: the app writes it through ent and records each
  write with `pd.Record`, in the transaction that wrote it. A check of the rows
  reads it as any kind, and a write that was not recorded is a row nothing on
  the trail is about.
- **`trail: {off: {why: "..."}}`**: the app writes it and keeps it off the trail
  -- a session's idle clock, a lock, a queue a loop empties. A check does not
  read it, `pd.Record` passes over it, and `why` is required.
- **On an entity a server writes**, `trail` is refused: the recorder puts its
  writes on the trail already. A write of yours through ent on such an entity
  is still recorded with `pd.Record`.
- **This replaces the rule the entry below came with** -- a kind with no `Add`
  or `Patch` was left out of the check for having none, which left out a kind
  kept to prove something, such as a consent. Whether an entity has RPCs is now
  only what its API offers.

## A check of the rows reads the archive, and what nothing accounts for is a finding

**Applies if** you call the generated `pd.Digests`, or write rows beneath your
servers -- a restore, a migration, anything through ent on a kind that has a
generated `Add` or `Patch` (payday#108).

- **`pd.Digests(ctx, db, digest, policy)`** takes the trail's policy, and reads
  its archive for a row whose newest line of the trail has left the database.
  `trail.Policy{}` reads none. `db` is the database: each page is read in a
  read-only transaction of its own.
- **`Rows.NotOnTrail` is the rows, not a count**, and `Rows.Ok()` is false while
  there are any: a row nothing on the trail is about is what a row added past
  the servers looks like. `Rows.Archived` says how many were checked against the
  archive.
- **`pd.Record(ctx, db, contents, action, ids)`** puts rows you wrote beneath
  the servers on the trail as the act that wrote them: call it in the
  transaction that wrote them. Give the operator a command that records what
  `pd.Digests` found once somebody has looked.
- **An entity may declare no RPC** -- `option (orm.message) = {}`, with no
  `rpc` -- and no layer wraps a method it does not have (the gate, the secret
  layer, the recorder's reads), which is new; before this such an entity did
  not compile. How its writes reach the trail is the entry above.
- **`audit.CheckRows` and `audit.Newest` are gone**; `audit.CheckKind` is what
  the generated code calls.

## A row of the trail keeps a digest of the row its write left

**Applies if** you generate with `pd gen`. `Audit` has two new columns,
`digest` and `digest_fields`, so migrate. Rows written before keep neither, and
nothing is digested until `audit.digest.key` names a key.

- **`audit.digest.key`** (`<APP>_AUDIT_DIGEST_KEY`) is base64 of at least 32
  random bytes (`openssl rand -base64 32`). Give it as the file it is in, or as
  `base64:` and the key. Keys rotated out go in `audit.digest.previous`.
- **The generated `pd.Digests(ctx, db, digest)`** checks the live rows against
  the trail. Give the operator a command that runs it.
- **An overlay on `Audit`** is written a row at a time, as before.

## `pd doctor` asks about a field that may hold a person

**Applies if** an entity of yours has a field declared `personal`, or a field
whose name is about somebody (`email`, `phone`, `address`, `birthday`,
`first_name`, ...). Nothing refuses anything; `pd doctor` says more than it did.

- **`(payday.field).personal` has presence**, so `personal: false` says a field
  was looked at and is about no one in particular -- a role, a state, a kind --
  and is a different statement from saying nothing. The trail treats the two
  alike.
- **`pd doctor` asks of every field of free text on an entity that holds people**
  -- one with a personal field -- and of any field named for something about a
  person, whether it is one. Answer each, beside it: `personal: true` or
  `personal: false`. The finding names the file and line.

**Ordering**: an app compiles its schema against `buf.build/payday/payday`, so
`personal: false` means anything only once the module it pins has the presence.
Until then it compiles, says nothing, and `pd doctor` keeps asking. `buf dep
update` moves the pin. (An `impersonal` field said this for a few hours before
it; nothing wrote it, and it is gone.)

## The page is held to payday's floors, and `pd upgrade` meets them

**Applies if** `go tool pd versions` marks `@bufbuild/protobuf` or
`@bufbuild/protoc-gen-es` with `!`: `pd gen --ts` refuses to write until they
are at payday's floor (`pdcli.ErrBelowFloor`).

- `go tool pd upgrade` moves every page package that is below its floor to the
  version `pd new` writes, along with the packages it moves with. It runs
  `npm` and commits nothing. `pd doctor` lists the other floors, and those
  refuse nothing ([upgrading](guide/upgrading.md)).
- Upgrading payday is `go tool pd upgrade --payday <commit>` now. It runs
  `go get` and `go mod tidy`, then the new payday's own upgrade and
  generation.
- The template's lower bounds are real releases now. The old `vite: ^7.1.14`
  named a version npm never published, so an app still declaring it with
  nothing installed is read as below Vite's floor (7.2.0).

## An index is in the order it declares, and a tenanted list's begins with the tenant

**Applies if** you generate with `pd gen`, which is every app: the ent schema
of any index that declared an edge before a field changes, and so does the
database the migration makes (lesomnus/rove#122).

- **An index's columns are in the order its refs are.** protoc-gen-orm-ent wrote
  every edge after every field, so an index declared `(party, issued_at)` was
  `(issued_at, party_id)` -- the wrong one for the reads it was declared for.
  After `pd gen` it is `(party_id, issued_at)`, under the name of those columns,
  and the next migration makes it beside the old one -- which it leaves:
  ent's migration drops no index, and the old one goes on being written on
  every insert and read by nothing. Drop each old name once the migration has
  made its replacement; on PostgreSQL `DROP INDEX CONCURRENTLY IF EXISTS
  <name>`, one statement at a time and outside a transaction, which blocks no
  read or write while it waits out the transactions already using the table --
  a plain `DROP INDEX` takes the table's strongest lock and queues every query
  behind it. rove's and roster's `Replaced` lists are what that looks like. An index whose fields all came before its edges -- `(alias,
  tenant)`, say -- is as it was.
- **`pd gen` prints the warnings it was given.** The generation's own warnings
  were dropped whenever it succeeded, so an app may see a list of them the
  first time. The one that matters here is the list index:

  ```
  protoc-gen-pd: app.Robot: list: no index begins with (tenant, date_created, id), ...
  ```

  An entity behind the wall is read narrowed to its tenants, so the index its
  list walks begins with the tenant. Put `tenant` first in its `page` index --
  `refs: [{name: "tenant", number: 2}, {name: "date_created", number: 15}, {name:
  "id", number: 1}]` -- and do the same for the index of each named order.
- **payday's own Holder and Tenant have a list index now**: `(tenant,
  date_created, id)` and `(date_created, id)`. They come with the copy `pd gen`
  makes; the migration adds them.

On a database with rows, each of these is an index built while the table is
locked for writes. PostgreSQL builds one on a million rows in seconds; a
deployment that cannot pause writes for that long builds them `CONCURRENTLY` by
hand first, under the names the migration expects.

## The trail keeps what a write said, and no value about a person

**Applies if** you generate with `pd gen`, which is every app: `pd.Recorder`
takes an argument now, so `cmd/serve.go` does not compile until it is given one
(#92).

- **`pd.Recorder` takes what the trail keeps of each kind's writes**, which is
  the deployment's to say (`audit.content`). It is asked for rather than
  defaulted, so that a setting is not something an app can read and then forget
  to hand over:

  ```diff
  -rec := bare.Recorders{pd.Recorder(), pd.ResourceRecorder(w, c.Watch.Outbox, nil, resource.RecorderOptions{})}
  +content, err := c.Audit.Contents()
  +if err != nil {
  +	return nil, err
  +}
  +rec := bare.Recorders{pd.Recorder(content), pd.ResourceRecorder(w, c.Watch.Outbox, nil, resource.RecorderOptions{})}
  ```

  `audit.Contents{}` is the default, for a test that builds a server of its own.
- **By default a row of the trail is the write's patch, and not its row.**
  `Audit.patch` is the document the write was compiled from, and an Add -- which
  was compiled from none -- is recorded as the row it made, written as a patch
  from the empty row; `Audit.value` is empty. A deployment that wants the row
  asks for it, for the whole trail or for one kind:

  ```yaml
  audit:
    content: patch            # fields | patch | value; empty is patch
    by:
      reading:
        content: value        # a machine's record, kept whole
  ```

  `fields` keeps which fields a write set and nothing they were set to. Anything
  of yours that read `Audit.value` for what an Add made reads `Audit.patch` now:
  applied to an empty row, it is that row less its key.
- **`Audit.fields`** is a new column, the numbers of the fields each write set.
  Migrate. Rows written before it have none.
- **`(payday.field).personal`** marks a field about a person -- a name, an
  address, a phone number. It is answered with like any other field, and on the
  trail as a value at **no** level: what it was set to is taken out of `patch`
  and cleared out of `value`, and its number is in `fields`. Declare yours. An
  entity that holds people and things in one table declares the shared field:
  there is no declaration that depends on the row. It is refused on an edge, on
  the key, and beside `secret`, which already says everything it would.
- **payday's `Holder` declares `alias`, `name`, `desc` and `labels` personal**,
  so after regenerating, a holder's trail says what was set and not to what. Who
  a line is about is still on it: `actor_id` and `object_id`.
- **A `secret` is matched by its name as well as its number.** A document an
  `Apply` was handed that named a secret field by its name or its JSON name
  carried the value onto the trail, the outbox and the in-process publish; it
  does not now.
- **`trail.Policy.Forget` is still what takes out what no declaration reaches**:
  free text, rows written before this, and a field nobody declared on a kind
  kept at `value`.

**Ordering**: `personal` is in payday's buf module, like `secret` was, so an app
depending on `buf.build/payday/payday` needs a version of it that has this
before its copy of `Holder` compiles. Until then the compile says the field
`personal` is not in `payday.Field`.

## An entity may ask for AddMany

**Applies if** you generate with `pd gen`. Regenerate; nothing else has to
change -- unless a layer of yours overrides `Add` on an entity you then give
an `AddMany`.

- **`rpc: {crud: true, add: {many: {}}}` adds `AddMany`** (#84): up to a
  thousand of an entity's `Add` requests in one call, written in one
  transaction, a statement for every few hundred rows. Each item is the row an
  `Add` of it would have been, and each is asked what an `Add` is: the gate,
  the Sink's name and tenancy, the Secret layer on the answer, a line of the
  trail. It comes from protobuf-orm (`orm`'s `rpc.add.many`), so the module's
  pin moves: `buf dep update` once `buf.build/orm/orm` has it.
- **What payday's layers say of an `Add` is a rule now**, put on the server
  behind with `bare.<E>WithAddRule`, so an `AddMany` asks it of every item. The
  generated `gate<E>.Add` and `sink<E>.Add` are `admit`; an `Add` that tries
  again for a name it made up is still an override, and so is an `AddMany`'s.
- **A layer of yours that overrides `Add`** says nothing about an `AddMany`,
  which comes from the server behind it. Write the rule with
  `bare.<E>WithAddRule` ([server guide](guide/server.md#a-rule-about-the-rows-an-add-writes)),
  or override `AddMany` as well.
- **Alone, an `AddMany` asks of each item**, as that many `Add`s would, and
  writes the trail a row at a time; in a transaction begun with
  `dialect.BeginTx` -- an operation of your own, or a batch -- each row named
  is asked once and the trail is written by the commit. A batch counts it as
  one operation.
- **An `AddMany` tries again for a name it made up**, as an `Add` does, but as
  a whole call, so a call of many is refused more often than an `Add` of one;
  see `slug.Tries`.
- **`pd gen` refuses an RPC `protoc-gen-orm-service` writes that payday does
  not know**: a layer inherits what it does not override, so a new one would
  have been served past the gate. And none of payday's own entities may ask for
  `AddMany`.
- **An entity with both an alias and a `stamp:` or `agrees:`** had only its
  name decided at `Add`, and its tenancy never asked. Both are now. Nothing
  this repository knows of declared both.

## A transaction remembers what it asked without being told to

**Applies if** a layer of yours calls `seen.For`. Delete the line and
regenerate.

- **`seen.For` is gone** (#84). What the gate and the sink were told about a
  row is kept by the transaction `dialect.BeginTx` began (`dialect.Keep`), so
  every such transaction asks each question once for the rows it names. Nothing
  is said beside `BeginTx` for it -- and a layer that left the line out asked
  everything again, with nothing to say it had.
- **The generated `Gate` keeps the driver it was put on**, in `WithDriver`:
  that is how it finds the transaction. A gate that was not put on one asks
  every time, as before.
- **`seen.Visible` and `seen.Tenant` take that driver**, and `Tenant` takes no
  context. Only generated code calls them.

## A transaction's trail is written by its commit

**Applies if** a layer of yours writes several rows in a transaction begun with
`dialect.BeginTx`, or reads or changes the trail inside one. Regenerate;
nothing else has to change.

- **The rows of the trail a transaction begun with `dialect.BeginTx` records
  are written together** (#84): five hundred to a statement as they are
  recorded, and the rest by its commit, where each was a statement of its own.
  In rove's import, with the row each write hands over (below), that was a
  third of what a row sent: 27.8 statements a row, and 18.8 now.
- **They hold or fall with the transaction as before.** The commit is what
  writes them, so a transaction cannot be ended with any left out, and one
  rolled back takes them along. What a row says is decided when the write is
  recorded, its `date_created` included.
- **A statement about the trail inside that transaction finds every row it
  recorded**, since what is kept is written ahead of it: a count, a list, the
  blanking an erasure does to a person's rows in the transaction that wrote
  them. What is asked is the statement's SQL, for the trail table's name as a
  word. A statement that reaches the table some other way -- through a view, or
  a function of your own -- finds only what has been written.
- **A trail an overlay added a column to is written a row at a time**, as it
  was: the recorder fills only payday's own columns, and a column it cannot
  fill is one only the generated server can.

## A write hands the trail the row it left

**Applies if** you generate with `pd gen`. Regenerate; nothing else has to
change. An app that runs `ent generate` itself adds one feature.

- **A line of the trail holds the row its write handed over, rather than one
  it read again** (#84). The recorder read the row after every write, which the
  write had just read itself -- a statement for every write. An Add's INSERT
  answers with the row now, in the same statement where the dialect has
  `RETURNING` (PostgreSQL and SQLite; on MySQL it is still a query), and a
  Patch or an Apply hands over the row it read back to answer with. Only an
  Erase is read as before. An import into rove sent 37 statements a row and
  sends 28.
- **ent generates with `sql/returning`**: the generated servers ask a create
  builder for the row it stored (`Stored`), which only a client generated with
  that feature has. `pd gen` passes it. Anything that runs `ent generate` over
  payday's schema itself adds `--feature sql/returning` beside `sql/modifier`.
- **A recorder of yours beside the trail is handed the row too**, since a
  `bare.Recorders` asks for every image any of its recorders asks for: an Add's
  and an Apply's `Change.Value` is set whether or not yours asked. It is the
  row whole -- the trail keeps a `secret:` field out of what it holds on a
  copy -- and the ORM's word holds: treat it as read-only.

## A transaction asks about each row it names once

**Applies if** a layer of yours begins a transaction and writes several rows in
it. Regenerate; nothing has to change, and one line makes it cheaper.

- **The gate's read of a reference, and the sink's read of its tenant, are
  asked once in a transaction begun with `seen.For`** (#84). Each was asked for
  every reference of every write -- an import into rove sent 68 statements a
  row, 33 of them those two questions, and sends 37. A generated batch
  begins its transaction that way already. A layer of your own adds the line
  beside its `dialect.BeginTx`, with the driver it began **on**:

  ```go
  drv, tx, err := dialect.BeginTx(ctx, s.drv)
  if err != nil {
  	return nil, err
  }
  defer tx.Rollback()

  ctx = seen.For(ctx, s.drv)
  ```

  Without it every question is asked as before. `seen` says what is remembered
  and why a write does not make it forget.

## An acceptance carries a restored database on, and is clean at once

**Applies if** you restore an app's database from a backup, or accept what
`trail verify` found. Regenerate; a `trail.Store` of your own changes as
below.

- **A database restored from before the latest checkpoint goes on once
  accepted.** It is `behind` -- it has numbered fewer checkpoints than the store
  keeps -- and no act takes a checkpoint while it is, as before. The acceptance
  that was to end that was refused for the same reason, so nothing took one
  again (#70). `Policy.Accept` now numbers its checkpoint after the newest the
  store keeps, by what each says, and names that one as the one before: the
  checkpoints from after the backup stay what the archive held, and the
  database goes on from there.
- **`Verify` is clean as soon as an acceptance is the latest checkpoint.** It
  compared the rows with the one before as well, which is what the operator had
  accepted they no longer add up to, so it went on finding that one `rewritten`
  until the next act took a checkpoint.
- **`Manifest.Checkpoint` takes `after`**: the next number is after the latest
  and after `after` as well, in the same transaction that takes it. The
  generated store has it once regenerated. One of your own moves its counter
  past `after` when that is higher:

  ```go
  func (s store) Checkpoint(ctx context.Context) (int, []trail.Archived, error)            // was
  func (s store) Checkpoint(ctx context.Context, after int) (int, []trail.Archived, error) // is
  ```

## A list reads the other way round, and in orders the schema names

**Applies if** an entity declares `list:`. Regenerate, and nothing else has to
change.

- **Every List request has `reverse`** (field 4), which reads the order every
  column the other way round. The index the order walks walks from the other
  end, so nothing has to be declared for it. The generated commands' `ls`
  (`pdcmd`) takes it as `--reverse`.
- **`list.orders`** declares more orders a request may name, each by a name
  and each ending in the key. A list that declares any has an `order` enum in
  its request (field 5); `--order NAME` names one at a terminal. Declare an
  index for each, in its directions; nothing checks that one is there.
- **A cursor is of the order and the way it was read**, and one carried on in
  another is refused. The cursor of the list's own order read forwards keeps
  the shape it had, so clients paging across the upgrade carry on.
- `list.orders` is a field of `payday.proto`. An app that takes payday's buf
  module from the registry (`buf.build/payday/payday`) can declare named
  orders once the registry has a version with it, and `buf dep update`
  brings it in. `reverse` needs nothing from the registry: it is a field of
  the generated contract.

## Every act ends in a checkpoint, and `trail.Archive` takes the policy

**Applies if** you call `trail.Archive`, or any act on the trail's archive —
`Policy.Forget`, `Policy.Purge`, `Policy.PurgeTenant`, `Policy.Collect`,
`Policy.Accept` — and read what it answers.

- **`trail.Archive(ctx, s, p, of, before)`** takes the policy where it took an
  archive, and writes into `p.Archive` as a pass does: sealed with `p.Keys`, its
  manifest's rows signed by `p.Signer`, ending in a checkpoint. It used to seal
  nothing and sign nothing, so in a deployment with a key its rows were
  `unsigned` until accepted. It still asks nothing of the windows or the holds.

  ```go
  n, err := trail.Archive(ctx, store, of, before, flob.NewOsStores(dir)) // was
  p.Archive = flob.NewOsStores(dir)                                      // is
  n, err := trail.Archive(ctx, store, p, of, before)
  ```

  Hand it the policy the deployment runs with, so that the checkpoint it ends
  in goes where the others do. A directory of the operator's own for `p.Archive`
  is a manifest that says the archive holds chunks it does not, and, with no
  `p.Checkpoints`, checkpoints in that directory and not beside the others.
- **Every act that changed the archive ends in a checkpoint**, as a pass does;
  one that changed nothing takes none, and nor does a question (`Doomed`,
  `PlanTenantPurge`, `Preview`, `Read`). What an act answers stands; when the
  checkpoint after it could not be taken, its error says so as well, joined to
  whatever else it says. A device that signs is asked once more per act.
- **No checkpoint is taken over rows that no longer add up to the latest one.**
  Once somebody has changed them, every act's error says so, until an operator
  accepts: `Policy.Accept` ends in a checkpoint of its own that says why
  (`Checkpoint.Accepted`). Nothing is settled meanwhile.
- **A loop that forgets one person per call** takes a checkpoint per person.
  Hand `Policy.Forget` everybody who is due at once where it can.

A `trail.Store` of your own changes nothing.

## A key in a device is a line of configuration

**Applies if** you hand in a `crypto.Signer` for a TPM, an HSM, Vault or a
cloud KMS as `trail.Policy.Signer`, or would rather not have to.

Nothing changes for a key in a file or the PEM document. `audit.checkpoints.key`
also names a key in a device now, by its URI, once the app imports what speaks
to the device — see [the runtime](runtime.md#keys-in-a-device):

- **Import the one you use**, in `cmd` beside the database driver:
  `config/signerpkcs11` (which needs cgo), `config/signertpm` or
  `config/signervault`. AWS KMS is a module of its own: require
  `github.com/lesomnus/payday/trail/trailawskms` and register it,
  `config.RegisterSigner(trailawskms.Scheme, trailawskms.Open)`.
- **A signer of your own** is registered the same way, under a scheme of its
  own, and is then configuration too; the code that set `Policy.Signer` can go.
- **A key the device would hand out is refused at start**: a PKCS #11 key that
  is extractable or not sensitive, a TPM key that is not `fixedtpm` and
  `fixedparent`, a Transit key that is exportable or allows a plaintext
  backup. Make one that is not; what the old key signed stays verifiable by
  its public half in `audit.checkpoints.trust`.
- **A key in a device signs every row of the manifest an act writes**, as the
  key in a file does, and not only a checkpoint: one across the network is a
  request a row, which a pass that moves many chunks makes many of.
- **A value that begins with a scheme an imported package registered** is that
  device's key, and not a file of that name.
- **`audit.keys.master` and `audit.keys.identity` read an age plugin's** too —
  `age1yubikey1…` and `AGE-PLUGIN-YUBIKEY-1…` — for a master in a device, run
  as `age-plugin-<name>` from PATH. The master is sealed to once at start now,
  which also refuses recipients age will not seal to together.

## The trail's manifest is signed, and its checkpoints chained

**Applies if** you sign checkpoints — `audit.checkpoints.key` or
`trail.Policy.Signer` — keep them on a store of your own, call `trail.Archive`,
or wrote a `trail.Store` of your own (a test double counts).

Every row of the manifest is now signed by the key that signs checkpoints, so
that a row changed after the latest checkpoint is found too, and each
checkpoint names the one before it — see
[the runtime](runtime.md#after-the-latest-checkpoint). For an app:

- **Regenerate, and migrate once.** `Archived` has two more columns,
  `signature` and `signed_by`. They are nullable, so a table that has rows
  takes them, and they are empty on every row there.
- **With a key, accept those rows once.** A row with no signature is `unsigned`
  until an operator accepts it: `policy.Accept(ctx, store, why)` signs each as
  it stands, and needs the key to. Until then a pass says so, and does not act
  on their chunks — nothing of theirs is destroyed or folded, and
  `Policy.Read` leaves them out. Without a key nothing changes.
- **A `trail.Store` of your own** is handed a `trail.Sign` with every write —
  `Adding`, `Added`, `Mark`, `Label` — and asks it for the signature to write
  beside each row, a row at a time, with every field the signature is over in
  the same write. `Mark` from a state to itself signs rows again as they are.
  The generated store does all of it.
- **`trail.Archive` signs nothing**, since it is handed no policy: with a key,
  the rows it writes are findings until accepted. A pass moves that
  deployment's rows. (It is handed the policy since
  [the entry above](#every-act-ends-in-a-checkpoint-and-trailarchive-takes-the-policy).)
- **On a store that refuses deletion, say so**: hand it in as
  `trail.WriteOnce(store)`, or set `audit.checkpoints.write_once` beside
  `audit.checkpoints.dir` on a WORM mount.
- **The findings** `unsigned` and `forged` are about rows too; `reordered` is
  new; `unkept`, `rewritten` and `relabelled` are about the chain as well.

Checkpoints written before name nothing, and the chain starts after them.

## The trail's archive can seal what its rows say

**Applies if** you want a tenant that leaves to take its trail out of the
archive's backups too, or you read the archive with `trail.Read`,
`trail.ReadTenant` or `trail.ReadFiles`.

Nothing changes without it. With `audit.keys` (or `trail.Policy.Keys`), what
each row says is sealed on its way into the archive, a key per tenant, and the
master's private half opens it — see [the runtime](runtime.md#sealed-contents).
For an app that turns it on:

- **Give the commands that read the master, and nothing else.** An export or a
  subject access request reads through `policy.ReadTenant`, which opens what
  it reads with `Keys.Identities` — `audit.keys.identity`, so
  `<APP>_AUDIT_KEYS_IDENTITY` for one run — and refuses a sealed row without it
  (`trail.ErrSealed`). The serving process is never given it.
- **`trail.Read`, `trail.ReadTenant` and `trail.ReadFiles` refuse a sealed
  row**, since they take no key. Read through the policy, or open a row with
  `Keys.Open`.
- **`TenantPurge.Keys` and `Receipt.Keys`** say how many keys a tenant's purge
  destroyed, which it does first, with a receipt `where: keys`.
- **Back the key store up briefly**, apart from the archive.

Chunks written before stay as they are and stay readable; rows a pass files from
`_legacy` are sealed on the way.

## The database keeps an account of the trail's archive

**Applies if** you set `audit.archive` or `trail.Policy.Archive`, call
`Policy.Purge`, `Policy.Doomed` or `trail.ReadTenant`, or wrote a `trail.Store`
of your own (a test double counts).

Every change to the archive is now written into a manifest in the database
first, and `Policy.Verify` compares the two — see
[the runtime](runtime.md#the-manifest). For an app:

- **Regenerate, and migrate once.** `pd gen` copies in one more entity of
  payday's, `Archived` (domain 5, which payday kept for this), and `trail.Store`
  gains the manifest's methods, which the generated store implements on it.
  The migration is one new table.
- **`Policy.Purge` and `Policy.Doomed` take the store**,
  `policy.Purge(ctx, store, cut)`, because what they destroy is decided by the
  manifest, which is in the database.
- **Export through the policy.** `policy.ReadTenant(ctx, store, tenant, fn)` and
  `policy.Read(ctx, store, fn)` read only what the manifest accounts for and
  check each chunk's bytes; anything that hands a tenant's trail to somebody as
  the trail — an offboarding export, a subject access request — wants those.
  `trail.ReadTenant` and `trail.Read` are unchanged, and read the archive as it
  is.
- **Decide where checkpoints go**, optionally: `audit.checkpoints.dir` for a
  directory of their own (`trail.Policy.Checkpoints` for anything else), and
  `audit.checkpoints.key` to sign them — Ed25519, ECDSA or RSA, a file or the PEM
  document — or a `crypto.Signer` of your own as `trail.Policy.Signer`, for a
  TPM, an HSM or a cloud KMS. With neither, they are kept in the archive and
  unsigned.
- **Give an operator a way to verify**: a command that calls
  `policy.Verify(ctx, store, full)` and prints the findings, and one that calls
  `policy.Accept(ctx, store, why)` once they have looked.

Nothing has to be run on the upgrade: the first pass fills the manifest from
what the archive holds, and files what is in `_legacy` by tenant.

## Erasing from the trail answers to a legal hold

**Applies if** you call `pd.ForgetInTrail`, `trail.Forget`, `trail.Collect`,
`trail.Leave`, `trail.Doomed` or `trail.Purge`, or wrote a `trail.Store` of
your own.

A tenant's answer can now carry a legal hold, `trail.Tenant.Hold`, and nothing a
held tenant may read stops existing while it is on — see
[the runtime](runtime.md#legal-holds). Every act that destroys something of the
trail had to be able to see the hold, so they are methods of `trail.Policy` now,
and the functions they replace are gone rather than kept beside them: a caller
still written against those would erase through a hold without knowing it.

| Was | Is |
| --- | --- |
| `pd.ForgetInTrail(ctx, db, objects)` and `trail.Forget(ctx, archive, encoded)` | `policy.Forget(ctx, pd.TrailStore(db), objects)`: the database and the archive in one call, answering `trail.Forgotten` with what a hold kept |
| `trail.Collect(ctx, s, of, before)` | `policy.Collect(ctx, s, of, before)`, answering what a hold kept as well |
| `trail.Leave(…)` | `trail.Archive`, or `policy.Collect` |
| `trail.Doomed(ctx, archive, cut)`, `trail.Purge(ctx, archive, cut)` | `policy.Doomed(ctx, cut)`, `policy.Purge(ctx, cut)` |
| `trail.Store` | gains `Heads` and `Blank`; `trail.Scope` gains `Objects`, and `trail.Whose` gains `Sharing` |

A caller with no policy of its own — a test, a command that was handed an
archive — means `trail.Policy{Archive: archive}`, which holds nothing. The
store is generated, so `pd gen` writes the new one.

And one thing is new: `policy.PurgeTenant` takes a leaving tenant's trail out,
and `policy.PlanTenantPurge` says what it would take. See
[the runtime](runtime.md#a-tenant-leaving).

## The trail's archive is a flob store, kept per tenant

**Applies if** you set `audit.archive`, call anything in `trail` beyond `Sweep`
and `Policy.Pass`, call the generated `pd.ReadTrail`, or wrote a `trail.Store`
of your own (a test double counts).

`trail.Policy` can now answer per tenant — `Policy.Tenants`, and floors in
`audit.min` — and the archive moved to a [flob](https://github.com/lesomnus/flob)
store with a namespace per tenant so that a tenant's window can be applied to
it. See [the runtime](runtime.md#and-per-tenant-when-the-app-answers-for-them).
With nothing new configured, what is kept and for how long does not change.

- **Nothing to run for the archive.** `audit.archive` is the same directory. The
  first pass adopts the files a version before wrote into it, by hard link, into
  a namespace of their own, and removes them from the directory; they are
  destroyed by the month in their name, as before. A copy taken before the
  upgrade is still readable with `trail.ReadFiles`.
- **One migration**, an index on `Audit (tenant_id, domain, date_created)`. Plan
  it as you plan any other.
- **The code:**

  | Was | Is |
  | --- | --- |
  | `trail.Policy.Archive` a directory | a `flob.Stores`; `config.AuditConfig.Policy()` makes one from `audit.archive` |
  | `trail.Archive(ctx, s, of, before, dir)`, `trail.Leave(…, dir)` | the same with the archive in place of `dir` |
  | `trail.Files(dir)` | `trail.Chunks(ctx, archive)` |
  | `trail.Read(paths, fn)` | `trail.Read(ctx, archive, fn)`, or `trail.ReadTenant` for one tenant's; `trail.ReadFiles(paths, fn)` for files on a disk |
  | `pd.ReadTrail(paths, fn)` | `trail.Read(ctx, archive, pd.TrailOf(fn))` |
  | `trail.Doomed(dir, cut)`, `trail.Purge(ctx, dir, cut)` | `(ctx, archive, cut)`, answering `[]trail.Chunk`; `Purge` writes a receipt |
  | `trail.Forget(dir, objects)` | `trail.Forget(ctx, archive, objects)` |
  | `trail.Named`, `trail.Writer`, `trail.NewWriter` | gone: a pass writes chunks |
  | `trail.Store`: `Older(ctx, kinds, at, limit)`, `Count(ctx, kinds, at)` | `Older(ctx, scope, at, cursor, limit)`, `Count(ctx, scope, at)`, and `Tenants(ctx, after, limit)` |

  The store is generated, so `pd gen` writes the new one; a scope of
  `trail.Scope{Kinds: kinds}` is what the old calls meant.
- **An archive that cannot list itself is refused** where the process comes up,
  since nothing could ever be destroyed from it. flob's stores on a disk, in
  memory and on S3 all can.

`pd.ForgetInTrail` and `trail.Forget` erase as they did. What they will do
about a tenant under a legal hold is the next change to them.

## What the ent schema cannot state has a directory of its own

**Applies if** you keep hand-written DDL (an exclusion constraint, a foreign key
that carries the tenant) in the planned migration directory, or in a second
directory that shares its history.

`entschema.Migrations.Extra` is a directory beside the planned one with a
history of its own, applied after it and never planned. See
[the server guide](guide/server.md#what-the-ent-schema-cannot-state). To move to
it:

- **Out of the planned directory.** Those statements are what each plan proposed
  to drop. Move them into new files under `migrations/extra`.
- **Out of a shared history.** Their versions are recorded in `schema_revisions`,
  so the new history starts without them, and on a database that already ran
  them they run again. Write the moved files so that running twice is harmless
  (PostgreSQL has no `ADD CONSTRAINT IF NOT EXISTS`, so guard each one with a
  check of `pg_constraint` in a `DO` block).

## `entschema.Check` leaves a foreign key or a check it does not know about alone

**Applies if** your database holds constraints the ent schema does not
declare, such as a foreign key that carries the tenant or a `CHECK`, or if you
kept them out because the server would not start with them.

`entschema.Check` used to refuse a database that held a foreign key or a check
constraint the schema does not declare: they came back from the diff as drops
and were counted as drift. They are now left alone, the same as an extra column
or index. A foreign key the schema declares and the database lacks is still
refused. So a composite foreign key that keeps a reference inside its tenant can
be a constraint rather than a trigger. payday requires `protobuf-orm/ent`
`v0.0.0-20261010071722-cf7a87c9af59`, so upgrading payday raises an app's pin
with it.

`Plan` has not changed. It still proposes dropping anything in the ent migration
directory that the schema does not declare, so keep those statements in a
directory of your own and apply it after the ent one.

## An overlay may add an index to one of payday's entities

**Applies if** a file in `proto/ext/payday/` declares an `option`, of any kind.

An option an overlay put on one of payday's entities used to be dropped
entirely, without a word, when payday's entity declared the same option. That
is always the case for `(orm.message)` and `(payday.entity)`. protobuf-merge now
merges the two (payday requires
`v0.0.0-20261009205601-8502fad9e678`, so upgrading payday raises an app's pin
with it), and `pd gen` compares the result against what payday shipped:

- An **index** the overlay adds to `(orm.message)` is generated. An overlay
  that carried one all along gets it now, and the next migration creates it. A
  `unique` one fails to create on a table that already holds rows it forbids, so
  look before you deploy.
- **Anything else** an overlay adds to `(orm.message)` or `(payday.entity)` is
  refused, where it used to be dropped. It never did anything, so take it out.

## The configuration is read by xli's `cfg`

**Applies if** anything says `config.For`, `pdcmd.Load`, `pdcmd.ConfigFlag` or
`pdcmd.NewCmdConfig` — every app `pd new` wrote does, in `cmd/config.go` and
`cli/cli.go`.

What read an app's struct from a file and the environment moved to
[`github.com/lesomnus/xli/cfg`](https://github.com/lesomnus/xli/blob/main/cfg/DESIGN.md),
beside the flags it can now bind. payday keeps the blocks (`config.ServerConfig`
and the rest), which read the same.

| was | is |
| --- | --- |
| `var Loader = config.For(Name)` in `cmd` | `l := cfg.New(cmd.Name, c)` in `cli.Cmd` |
| `pdcmd.ConfigFlag()` | `cfg.ConfigFlag()` |
| `pdcmd.NewCmdConfig(cmd.Loader, c)` | `cfg.NewCmdConfig(l)` |
| `pdcmd.Load(cmd.Loader, c)` | `cfg.Load(l, version)`, with `version := pdcmd.NewCmdVersion()` |
| `pdcmd.Reads("KEY_")` | `cfg.New(cmd.Name, c, cfg.Reads("KEY_"))` |
| `config.ReadFile(path, &v)` | `cfg.NewFile[V](path).Read()` |
| `cmd.Loader.EnvNames(&c)` | `l.EnvNames()` |
| `if v, _ := flg.Find[string](cl, "x"); v != "" { c.X = v }` after the load | `cfg.Bind(l, &c.X, &flg.String{Name: "x"})` |

`cfg.Load(l, version)` loads for every command but `version`, so a deployment
whose configuration is wrong can still be asked what build it is.

What a deployment will notice:

- **A key in the file that nothing reads is an error**, with the line it is on.
  It was ignored, which is how a misspelt key became a setting that silently was
  not there. A key starting with `x-` is ignored, for anchors.
- A value from the environment is taken as it is. A list is `a,b` or YAML flow,
  `[a, b]`; it was only the latter.
- In the file, a reference inside `[...]` or `{...}` must be quoted:
  `["${env:A}"]`. The parse error says so.
- `${file:/path}` is for a secret only, in a field of type `cfg.Secret`, which
  reads the file again when it is rotated. A secret written `file:/path`, the
  way roster and shale wrote one, is an error that says how to write it.
- A field with no tag is named in snake_case: `ListenAddr` is `listen_addr`,
  where it was `listenaddr`. Every block payday ships is tagged.
- `010` is ten, as YAML 1.2 reads it. It was eight.
- `config env` lists in the order of the struct rather than sorted; `config`
  prints what was set, with where it came from, and no secrets — by name, as it
  did, and every `cfg.Secret`.

## A UUID is the standard library's

`uuid.UUID` now comes from Go 1.27's `uuid` package rather than from
`github.com/google/uuid`. An app that names the type does so from the new
import path, and drops the dependency.

Nothing moves in the database or on the wire. `database/sql` writes a
`uuid.UUID` out in the canonical form and reads one back, the way it has always
done a `time.Time`, which is the same text `github.com/google/uuid` wrote for
itself; and a UUID has always crossed the wire as sixteen bytes.

What the standard library does not have, `entuuid` does:

| was | is |
| --- | --- |
| `uuid.FromBytes(b)` | `entuuid.FromBytes(b)` |
| `uuid.Nil` | `uuid.Nil()` |
| `uuid.NewString()` | `uuid.New().String()` |
| `uuid.Must(uuid.NewV7())` | `uuid.NewV7()` |
| `v.Version()`, `v.Variant()` | `v[6]>>4`, `v[8]>>6` |

`entuuid` is `github.com/protobuf-orm/protoc-gen-orm-ent/runtime/entuuid`, which
a generated server already depends on. Its `FromBytes` is the one that matters:
a UUID arrives as protobuf `bytes`, where nothing enforces the length.

A predicate written by hand against a UUID column binds the `uuid.UUID`; there
is nothing to convert. The one place that still wants a `driver.Valuer` is
`entql`, which asks for one at the call rather than at the bind:
`driver.DefaultParameterConverter.ConvertValue` is what to wrap it in.

## Every table is named after its entity, and none of them is pluralized

**Applies if** you have a database. It is a rename of every table an app has.

payday builds on a fork of ent, and the fork stopped putting entity names
through an English inflector to decide what to call a table. `User` was `users`
and is `user`; `Person` was `people` and is `person`; `Category` was
`categories` and is `category`. The name is the `snake_case` of the entity now,
so it can be worked out from the schema rather than looked up.

Join tables of a plain many-to-many edge are unchanged -- they are named after
the edge, not the type -- and so are index names, which were already built from
the singular. Foreign key constraint symbols carry two table names and so move
with them: `cards_users_card` is `card_user_card`.

**What to change.** Nothing in your schema or your Go code: the generated
constants follow, and `pd gen` writes them. The database is the work. `pd gen`
plans the rename like any other schema change, and `entschema.Check` refuses to
serve a database that does not match, so a deployment that has not run it will
say so rather than read an empty table.

Anything that names a table itself has to move by hand: raw SQL in your code,
a view, a trigger, a dashboard query, an entry in this file. A query written
against `roles` wants `role` now.

## A message-typed field is stored as protojson, and was storing nothing

**Applies if** an entity of yours has a field whose type is a message you
declared without `orm.message` — a value carried whole by the row that holds it,
with no table and no service of its own. If none does, nothing here applies, and
that is why this went unnoticed: payday's own reference app had no such field
until it was given one.

What was generated was an `encoding/json` column. Every message payday generates
uses the protobuf opaque API, whose fields are all unexported — so
`encoding/json` saw none of them, and every value round-tripped as `{}`. Written
whole, read back empty, with no error on either side.

It is `entpb.ValueScanner` now, which is protojson, in a `jsonb` column on
PostgreSQL and `json` on MySQL.

**What to do about the rows you have.** Nothing, almost certainly: they all hold
`{}`, because that is what the bug wrote. What they do *not* hold is anything to
migrate, so the column can be rewritten in place. If your app worked around this
— storing the same value a second way, or reading it from somewhere else — that
workaround is now the thing that disagrees with the column.

**And the field names travel.** protojson writes `displayName` where the schema
says `display_name`, so a SQL predicate reaching inside the column has to spell
it that way, and renaming a field of such a message is a silent migration
nothing checks.

## A reconnecting watch takes the snapshot again, whatever it asked for first

**Applies if** you run `<app> <entity> watch --retry` and set `skip_snapshot` on
the request, or you wrote a client that does the same thing.

`skip_snapshot` says *I know the current state*. After a gap that is no longer
true: whatever changed while the connection was down was sent to nobody, and a
watch has no backlog to catch up from. So a reconnect clears the flag and asks
for the snapshot, which is the only thing that says what was missed.

What changes for you is volume rather than correctness: a client that sized its
buffers for a delta-only stream gets a full snapshot on every reconnect. The
alternative is holding a row that is wrong until the next write happens to
correct it, which may be never.

## A batch operation is on the trail under its own name

**Applies if** you read the audit trail by `action` — a report, a retention
rule, a filter on a dashboard — and something in your app writes through
`payday.BatchService`. It applies as well to anything that groups writes by the
watch event they arrived in, which the outbox answers differently; see below.

The recorder does not know what the caller asked for, so it asks gRPC. Inside a
batch, gRPC's answer is the envelope — `/payday.BatchService/Do`, for every
operation of every batch — so a hundred different writes were filed as one
action, and "who renamed this" answered with a method that renames nothing.

Each operation is dispatched now in a context where gRPC answers with the
operation's own method, which is the name the caller wrote into the op. A batch
of two Adds is two rows whose `action` is `/app.RobotService/Add`, exactly what
the same two calls made one at a time would leave. The envelope files no row of
its own, because it writes nothing.

**A watch hears it differently down the two publishing paths**, and an app with
the outbox on has both. The in-process publish is one event per call — the
interceptor, once the handler has answered — so the batch arrives **whole**:
`Event.Method` is the batch, and each `Change.Method` inside it is the
operation's. The queue is a row per write and the drainer publishes rows, so the
same batch comes back out of it as one event per operation, each holding its
single change and each carrying the operation's method as `Event.Method`. The
grouping was never in the queue to hand on — a row is written inside the
transaction, and the call it belongs to only ends after the commit — so a
subscriber looking for the batch in `Event.Method` finds it on the immediate
event and never on the drained one.

**What to change.** A query or a retention rule matching
`/payday.BatchService/Do` matches nothing written from here on — those rows sit
under the operations' own methods now, beside the ones the same writes leave
outside a batch. Rows already written keep the action they were written with;
nothing rewrites the trail.

The seam is [`batch.AsOp`](../batch/batch.go), which the generated dispatch
calls once per operation, and which anything dispatching operations of its own
calls the same way. The queue's half is `Drain` in the generated `server/pd`,
which publishes a queued row at a time and has no call to group them by.

## A soft erase is on the trail under the row it erased

**Applies if** anything reads the trail for erasures, and especially if the
tenant whose row was erased is one of the readers.

`Audit.value` is the row as it was when the event was over, an Erase included,
and `Audit.tenant_id` is whose row it was. A soft erase was the one write where
neither held. The recorder reads the row back through the bare server, whose
every read narrows to the rows still here, so the row the erase had just stamped
answered NotFound and the record took the hard-erase path: the actor's tenant,
an empty value. The wall on the trail is the OR of `tenant_id`,
`actor_tenant_id` and `counterpart_tenant_id`, and an erase names the erased
tenant in none of them — the first two were the actor's and the third is unset
unless the operation said otherwise — so that made the record of an erasure the
one row the erased side could not read.

That read is retried past the erased filter now, inside the same transaction.
A soft erase is filed under the **erased row's** tenant, and its value is the
whole row with the stamp on — the only account of what a row held at the moment
it went.

**A hard erase is unchanged**, and is the exception on purpose: the recorder
runs inside the transaction after the delete, so there is nothing left to read.
It is filed under the actor's tenant with an empty value, which is the last
thing known about it.

**What to check.** Two things move. A reader that found erasures by an empty
`value` finds only hard ones from here on. And a tenant reading its own history
sees the erasures of its own rows, which it could not before — that is the
point of the change, and it is a widening of what that tenant may read.

## An overlay may not change what the file is

**Applies if** an entity overlay of yours — `proto/ext/payday/holder.ext.proto`
and the rest — carries a `features.` option at the top of the file, outside
any message.

The merge unions the file's options, so `option features.field_presence =
IMPLICIT;` written there lands on the **whole** copied entity: every field of
every message in it, payday's own included. What that costs is not obvious,
which is why it is refused rather than documented. On a field something calls
`Has` on, it stops the build, which is the lucky version; on a field nothing
calls `Has` on, it changes only what "not set" means on the wire, and nothing
says so.

The same refusal already covered contract overlays
(`proto/ext/<pkg>/*_svc.ext.proto`); it covers entity overlays now. The line is
easier to carry into one of those: the entity file an overlay is written against
begins with one, so copying its head to get started brings it along, and the
merge used to take it without a word.

**What to change.** Delete the line — `pd gen` refuses and names the file. An
option that is about one message goes **inside** that message, which is where it
is the app's to say and where generation leaves it alone.

## `ls --next` carries on from where it says it does

**Applies if** anything pages through a list with the commands `pdcmd` builds,
or if you mounted a hand-written list RPC into the tree.

The two halves have different names. An answer calls the cursor `next` and a
generated list request calls the same thing `after`, for where it goes rather
than where it came from — and the flag's value never reached the request at
all, so every page was the first page. A first page repeated is exactly what an
honest one-page list looks like, so nothing said so.

`--next` writes `after` now, and falls back to `next` for a hand-written service
that named the field the way the flag is named. A list request with neither is
refused by name rather than dropped:

```
acme.ThingListRequest: has no cursor for --next to carry on from
```

**What to change.** Nothing, for a generated list. A hand-written list RPC
needs a `string` field called `after` or `next` to page at all, and now says so
instead of answering the first page again.

## A grant's methods are patterns, so `*` in one now means something

**Applies if** anything you store and hand to `frame.Grant.To` — a role's
methods, an API key's, whatever your app calls them — could already contain a
string with `*` as a whole part, like `/app.RobotService/*`.

`Grant.Allows` was a membership test. A stored `/app.RobotService/*` matched
only a method named exactly `*`, which cannot exist, so such a string allowed
**nothing**. It is a pattern now and allows that whole service.

That is a widening, and it is the only backwards-incompatible thing here: a
grant of whole method names behaves exactly as it did, because `frame.Covers`
answers on equality first.

**What to check.** One query, against wherever you keep them:

```sql
SELECT * FROM role WHERE EXISTS (
  SELECT 1 FROM json_each(methods) WHERE json_each.value LIKE '%*%'
);
```

Anything it returns was allowing nothing and now allows something. Almost
certainly there is nothing — a string like that is one somebody typed expecting
this feature before it existed.

**What you gain**, and the reason for it: a list of methods is a snapshot of
something that grows. Write out every method of a service today and the next
release adds one the role does not allow — silently, to a role whose name says
it covers that service.

```
/app.RobotService/Get     one method
/app.RobotService/*       one service
/app.*/*                  one package
/app.*/Get                that method wherever it appears
/*.*/*                    everything
```

A whole part or nothing. `*Get*` is not a glob, it is a method name that
happens to contain asterisks — partial matching is where three string
comparisons become a regular expression.

And a pattern is covered by **one** pattern you hold, never by several
together. Holding `/app.A/*` and `/app.B/*` does not let you hand out
`/app.*/*` even where those are the only two services, because the third one
added next release would be covered by a grant made before it existed. See
[`frame.Covers`](../frame/method.go).

## payday's entities are copied inside your proto package

**Applies if** your schema has `import "payday/tenant.proto"` or any other
`payday/*.proto`, which is every app generated before this.

`pd gen` used to copy payday's entities to `proto/payday/`. A protobuf registry
is per process and keys files by their path, so two apps that both did that
registered one path twice and panicked before `main` — which is not
hypothetical, it is what the first app to import another payday app's generated
client did to its own binary. The copies land at `proto/<pkg>/payday/` now, so
the path carries the app's proto package and no two apps collide.
[Two apps in one process](guide/packages.md#3-two-apps-in-one-process) is that
rule in full.

**What to change**, in your hand-written schema and in your overlays:

```diff
-import "payday/tenant.proto";
+import "app/payday/tenant.proto";
```

where `app` is whatever your entities declare as their proto package. Then:

```sh
rm -rf proto/payday       # the old copies; pd gen writes the new ones
go tool pd gen .
```

Generated `*_svc.g.proto` are rewritten for you. If one is left holding the old
import, delete it and generate again — a stale one is an input to the next run.

**And check your proto package is your own.** The package name is the path on
disk, so distinct packages are distinct paths — and two apps that both kept
`pd new`'s `package app;` are distinct in neither, their messages being
`app.Holder` on both sides. Renaming it is one line per proto:

```diff
-package app;
+package acme;
```

`go_package` is separate and does not have to match.

The rule this leaves: **two payday apps can share a process when their proto
packages differ**, and two instances of the *same* app always could — one binary
links that app's files once, so nothing registers a path twice.

## `payday.Holder` is watchable, and has a list

**Applies to** every app, and needs no change from you.

`Holder` declares `watch: {}` and a `list:`, so `HolderService` gains `Watch`
and `List`. An app that does not open a stream pays for a generated method and
nothing else.

It is declared in payday's own schema because an app cannot: an overlay merges
**fields** and not the entity option, so there was no way for a deployment to
make payday's entity streamable.

## A field can say it is never answered with

**Applies if** you store a verifier — a password hash, an API key hash.

`(payday.field).secret` marks a field that is written and never handed back, and
`pd.Secret` is the generated layer that clears it on the way out. Stack it:

```diff
-app.Build(walled, core.Build(), pd.AuditBuild(), pd.GateBuild())
+app.Build(walled, core.Build(), pd.AuditBuild(), pd.SecretBuild(), pd.GateBuild())
```

The layer is only generated for an app that declared one, so a stack naming it
without one will not compile — which is the reminder.

**Ordering**: the extension is in payday's buf module, so an app depending on
`buf.build/payday/payday` needs a version of it that has this. Until then the
compile says `unknown extension payday.field`.

## A field can say a request may not assert it

**Applies if** you have a stamp — when something was verified, attested,
approved — that the deployment establishes rather than a caller.

`(payday.field).stamped` refuses it in the generated `Add`:

```proto
google.protobuf.Timestamp date_verified = 9 [
  (orm.field) = {nullable: true},
  (payday.field) = {stamped: true}
];
```

```
InvalidArgument: date_verified: is established by this deployment and not
asserted by a request
```

The refusal asks the question the request can answer. A field with presence in
the `Add` request (a message, an edge, or a scalar that is `nullable` or has a
`default`) is refused when it is set at all. A scalar the row cannot do without
has no presence there (the ORM declares it `IMPLICIT`), so it is refused when it
is not zero, and a list or a map when it is not empty. The same holds for a
field an overlay adds to one of payday's entities (`proto/ext/payday/`), which
once generated a gate that did not compile (#29).

**`immutable:` is not this, and reaching for it is the mistake this exists to
end.** `orm.field.immutable` takes a field out of the **patch** request and
leaves it in `Add`, which is right for a tenancy stamp — set once, never moved —
and exactly backwards for this: nobody asserts a verification when the row is
created, and something writes it on the day the verification happens.

The refusal lands in the **gate**, which is exact rather than convenient: a
stamp is not a permission and not a column nobody may touch, it is a fact a
*request* may not assert. So an app's own work through a server without the gate
— `init`, a seed, the call that does the verifying — writes it without anybody
having listed those callers. `Patch` is untouched, because that is the road the
stamp is written by.

Two apps found this the hard way before the option existed, each closing it in a
layer of its own. A layer works and it is invisible: it is a line in a stack
rather than a property of the message, so the next app declares the same field
and does not know to write the same refusal.

## A field that lies about presence is refused

**Applies if** your schema has a message field with no `nullable`, no `default`
and no marker — a `google.protobuf.Timestamp` you meant to be optional, most
likely.

It generated a NOT NULL column while the API beside it had `Has…`, so a caller
asking whether a value was set was told yes and read a zero. Generation refuses
it now and names both fixes, because they mean different things:

```diff
-google.protobuf.Timestamp date_seen = 8;
+google.protobuf.Timestamp date_seen = 8 [(orm.field) = {nullable: true}];
```

or a `default`, if the value is always there.

`date_created`, `date_updated` and `date_erased` are exempt, and the rule is
their declarations rather than their names — an app whose version field is
called something else is not caught by a rule about spelling.

## `pdtest.DB` gives a second database to a second call

**Applies if** a test of yours calls it twice, which is a test that stands two
apps up.

It used to name the schema after the test, so two calls got one schema and the
second's `DROP SCHEMA` removed the first app's tables. It passed on SQLite and
failed only under `PDTEST_POSTGRES`. Nothing to change: no existing test could
have wanted the old behaviour.

## `order:`, `by:` and `with:` name a field as `{name, number}`

**Applies if** your schema declares a `list:`, which is every entity with a page
or a watch.

They took a bare string. A proto field is renamed without the wire changing, so
the **number** is the identity and the name is a label -- and a declaration
carrying only the label follows a rename onto whatever took the old name. That
is not the loud failure of "there is no such field" but the quiet one of "that
is a different field now", and a paging order that moved to another column is
exactly what this generator exists to refuse.

It is also how the rest of a schema already points at a field: `orm`'s indexes
carry `{name, number}`, and two conventions for one thing is one somebody has to
remember which is which for.

```diff
     list: {
-      order: [{field: "date_created"}, {field: "id"}]
-      by: ["ref", "tenant"]
-      with: ["tenant"]
+      order: [{field: {name: "date_created", number: 15}}, {field: {name: "id", number: 1}}]
+      by: [{name: "ref"}, {name: "tenant", number: 2}]
+      with: [{name: "tenant", number: 2}]
     }
```

Either half alone still works, so a small schema is not made verbose and an
existing one is not rewritten to be read. **Both together is what is checked**:
generation resolves by the number and refuses when the name no longer matches.

`{name: "ref"}` has no number, since it is not a field.

`tenanted: {field: ...}` is unchanged and still takes plain strings -- it names
a column the wall reads, not a filter.
