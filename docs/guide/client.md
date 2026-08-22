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
| `gen/` | messages, service descriptors, `domains.ts`, and one `entities.ts` declaration per entity |
| `src/client.ts` | `createClient` per service — for a script, a one-off call, or [several writes as one transaction](batch.md) |
| `src/store.ts` | the store and the queries a page reads through — §2 |
| `src/main.tsx` | the transport, the credential, and the `Provider` around the page — §2 |
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
export async function open(transport: Transport, credential: string): Promise<App> {
	const at = { name: 'widget', identity: await identityOf(credential) }

	const store = Store.open(entities, { ...at, disk: await openDisk(entities, at) })
	await store.hydrate()

	return { store, queries: new Queries(store, transport, entities) }
}
```

Two things in that are worth reading twice.

**The identity is a digest of the credential, not a name for the person.** What
a caller may see is the actor *and* the scope together, so the same person
holding a narrowed credential is a different store. Keyed on the person, the
narrowed session would draw the wide session's rows: nothing leaks either way,
and the screen is wrong.

**The disk is optional.** Delete those two lines and everything still works —
the store is a store either way and lives as long as the tab does. What the
mirror buys is that a reload draws the page it had rather than a spinner for it.

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
confidently wrong at a page boundary. [CLIENT.md §3](../CLIENT.md#3-the-client-is-a-replica)
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
[CLIENT.md §3](../CLIENT.md#3-the-client-is-a-replica).

`keep` is a week by default, counted from when this side last wrote the row, and
`Infinity` turns expiry off.

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
and no backend to start. See [CLIENT.md §2](../CLIENT.md#2-the-whole-app-in-a-page)
for what it needs from the page, and run `pd doctor` — the four ways it goes
wrong all fail without naming their cause.

---

## Where to go next

| | |
| --- | --- |
| [schema.md](schema.md) | declaring the entity all of this is generated from |
| [permissions.md](permissions.md) | what the server does with these calls |
| [errors.md](errors.md) | putting a refusal under the right form field |
| [CLIENT.md](../CLIENT.md) | why the store is in memory, and why Connect over drpc |
