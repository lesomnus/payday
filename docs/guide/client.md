# The page

payday's client half is one idea: **a read that goes through the framework is a
read the framework knows about.** So when a row changes, every place drawing
that row is told — and nothing anywhere declared that relationship.

```tsx
const { data } = useQuery(RobotService.method.list, { filters: [] })
```

That is the dependency. Not a key, not an invalidation rule, not a tag — calling
it *is* the declaration, because the answer says which rows it carried.

Everything below follows from that. This document is what you write and what you
get; for what the server does with the calls, read
[Permissions and the wall](permissions.md).

---

## 1. What `pd new` writes

`pd gen --ts` writes `gen/`. `pd new` writes the rest once — a Vite project
whose `src/` holds four TypeScript files, and only the last of them is yours:

| | |
| --- | --- |
| `gen/` | messages, service descriptors, `domains.ts`, one `entities.ts` declaration per entity, and `events.ts`, the registry of what the app's events hold |
| `src/client.ts` | `createClient` per service — for a script, a one-off call, or [several writes as one transaction](batch.md) |
| `src/store.ts` | the store and the queries a page reads through — §2 |
| `src/main.tsx` | the transport, the credential, and the `Provider` around the page — §2 |
| `src/session.tsx` | the same, signed in by a session cookie instead of holding a credential — §2 |
| `src/page.tsx` | yours |

`index.html`, `style.css`, `vite.config.ts` and `tsconfig.json` come with them,
and are a Vite project like any other.

`client.ts` is short and stays short, because nothing is generated per service:
protobuf-es emits the descriptor beside the messages and Connect's
`createClient` takes a descriptor. Adding an entity is one line there.

**A page does not read through `client.ts`.** It reads through the store, which
is what makes a row it drew redraw when the row changes.

---

## 2. Opening the store

```ts
export async function open(transport: Transport, identity: string): Promise<App> {
	const at = { name: 'widget', identity }

	const store = Store.open(entities, { ...at, disk: await openDisk(entities, at) })
	void deleteOtherDisks(at)
	await store.hydrate()

	return { store, queries: new Queries(store, transport, entities) }
}
```

Two things in that are worth reading twice.

**The identity names what the caller may see, not the person.** What a caller
may see is the actor *and* the scope together, so the same person holding a
narrowed credential is a different store. Keyed on the person, the narrowed
session would draw the wide session's rows: nothing leaks either way, and the
screen is wrong.

It is passed in already made, because the two pages `pd new` writes come by it
differently. `main.tsx` holds its credential and digests it with `identityOf`.
`session.tsx` is signed in by a session cookie it cannot read — that is what
HttpOnly is for — so it asks the server instead: `GET /session` answers
`{ identity, id, tenantId }`, and `identity` is a digest of what the session
lets its caller see — who, in which tenant, narrowed how — made where the
session can be read. So a person's next sign-in is the same store as their
last, and a narrower one is another. The other two are who the session names,
which is what the page's first question about the person is asked with. See
[Putting a login in front of an app](signing-in.md#a-page-signed-in-by-cookie).

**Every call says which store it is for.** The query layer sends the identity
on every call it makes, in `payday-identity` (`IDENTITY_HEADER` in
`@lesomnus/payday/query`), and `auth/authsession` serves a call only as a
session that identity names; under any other it is `Unauthenticated`. A cookie
is every tab's, so this is what keeps a tab left holding one person's store
from reading the next person's rows into it, or sending what it kept as them.
`web.Cors` allows the header for a page on another origin.

**The disk is optional.** Delete it and everything still works — the store is
a store either way and lives as long as the tab does. What the mirror buys is
that a reload draws the page it had rather than a spinner for it. Once this
caller's mirror is open, every other caller's is deleted, and not waited on
([§5](#5-persistence)).

`open` is `pd new`'s `src/store.ts`, and `pd new` writes `src/` once: a page
made before the `deleteOtherDisks` line was in it has an `open` without it, and
the line is the app's to add.

`src/main.tsx` is what calls it. Both of the things above have to be settled
before React starts — which transport, and whose store — because rendering over
a store that has not hydrated is a spinner for something the tab already had:

```tsx
<Provider app={await open(transport, credential)}>
	<Page />
</Provider>
```

`@lesomnus/payday/react` is thirty lines over `Queries`. Vue or Svelte is the
same file, the same length — the reactive layer needs a **synchronous read**
and a subscription, and that is all of it.

---

## 3. Reading

```tsx
const { state, data, error } = useQuery(ThingService.method.list, { filters: [] })

if (state === 'error') return <p>{String(error)}</p>
if (data === undefined) return <p>...</p>
```

`state` is `'pending' | 'ok' | 'error'`, and `data` is the answer with **every
row in it read from the store** rather than from what arrived. That last part is
what makes one row shown in six places six views of one thing.

Two components asking the same question share one call, one entry and one
stream. The key is the method and the request, so a request object rebuilt on
every render is still one query.

### The watch comes with a filter

A query that names **at least one filter** opens the sibling `Watch` behind it,
for as long as anybody is asking. So a row somebody else changed arrives without
anything here polling, and without you writing a line:

```tsx
useQuery(ThingService.method.list, {
	filters: [{ ref: { key: { case: 'id', value: id } } }],
})
```

**A filterless list is not watched.** A watch says which rows it is about, and
one that says nothing is the whole table for as long as the stream is open, so
the server refuses it:

```
filters: a watch says which rows it is about; one that says nothing is the whole table, for as long as it is open
```

Opening it anyway would spend a stream on a refusal nobody ever reads — a failed
stream looks like one that ended, and what follows an ended stream is one reopen
— so the client does not open it. `{ filters: [] }` is the first page plus a
re-read after every write **this** client makes, which is what a page that shows
only what it is itself changing needs. A page that wants *other people's* writes
live says which rows it is about.

| | |
| --- | --- |
| `{ watch: false }` | no stream, for a filtered query that does not want one |
| `{ watch: true }` | insists on one, for a service whose `Watch` takes the whole table |

A watch says a row is gone by naming it with nothing attached: the item carries
the identifier and no value. There are no tombstones, and a row that left the
filter and a row that was erased are the same thing to a client watching a
filter. What the stream promises is in the schema guide, under
[`watch:`](schema.md#6-watch--the-page-kept-current).

### Reading a row somebody else fetched

```tsx
import { Tenant } from '../gen/entities.js'
import type { Tenant as TenantMsg } from '../gen/app/payday/tenant_pb.js'

const tenant = useRow<TenantMsg>(Tenant.typeName, thing.tenant?.id)
```

Two imports for one name, because they are two different things: the entity
declaration in `gen/entities.ts` is a *value* carrying `typeName`, and the
message is a *type* in the `_pb` file. Importing either one alone does not
compile.

A list that did not ask for the tenant answers with its identifier and nothing
else. This reads the row a `Get` elsewhere on the page already put in the store
— one copy, two components, nothing joining them up.

`undefined` means nothing has it, which is the cue to go and ask.

---

## 4. Writing

```tsx
const add = useCall(ThingService.method.add)

add.call({ tenant: { key: { case: 'id', value: tenantId } }, alias })
	.then(() => setAlias(''))
	.catch(() => {})
```

What happens next is not written down anywhere:

- **The answer is a row**, so it goes into the store and every place showing
  that row is right at once — with no round trip.
- **The lists over that entity are read again**, because a create can change
  what belongs in one and only the server knows which. Only the lists that are
  being drawn; an idle query re-reads when something draws it again.
- **An `Erase` answers `{ erased: bool }`** — whether this call was the one that
  erased — and never says *what* it erased, so the subject is read from the
  *request*. A row the request named by `id`, on an entity with a version field,
  leaves every list drawing it before the round trip is over. A row named by an
  alias leaves when the re-read answers, one round trip later: an alias says
  which row to the server and not to this side.

That version field is the same one a `watch:` is refused without, so an entity
with a list a page draws usually has it. Without one the store cannot tell a row
it deleted from a row it was never given — a neighbour that arrived as a bare
reference is exactly that — so it keeps drawing the copy the answer carried
until the re-read lands.

### A run of writes

The lists are re-read rather than judged here because judging means evaluating
the server's filter and the server's order over a partial copy, which goes
confidently wrong at a page boundary. [client.md §3](../client.md#3-the-client-is-a-replica)
works that through.

For a run of writes that ends in one read you own:

```ts
await add.call(a, { revalidate: false })
await add.call(b, { revalidate: false })
await add.call(c)
```

The last call is then the only read, and it is yours: with `revalidate: false`
nothing else will make one. The answers still land in the store — that is not
the part being turned off.

### Without a connection

`call` fails when there is nobody to answer. A write that has to survive that,
such as a count taken in a basement, goes through `send`:

```ts
const id = pdid.newId(RobotDomain).bytes
await queries.send(RobotService.method.add, { id, tenant, alias })
```

It resolves once the write is **kept**, in the store's mirror, before anything
is sent. After that it is tried, and what happens depends on the answer:

- **Answered:** it lands the way a call does.
- **Nobody answered** (no network, or no server behind the proxy): it waits,
  and every write made after it waits behind it, so they arrive in the order
  they were made.
- **Refused:** it stays, with the error a `call` would have thrown, details
  included, so `pderr` reads it as it reads any other refusal.

What is waiting is sent again whenever anything answers, because a read that
came back means the connection came back. For other moments, such as the
browser's `online` event, call `queries.flush()`.

```tsx
function Waiting() {
	const { queries } = useApp()
	const writes = useWrites()

	return writes.map((w) => (
		<li key={w.id}>
			{w.name} — {w.state === 'refused' ? w.error?.rawMessage : 'waiting'}
			{w.state === 'refused' && <button onClick={() => queries.dismiss(w.id)}>dismiss</button>}
		</li>
	))
}
```

A waiting write is drawn **as a write**, not as the row it will make. Guessing
that row is the optimistic update [§6](#6-what-is-not-here) says this does not
have, for the same reason.

Six things are worth knowing before relying on it:

- **A write may arrive twice.** An answer can be lost on the way back, and the
  write is then sent again, so queue writes that mean the same thing twice. An
  `Add` with an identifier minted here is the case payday knows: the second one
  is refused `AlreadyExists`, and the queue reads the row by that identifier
  instead. An `AddMany` whose every item carries one is that many of them, read
  back a row at a time; one with an item that does not is a refusal that
  stands. An `Erase` is the same write by nature. A `Patch` that names the
  version it read is refused the second time, which is correct about the row
  and misleading about the write.
- **Whether the server takes that identifier is your app's call.** `pd.Minter()`
  keeps an identifier of the right domain. A minter of your own may refuse it,
  and the queued `Add` is then a refused write. Or it may replace it, and then a
  lost answer's second `Add` makes a second row. Keeping what the page mints in
  step with what your minter accepts is your app's job too
  ([the generation contract §5](../schema.md#5-identifiers)).
- **It is sent as the caller its store is for.** The transport carries whoever
  it carries when the write goes, and with a cookie that is whoever signed in
  last, in any tab. The write says whose store it is from, as every call does,
  and `auth/authsession` refuses it under any other session. Refused
  `Unauthenticated` — somebody else's session, or one that idled out while the
  page was offline — it **waits**, and goes once its caller signs in again: the
  same person with the same grant is the same store. Signing out is what lets
  the writes go: `store.forget()`, in **every** page holding the store, which
  `pd new`'s `session.tsx` says over a `BroadcastChannel`, and then
  `store.close()` ([§5](#5-persistence)). A forgotten store keeps nothing more
  and sends nothing. Read `queries.writes()` before signing out if losing them
  matters.
- **A secret is not kept.** A write that sets a field declared
  `(payday.field).secret` is refused by `send` before anything is stored, because
  waiting would keep the secret on disk in the clear -- and so is an `AddMany`
  one of whose items does, and any `Apply` to an entity that declares one: a
  patch says what it writes in terms only the server reads. Send it with
  `call`. A secret in a batch or in an RPC of your own, such as a password, is
  not visible here, so keep those out of `send` yourself.
- **One tab sends.** Two tabs on one store both hold its queue. The one sending
  takes a Web Lock and re-reads the queue from the mirror first, so a write is
  sent once.
- **A service no entity answers** has to be named:
  `new Queries(store, transport, entities, { services: [ReportService] })`.
  Otherwise a write to it kept by an earlier page waits until this page sends
  through that service itself.

---

## 5. Persistence

```ts
disk: await openDisk(entities, at, { keep: 7 * 24 * 60 * 60 * 1000 })
```

**Memory is the truth and the disk is a mirror.** The query layer mirrors its
own answers there too, under the same identity and beside the rows, so a
reopened page draws the list it had on the first frame instead of a spinner for
it. Why it is that way round, why rows alone would not be enough, and why the
expiry clock runs from this side's write are
[client.md §3](../client.md#3-the-client-is-a-replica).

`keep` is a week by default, counted from when this side last wrote the row, and
`Infinity` turns expiry off.

**Everybody else's mirror goes.** A mirror is swept only when its own caller
hydrates it, so one whose caller has left the browser to somebody else would
keep their rows, answers and unsent writes for as long as the browser keeps the
database. `deleteOtherDisks(at)` deletes every mirror of the app but `at`'s: a
page asks for it once the caller's own is open, and does not wait on it --

```ts
const store = Store.open(entities, { ...at, disk: await openDisk(entities, at) })
void deleteOtherDisks(at)
await store.hydrate()
```

**Signing out forgets, then closes.** `store.forget()` drops the caller's rows,
answers and waiting writes at once, and the mirror's copy of them once the
writes queued for it ahead of the forget have gone. `store.close()` closes the
mirror only after that, so neither has to be awaited for the mirror to be left
empty -- and a row written after `close()` stays in memory. Each answers a
promise: `forget()` for when the mirror is cleared, and `close()` for when it
is closed. `close()` rejects when the forget before it did not reach the disk,
and closes the mirror all the same; so a page that would tell the person awaits
it, as `pd new`'s `session.tsx` does:

```ts
void store.forget()
await store.close() // rejects if the mirror still holds what was forgotten
```

A mirror another page deleted counts as forgotten. One another page moved to a
later version does not, since it is still on the disk. A row that did not
reach the mirror is `flushed()`'s to say, and not `close()`'s.

A page lets go of its mirror when another page deletes it or moves it to
a later version, so neither waits on a tab somebody left open. A tab from before
that holds on: the sweep leaves its mirror for next time, and `openDisk` fails
saying another page holds it, rather than waiting for as long as that tab is
open.

The schema stamp is a digest of field numbers, names and kinds, so nobody has to
remember to bump a version: change the schema and the mirror is discarded.

---

## 6. What is not here

**No client-side query language.** The filters are the ones the schema declared
in `list.by`, checked at generation. A filter the server cannot answer is not
something to find out in a browser.

**No optimistic update API.** The answer to a write *is* the row, so the store
has the truth about as fast as an optimistic guess would have had a guess —
without a rollback path to get wrong.

**No cache keys, no invalidation rules, no tags.** A read declares itself by
being made.

**No entity-specific generated code.** `gen/entities.ts` is declarations only —
the normalizing, reconciling and watch-applying is one runtime that reads those
with the protobuf descriptors.

---

## 7. The other transports

The same page runs against three things and cannot tell which:

| | |
| --- | --- |
| a server | Connect / gRPC-Web over HTTP — `web.New` serves it |
| the sandbox | the whole app compiled to wasm, in the page, over `grpc-dgram` |
| a test | a fake `Transport` — which is how payday's own React binding is tested |

The transport is the only line that differs. Nothing above `store.ts` knows
which one it got, and that is what makes the sandbox worth having rather than a
demo that drifts.

The sandbox is not written by `pd new` — it makes `grpc-dgram` a direct
requirement of your app, which a backend-only one should not acquire by
scaffolding. Ask for it when you want it:

```sh
$ go tool pd sandbox init .
```

Then a reload is a fresh server: new instance, new database, nothing left over,
and no backend to start. See [client.md §2](../client.md#2-the-whole-app-in-a-page)
for what it needs from the page, and run `pd doctor` — the four ways it goes
wrong all fail without naming their cause.

### Do these two things while you are there

The module is tens of megabytes — payday's own is 68 — and both of these are
about that one fact. `pd sandbox init` writes the pass-through for them and the
page decides:

```ts
const box = await start({
	worker: new URL('./sandbox-worker.ts', import.meta.url),
	onProgress: (v) => setGot(v),   // { loaded, total, from, keeping }
})
```

**Keep it, which is on by default.** The browser's own HTTP cache will not hold
an entry this size — a cache backend drops any single one over a fraction of the
whole cache — so without this every reload downloads the module again, and
nothing says so: the dev server answers `304` to a conditional request all day
and the browser never sends one. payday keeps it in the Cache API instead and
revalidates by hand, so a rebuild is still picked up. `cache: false` turns it
off, and there is no good reason to.

**Draw the progress.** A page that says only "starting" for eight seconds is
indistinguishable from a page that has hung, and this is the whole of a first
visit. Three of the four fields are worth putting on the screen:

- `loaded` / `total`, with `total: 0` meaning the length is not knowable, which
  is a state a bar needs rather than a number to substitute for;
- `from`, because reading 68 MB off a disk fills a bar exactly like downloading
  it does, and somebody watching that on a reload concludes the caching is
  broken;
- `keeping: false`, which means the next reload does all of it again. It has one
  ordinary cause and the page is the only thing that can say it: the Cache API
  needs a secure context, so a container's dev server reached from the host at
  `http://172.17.0.2:5173` — by address rather than through a forwarded port —
  has nowhere to keep anything.

`internal/apptest/ts/src/devtools-page.tsx` in this repository draws all of it,
in about forty lines.

### The registry a Connect transport is handed

A transport that speaks Connect's JSON -- `createConnectTransport`, which is
what `pd new` writes in both `main.tsx` and `session.tsx` -- is handed the
registry of the app's event messages:

```ts
import { events } from '../gen/events.js'

const transport = createConnectTransport({
	baseUrl: ADDR,
	jsonOptions: { registry: events },
})
```

An event keeps its value as an `Any`, and JSON names the message an `Any` holds
by its type, `{"@type": "type.googleapis.com/app.RobotMoved", …}`. protobuf-es
reads that only with the type registered, so without the registry a List of
events fails -- the whole page of it, on the first row that holds a value --
and until the first such row is written, nothing says the line is missing.
`pd gen --ts` writes `gen/events.ts` for every app, empty for one with no past,
so that the line is there from the day a page is made.

A page made before this has transports without it, and the line is the app's
to add; in an app that keeps a past, `pd doctor` names each file of `ts/src`
whose transport speaks JSON and is handed none. The same registry is what
`anyUnpack(ev.value, events)` reads a value with; see
[Keeping a past](past.md#5-reading-an-event-back). And it is the panel's
`registry`, which writes one out; see [§8](#8-the-panel).

## 8. The panel

A window on the rows the server answers with and on what the store believes,
in the page, without a network tab — which a sandbox does not have, since its
calls travel a message port to a Worker.

Nothing writes it for you. `pd new` does not and `pd sandbox init` does not:
it is a development surface and where it belongs is a decision about your
build, not about your schema. It is an import and an element.

```tsx
import { Provider } from '@lesomnus/payday/react'
import { Devtools } from '@lesomnus/payday/react/devtools'

import { entities } from '../gen/entities.js'

<Provider app={app}>
	<Devtools entities={entities} />
</Provider>
```

**Under the `Provider`**, because it reads the same `App` and the same store
your page does — that is the point of it. **`entities` is the generated array**,
the one `Store.open` was already given; the panel takes it rather than reading
it off the store because what a picker wants is the list as the app declared
it.

[client.md §4](../client.md#4-a-window-on-both-halves) is why it is payday's
rather than each app's, and why it asks the server rather than the database
it is sitting on top of.

`internal/apptest/ts/src/devtools-page.tsx` in this repository is a whole page
around it — the transport, the editor, and the progress a first load needs —
and it is what payday's own sandbox is looked at through. The four lines above
are the part that is the panel.

**Hand it `events` as well**, the registry the page's Connect transport is
handed ([above](#the-registry-a-connect-transport-is-handed)):

```tsx
import { events } from '../gen/events.js'

<Devtools entities={entities} registry={events} />
```

The panel writes a row out as protobuf JSON -- the `get` tab's document, and a
cell holding a message -- and an event keeps its value as an `Any`, which
protobuf JSON can write only with the type it holds registered. Without it, the
`get` tab says so rather than showing an event that holds a value, and a table
shows the type the value names and how big it is. Hand it over even where the
transport needs none: a sandbox's speaks binary, which reads an `Any` as bytes,
so the panel is the first thing that writes one out. An app with no past has
`events`, empty, and loses nothing by passing it. `registry` came with
`@lesomnus/payday` 0.1.4.

### What is in it

| | |
| --- | --- |
| `list` | rows as the wire answered, paged by being scrolled, columns you can turn off, and a search over what is on the screen |
| `get` | one row as protobuf JSON — read-only, or an editor; see below |
| `store` | what this browser holds for that entity, which the server was not asked about |

Two switches at the bottom. `utc` writes timestamps in UTC rather than in the
zone you are in — the column holds UTC either way, and the default is yours
because the request log beside this is a wall clock. `past the wall` appears
only if you hand it somewhere to go:

```tsx
<Devtools entities={entities} ungated={ungatedTransport} />
```

That is a second transport reaching the **ungated** stack, which a sandbox has
and a deployment does not — there, the wall protects the page from itself. It
answers the one question the walled path cannot: a row that is not there and a
row you may not see are the same answer through the wall and different through
this. An app that was never given one cannot offer the switch, which is the
whole of the guard.

### An editor over the document

`<Devtools>` shows a row the `Get` tab answered with as protobuf JSON. Hand it
Monaco and that becomes an editor, with completion over the fields the message
actually has, a hover saying what each is, and a red line under a typo — all of
it from the descriptor, which is already a JSON Schema in every respect but the
spelling:

```ts
import * as monaco from 'monaco-editor'

<Devtools entities={entities} monaco={{ editor: monaco.editor, Uri: monaco.Uri, json: monaco.json.jsonDefaults }} />
```

payday does not depend on it and cannot: fifteen megabytes into every app that
mounts the panel, and web-worker URLs that only your bundler can write. Set
`MonacoEnvironment` yourself — without the JSON worker there is no language
service, and the editor still edits, which is the confusing half of getting it
wrong. `devtools-page.tsx` shows where it goes.

Without it the panel is whole: the same document, coloured, read-only.

**Fetch it rather than import it.** Monaco is four megabytes and the first
thing anybody does in the panel is read a list, which needs none of it. At the
top of the module it is in the entry chunk, so nothing renders until all of it
has arrived; asked for after the page is up, the table is on the screen and
interactive while the editor is still coming. The panel takes `monaco` whenever
it turns up — a `Get` opened before it lands is the read-only document above,
and becomes editable when it does.

```ts
const [edit, setEdit] = useState<MonacoLike>()
useEffect(() => {
	void editor().then(setEdit)   // the dynamic import, and MonacoEnvironment inside it
}, [])

<Devtools entities={entities} {...(edit === undefined ? {} : { monaco: edit })} />
```

Measured on this repository's own page: the entry chunk is 484 kB where it was
4,354 kB, and 146 kB gzipped where it was 1,133. `devtools-page.tsx` is the
whole of it, including where `MonacoEnvironment` has to go — beside the import
that will ask for a worker, which is the only ordering that guarantees it is set
first.

Saving sends a `Patch` of the fields that **changed** and that the document
actually carries. Deleting a line is not how a value is cleared — the `_null`
companions on the request are, and they exist because absent and empty are
different things that a document cannot tell apart.

**diff** halves the region: the document is typed into on the left, and the
right is an inline diff of it against what was read, read-only. That way round
rather than Monaco's own side-by-side — where the editable half is the right
one — because typing belongs where reading starts and a diff is something to
glance at. It is also why the right is a second editor: one diff editor cannot
be half editable and half not.

The diff waits 300ms for typing to stop. Recomputing it per keystroke costs
nothing anybody sees and makes the right-hand side flicker through every
half-typed word.

`jsonSchemaOf` is exported from `@lesomnus/payday/react/jsonschema` if you want
the same thing for a form of your own.

---

## Where to go next

| | |
| --- | --- |
| [schema.md](schema.md) | declaring the entity all of this is generated from |
| [permissions.md](permissions.md) | what the server does with these calls |
| [errors.md](errors.md) | putting a refusal under the right form field |
| [client.md](../client.md) | why the store is in memory, and why Connect over drpc |
