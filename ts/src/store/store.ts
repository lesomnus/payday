/**
 * The local store: a replica of what this caller may see, kept up to date by a
 * `Watch`.
 *
 * It is layer 3, and the last layer payday takes a position on. Above it is a
 * UI framework, which is taste; this is not taste, and that is the whole reason
 * to draw the line here.
 *
 * # Why a store rather than a cache
 *
 * A cache of responses answers the question that was asked. A store answers
 * questions nobody asked yet -- because it holds rows rather than replies, one
 * copy of each, reachable by identifier.
 *
 * Everything that makes that hard is mechanical and comes out of the schema:
 * normalizing a nested response, ordering two answers about one row, and
 * applying a change that arrived while nobody was looking. Every one of those
 * is a thing a person writing it by hand gets right four times and wrong the
 * fifth, which is exactly what is worth generating a caller for.
 *
 * # It is in memory, and that is not a shortcut
 *
 * Reading has to be **synchronous**. A screen that re-renders when a row
 * changes asks the store for that row while it renders, and every reactive
 * binding worth having -- React's `useSyncExternalStore` most plainly -- takes
 * a function that answers now rather than a promise. IndexedDB cannot; so a
 * store on IndexedDB needs a copy in memory anyway, and once that copy exists
 * it is the store and the database is persistence.
 *
 * Which is a separate concern, and it is behind a seam: hand [Opts.disk] a
 * mirror and the rows are written out behind the reads, so a reload draws what
 * it had and finds out what changed behind that. See `disk.ts` for why memory
 * stays the truth, and `idb.ts` for the one implementation there is.
 *
 * # It is tied to what this caller can see
 *
 * A store is opened **for a credential**, and one opened for another is a
 * different store. Nothing leaks if it is not -- the server never sent anything
 * the caller may not see -- but the screen is wrong: rows a narrowed credential
 * can no longer read are still there, and a page drawn from them shows things
 * that are not there any more.
 *
 * Note *credential* and not *user*. What a caller can see is the actor **and
 * the scope**: the same person holding a credential narrowed to one customer
 * sees that customer, and holding a whole one sees everything. Keyed on the
 * person, those two share a store and the narrowed session shows the wide
 * session's rows. See [identityOf].
 *
 * @module
 */

import { create, fromBinary, toBinary, type Message } from '@bufbuild/protobuf'

import { bytes, key, type EntityDesc, type Row } from './desc.js'
import type { Disk } from './disk.js'
import { flatten, newer } from './flat.js'

// In a page, in a worker and in Node, and the only thing outside the language
// this module needs. Declared rather than putting `DOM` in `lib`; see
// `digest.ts`.
declare const queueMicrotask: (f: () => void) => void

/** Opts is what a store is opened with. */
export interface Opts {
	/**
	 * What this caller can see, as a string.
	 *
	 * Not optional, and not the user's name: see the note on identity above,
	 * and [identityOf] for what to put here.
	 */
	readonly identity: string

	/** The name of the app, which is the rest of the store's name. */
	readonly name: string

	/**
	 * Where to mirror the rows, if anywhere.
	 *
	 * Absent is a store that lives as long as the page does, which is the right
	 * answer for plenty of apps. See `@lesomnus/payday/store/idb`, and note
	 * that a store opened with one still has to be told to [Store.hydrate].
	 */
	readonly disk?: Disk
}

/**
 * Key is what a subscription is on: one row, by entity and identifier.
 *
 * It is a string rather than a pair so that whatever else keeps state about
 * this store -- a query layer with result sets, say -- can key on the same bus
 * without payday having to know what it keys on. See [Store.touch].
 */
export type Key = string

/** Store is every entity of one app, for one caller. */
export class Store {
	readonly name: string

	private readonly by: Map<string, EntityDesc>
	private readonly rows = new Map<string, Map<string, Row>>()
	private readonly listeners = new Map<Key, Set<() => void>>()

	/** Keys written during the current batch, notified when it ends. */
	private pending: Set<Key> | undefined

	private readonly disk: Disk | undefined

	/** What a layer above kept beside the rows; see [Store.blob]. */
	private readonly blobs = new Map<string, Uint8Array>()

	/** What the mirror has not been told about yet; see [Store.mirror]. */
	private queued = new Set<Key>()
	private queuedBlobs = new Set<string>()
	private scheduled = false
	private flushing: Promise<void> = Promise.resolve()
	private failed: unknown

	private constructor(name: string, by: Map<string, EntityDesc>, disk: Disk | undefined) {
		this.name = name
		this.by = by
		this.disk = disk
		for (const typeName of by.keys()) this.rows.set(typeName, new Map())
	}

	/** open answers with the store for `opts.identity`. */
	static open(entities: readonly EntityDesc[], opts: Opts): Store {
		const by = new Map<string, EntityDesc>()
		for (const v of entities) by.set(v.typeName, v)

		return new Store(`${opts.name}:${opts.identity}`, by, opts.disk)
	}

	/**
	 * hydrate fills this store from its mirror, once.
	 *
	 * Await it before the first render and the page draws what it had; call it
	 * later and it is still safe, because a row from disk only lands where it
	 * is not older than what is already held -- the same rule two answers from
	 * the server are ordered by. So a mirror that is behind cannot undo a read
	 * that already came back.
	 *
	 * What it does **not** do is make anything current. The rows are as old as
	 * the tab that wrote them, and what makes them true again is the reads a
	 * page makes anyway -- drawn over what was already there instead of over a
	 * spinner. A `Watch` closes the rest.
	 */
	async hydrate(): Promise<void> {
		if (this.disk === undefined) return

		const held = await this.disk.load()
		for (const [k, v] of held.blobs) this.blobs.set(k, v)

		this.batch(() => {
			for (const [k, row] of held.rows) {
				const at = k.indexOf('/')
				if (at < 0) continue

				const typeName = k.slice(0, at)
				const rows = this.rows.get(typeName)

				// An entity this app no longer has. The stamp catches a schema
				// that moved, so this is the narrow case of a mirror written by
				// something that shared the name -- and dropping it is right.
				if (rows === undefined) continue

				const id = k.slice(at + 1)
				const was = rows.get(id)
				if (!newer(this.desc(typeName), row, was)) continue

				rows.set(id, { ...was, ...row })
				this.dirty(k)
			}
		})
	}

	/**
	 * flushed answers when everything written so far has reached the mirror,
	 * and throws if the last attempt to reach it did not.
	 *
	 * Nothing has to await this. Writing out is behind the reads on purpose --
	 * a render must never wait on a disk -- so a mirror that fails costs the
	 * mirror and nothing else, and the store goes on being right. This is for a
	 * test, and for an app that would like to say something when the copy it
	 * thought it had is not there.
	 */
	async flushed(): Promise<void> {
		// A write made this turn is still only scheduled, so waiting on the
		// chain now would be waiting on the one before it. Yield until the
		// scheduled turn has run and its work is on the chain.
		while (this.scheduled) await Promise.resolve()

		await this.flushing
		if (this.failed !== undefined) throw this.failed
	}

	/** close lets go of the mirror. */
	close(): void {
		this.disk?.close()
	}

	/**
	 * blob is what a layer above this one left beside the rows, **now**.
	 *
	 * Rows are not the whole of what a page draws: a list is an order and a
	 * membership, and neither of those is in any row. So the query layer keeps
	 * the answers it gave here, and a reloaded page draws the list it had on
	 * its first frame instead of a spinner. See `disk.ts`.
	 *
	 * It is here rather than beside the query layer for one reason and it is
	 * not convenience: this is what knows the credential, holds the mirror, and
	 * is told to [Store.forget]. Answers kept anywhere else would be answers
	 * that outlive the logout that dropped the rows they name.
	 */
	blob(key: string): Uint8Array | undefined {
		return this.blobs.get(key)
	}

	/** setBlob keeps one, or drops it when given nothing. */
	setBlob(key: string, v: Uint8Array | undefined): void {
		if (v === undefined) this.blobs.delete(key)
		else this.blobs.set(key, v)

		this.mirrorBlob(key)
	}

	/** desc is the declaration for one entity. */
	desc(typeName: string): EntityDesc {
		const v = this.by.get(typeName)
		if (v === undefined) throw new Error(`store: this app has no ${typeName}`)

		return v
	}

	/** rowKey is what a subscription on one row is keyed by. */
	rowKey(typeName: string, id: Uint8Array | string): Key {
		return `${typeName}/${key(id)}`
	}

	/**
	 * subscribe calls `cb` when any of `keys` is written, and answers with the
	 * way to stop.
	 *
	 * The keys are read once. A caller whose set of keys changes -- a list that
	 * grew a row -- subscribes again, which is what a reactive binding does on
	 * every render anyway.
	 */
	subscribe(keys: Iterable<Key>, cb: () => void): () => void {
		const on: Key[] = []
		for (const k of keys) {
			let set = this.listeners.get(k)
			if (set === undefined) {
				set = new Set()
				this.listeners.set(k, set)
			}

			set.add(cb)
			on.push(k)
		}

		return () => {
			for (const k of on) {
				const set = this.listeners.get(k)
				if (set === undefined) continue

				set.delete(cb)
				if (set.size === 0) this.listeners.delete(k)
			}
		}
	}

	/**
	 * touch says a key changed, for state this store does not hold.
	 *
	 * A query layer keeps result sets -- which rows a filter answered with --
	 * and those change for reasons the store cannot see. Rather than a second
	 * notification bus beside this one, it says so here.
	 */
	touch(...keys: Key[]): void {
		if (this.pending !== undefined) {
			for (const k of keys) this.pending.add(k)
			return
		}

		for (const k of keys) this.fire(k)
	}

	/**
	 * row is what is held for one row, or undefined, **now**.
	 *
	 * Synchronous on purpose; see the note on memory above.
	 */
	row(typeName: string, id: Uint8Array | string): Row | undefined {
		return this.rows.get(typeName)?.get(key(id))
	}

	/**
	 * all is every row held for one entity, in no order.
	 *
	 * There is no order because there is no index: what a page shows in an order
	 * is a `List`, and the server ordered it -- by a key it has an index for,
	 * which is the whole of why `list.order` is refused when it does not end in
	 * the key. Sorting a partial copy here would answer a different question
	 * confidently.
	 */
	all(typeName: string): Row[] {
		const rows = this.rows.get(typeName)
		if (rows === undefined) throw new Error(`store: this app has no ${typeName}`)

		return [...rows.values()]
	}

	/**
	 * get answers with a row as the message it came from, or undefined.
	 *
	 * The neighbours are **not** filled back in. What comes back is the row,
	 * with a reference where the neighbour was, and reading a neighbour is
	 * asking for it by name -- which is one lookup against a primary key rather
	 * than a join nobody asked for. A page that wants both asks for both.
	 */
	get<T extends Message>(typeName: string, id: Uint8Array | string): T | undefined {
		const row = this.row(typeName, id)
		if (row === undefined) return undefined

		return this.message<T>(typeName, row)
	}

	/** message is a row read back as the message it came from. */
	message<T extends Message>(typeName: string, row: Row): T {
		const desc = this.desc(typeName)
		const refs = new Map((desc.refs ?? []).map((r) => [`${r.field}Id`, r.field]))

		const init: Record<string, unknown> = {}
		for (const [k, got] of Object.entries(row)) {
			const ref = refs.get(k)
			if (ref !== undefined) {
				if (typeof got === 'string' && got !== '') init[ref] = { id: bytes(got) }
				continue
			}

			const f = desc.schema.fields.find((v) => v.localName === k)
			if (f === undefined) continue

			init[k] = f.fieldKind === 'scalar' && f.scalar === 12 && typeof got === 'string' ? bytes(got) : got
		}

		return create(desc.schema, init) as T
	}

	/** put writes a message and everything it was carrying. */
	put(typeName: string, ...vs: Message[]): void {
		this.batch(() => {
			for (const v of vs) this.write(typeName, v)
		})
	}

	/**
	 * apply takes one message of a `Watch` and writes what it says.
	 *
	 * A watch sends **state**: an item names a row, and either carries what it
	 * is now or carries nothing, which is how a removal is said. So this is two
	 * cases and no third -- there is no tombstone to recognise and no ordering
	 * of deltas to preserve.
	 *
	 * `action` is the RPC that made the change and is not read here. It is for
	 * whoever is watching to show something about, which is a decision no
	 * framework can make: `Erase` and `Deactivate` both arrive as an absent
	 * value, and only the app knows which of them is worth a sentence.
	 */
	apply(typeName: string, items: readonly { id: Uint8Array; value?: Message | undefined }[]): void {
		this.batch(() => {
			for (const item of items) {
				if (item.value === undefined) {
					// Absence is how a removal is said. Nothing else says it,
					// and nothing needs to.
					this.rows.get(typeName)?.delete(key(item.id))
					this.dirty(this.rowKey(typeName, item.id))
					this.mirror(this.rowKey(typeName, item.id))
					continue
				}

				this.write(typeName, item.value)
			}
		})
	}

	/**
	 * forget throws this caller's copy away.
	 *
	 * What it is for is logging out. The rows are not a secret -- the server
	 * only ever sent what that caller could see -- but they are *that caller's*,
	 * and leaving them where the next one opens the same page is the kind of
	 * thing that looks like a leak whether or not it is one.
	 *
	 * Everything subscribed hears about it, because a screen drawn from rows
	 * that are gone is a screen showing what is no longer there.
	 */
	forget(): void {
		this.batch(() => {
			for (const [typeName, rows] of this.rows) {
				for (const id of rows.keys()) this.dirty(`${typeName}/${id}`)
				rows.clear()
			}

			for (const k of this.listeners.keys()) this.dirty(k)
		})

		// The answers go too, and they matter more than the rows do: an answer
		// is a list of identifiers this caller was allowed to see, and one left
		// behind would be drawn on the next page before anything asked.
		this.blobs.clear()

		// The mirror goes as a whole rather than a key at a time -- what is
		// being said here is "this caller is done", and half a mirror left
		// behind by a failed key-by-key delete is the outcome worth ruling out.
		this.queued.clear()
		this.queuedBlobs.clear()

		const disk = this.disk
		if (disk !== undefined) this.chain(() => disk.clear())
	}

	/**
	 * write is one message, taken apart and reconciled, and then its
	 * neighbours.
	 *
	 * Depth-first and unbounded, because the nesting is: a response may carry a
	 * robot's tenant, and one day a tenant's something-else. What bounds it is
	 * the schema -- an entity graph with a cycle in it would need a seen-set,
	 * and there is no such graph, since an edge is a foreign key and a cycle of
	 * foreign keys cannot be written in the first place.
	 */
	private write(typeName: string, v: Message): void {
		const desc = this.desc(typeName)
		const { row, nested } = flatten(desc, v)
		if (row.id === '') return

		const rows = this.rows.get(typeName)
		if (rows === undefined) throw new Error(`store: this app has no ${typeName}`)

		const was = rows.get(row.id)
		if (newer(desc, row, was)) {
			// Merged rather than replaced. A response carries what the request
			// selected, so a row that arrived with three of its eight fields is
			// not a row whose other five are now empty -- and treating it as one
			// is how a page loses a name it was showing a moment ago.
			rows.set(row.id, { ...was, ...row })
			this.dirty(`${typeName}/${row.id}`)
			this.mirror(`${typeName}/${row.id}`)
		}

		for (const n of nested) this.write(n.to, n.value)
	}

	/**
	 * batch runs `f` and tells everything subscribed once it is done.
	 *
	 * A response carrying twenty rows is one change to a screen, and a
	 * subscriber told twenty times renders nineteen frames nobody asked for.
	 * Reentrant, since writing a message writes its neighbours.
	 */
	private batch(f: () => void): void {
		if (this.pending !== undefined) {
			f()
			return
		}

		const keys = new Set<Key>()
		this.pending = keys
		try {
			f()
		} finally {
			this.pending = undefined
		}

		for (const k of keys) this.fire(k)
	}

	private dirty(k: Key): void {
		this.pending?.add(k)
	}

	/**
	 * mirror says a row wants writing out.
	 *
	 * Keys and not rows, so that a row written three times in one turn is one
	 * write of what it finally is. And on a microtask rather than at once,
	 * because a response carrying twenty rows should be one transaction --
	 * which is the same reason [Store.batch] exists, one layer down.
	 *
	 * Separate from [Store.dirty] and not derived from it: `touch` puts keys
	 * on that bus which are not rows at all, and a mirror asked to write one of
	 * those would be writing a query's identity to disk.
	 */
	private mirror(k: Key): void {
		if (this.disk === undefined) return

		this.queued.add(k)
		this.schedule()
	}

	private mirrorBlob(k: string): void {
		if (this.disk === undefined) return

		this.queuedBlobs.add(k)
		this.schedule()
	}

	private schedule(): void {
		if (this.scheduled) return

		this.scheduled = true
		queueMicrotask(() => {
			this.scheduled = false

			const keys = this.queued
			const blobs = this.queuedBlobs
			if (keys.size === 0 && blobs.size === 0) return

			this.queued = new Set()
			this.queuedBlobs = new Set()

			const disk = this.disk
			if (disk === undefined) return

			// Read now rather than when the write lands: what belongs on disk
			// is what the row is, and by the time this runs it is whatever the
			// last writer left.
			const rows: [Key, Row | undefined][] = []
			for (const at of keys) {
				const i = at.indexOf('/')
				rows.push([at, this.rows.get(at.slice(0, i))?.get(at.slice(i + 1))])
			}

			const kept: [string, Uint8Array | undefined][] = []
			for (const at of blobs) kept.push([at, this.blobs.get(at)])

			this.chain(() => disk.save({ rows, blobs: kept }))
		})
	}

	/**
	 * chain runs one mirror write after the last one has finished.
	 *
	 * A thunk and not a promise, so the transaction is **opened** in turn as
	 * well: two overlapping ones commit in whatever order the database chose,
	 * and the later write is not always the later commit -- which would leave
	 * the mirror holding a row the store had already moved past.
	 */
	private chain(work: () => Promise<void>): void {
		this.flushing = this.flushing.then(() =>
			work().then(
				() => {
					this.failed = undefined
				},
				(err: unknown) => {
					// Swallowed here and remembered for [Store.flushed]. A
					// mirror that failed is a mirror, and the store above it is
					// still the truth.
					this.failed = err
				},
			),
		)
	}

	private fire(k: Key): void {
		const set = this.listeners.get(k)
		if (set === undefined) return

		// Copied, because a listener may unsubscribe while being told.
		for (const cb of [...set]) cb()
	}
}

/** roundtrip is a message copied through the wire form, for a test. */
export function roundtrip<T extends Message>(desc: EntityDesc, v: T): T {
	return fromBinary(desc.schema, toBinary(desc.schema, v)) as T
}
