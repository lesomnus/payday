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
 * Which is a separate concern, and not this one. What persistence buys is
 * surviving a reload and working offline; what it costs is a schema for a copy
 * of something the server has. It goes behind a seam when it is wanted.
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
import { flatten, newer } from './flat.js'

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

	private constructor(name: string, by: Map<string, EntityDesc>) {
		this.name = name
		this.by = by
		for (const typeName of by.keys()) this.rows.set(typeName, new Map())
	}

	/** open answers with the store for `opts.identity`. */
	static open(entities: readonly EntityDesc[], opts: Opts): Store {
		const by = new Map<string, EntityDesc>()
		for (const v of entities) by.set(v.typeName, v)

		return new Store(`${opts.name}:${opts.identity}`, by)
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
