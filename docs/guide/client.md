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

## 1. Three files, and only one is interesting

`pd gen --ts` writes `gen/`, and `pd new` writes the rest once:

| | |
| --- | --- |
| `gen/` | messages, service descriptors, and one `entities.ts` declaration per entity |
| `src/client.ts` | `createClient` per service — for a script or a one-off call |
| `src/store.ts` | the store and the queries a page reads through |
| `src/page.tsx` | yours |

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

Then, in React:

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

### The watch comes with it

A query carrying filters opens the sibling `Watch` behind it, for as long as
anybody is asking. So a row somebody else changed arrives without anything here
polling, and without you writing a line.

```tsx
useQuery(ThingService.method.list, { filters: [] }, { watch: false })
```

Off is for when you mean it. On is the default because the alternative is a
screen that is quietly wrong and a timer somebody has to tune.

A watch says a row is gone by **not sending it**. There are no tombstones: a row
that left the filter and a row that was erased look the same to a client
watching a filter, because they are the same.

### Reading a row somebody else fetched

```tsx
const tenant = useRow<Tenant>(Tenant.typeName, thing.tenant?.id)
```

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
- **An `Erase` answers with nothing**, so what says which row is gone is read
  from the *request*, and the row leaves every list drawing it before the round
  trip is over.

### Why the lists are re-read rather than judged here

The alternative is to evaluate the server's filter and order against a
**partial copy**, and that is confidently wrong at a page boundary: a new row
that belongs on page 3 of a list you hold two pages of either appears where it
should not or vanishes where it should be.

For a run of writes that ends in one read you own:

```ts
await add.call(a, { revalidate: false })
await add.call(b, { revalidate: false })
await add.call(c)
```

---

## 5. Persistence

```ts
disk: await openDisk(entities, at, { keep: 7 * 24 * 60 * 60 * 1000 })
```

**Memory is the truth and the disk is a mirror.** It cannot be the other way
round: a reactive read has to answer *now*, and IndexedDB cannot — so the moment
there is a memory copy, that copy is the store and the database is persistence.

Rows alone are not enough. **A list is an order and a membership**, and neither
is on any row, so a refresh that restored only rows would draw a spinner for the
list it had a second ago. The query layer puts its own answers in the same
mirror under the same identity, and restoring is **synchronous** — a reopened
page draws its list on the first frame.

`keep` is a week by default and `Infinity` turns expiry off. It bounds two
things: how large a mirror gets, and **how old an answer a reload may draw**. A
restored answer is drawn as though true for one round trip, so there has to be a
limit on how stale that is allowed to be.

The clock is **when this side last wrote**, not the server's `dateUpdated`.
Measured the other way you would evict rows that have not changed since 2020 —
which is to say the rows that never change.

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
with the protobuf descriptors. The generator used to emit `get`/`_hydrate`/
`_dehydrate`/`_compare` per entity; that is one file now.

---

## 7. The other transports

The same page runs against three things and cannot tell which:

| | |
| --- | --- |
| a server | Connect / gRPC-Web over HTTP — `web.New` serves it |
| the sandbox | the whole app compiled to wasm, in the page, over `grpc-dgram` |
| a test | a fake `Transport`, which is what payday's own tests use |

The transport is the only line that differs. Nothing above `store.ts` knows
which one it got, and that is what makes the sandbox worth having rather than a
demo that drifts.

---

## Where to go next

| | |
| --- | --- |
| [schema.md](schema.md) | declaring the entity all of this is generated from |
| [permissions.md](permissions.md) | what the server does with these calls |
| [errors.md](errors.md) | putting a refusal under the right form field |
| [CLIENT.md](../CLIENT.md) | why the store is in memory, and why Connect over drpc |
