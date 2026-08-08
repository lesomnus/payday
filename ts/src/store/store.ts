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
 * copy of each, reachable by identifier and by name.
 *
 * Everything that makes that hard is mechanical and comes out of the schema:
 * normalizing a nested response, keeping several keys for one row, ordering two
 * answers about one row, and applying a change that arrived while nobody was
 * looking. Every one of those is a thing a person writing it by hand gets right
 * four times and wrong the fifth, which is exactly what is worth generating a
 * caller for.
 *
 * # It is tied to who is asking
 *
 * A store is opened **for an identity**, and one opened for another identity is
 * a different database. Nothing leaks if it is not -- the server never sent
 * anything the caller may not see -- but the screen is wrong: rows a narrowed
 * credential can no longer read are still there, and a page drawn from them
 * shows things that are not there any more.
 *
 * So the identity is part of the database name, and changing it is opening a
 * different one. There is no invalidation to get right and nothing to remember
 * to call.
 *
 * # What is not here
 *
 * Reactivity. Dexie answers `liveQuery` over any of this and that is an
 * observable, so a React, Vue or Svelte adapter is about ten lines -- which is
 * why the line is drawn at the store rather than at a hook. A framework binding
 * payday shipped would be a convenience; a framework payday *required* would be
 * the thing that makes full-stack frameworks fail.
 *
 * @module
 */

import { create, fromBinary, toBinary, type Message } from '@bufbuild/protobuf'
import * as dexie from 'dexie'
import type { Dexie as DexieDb, EntityTable, Table } from 'dexie'

import { bytes, key, type EntityDesc, type Row } from './desc.js'
import { flatten, newer } from './flat.js'

/** Opts is what a store is opened with. */
export interface Opts {
	/**
	 * Who this store is for, which becomes part of the database name.
	 *
	 * An identifier, or anything else that names one caller. See the note on
	 * identity above for why it is not optional: a store shared between two
	 * callers shows the first one's rows to the second, and no server said
	 * anything wrong.
	 */
	readonly identity: string

	/** The name of the app, which is the rest of the database name. */
	readonly name: string
}

/**
 * Store is every entity of one app, for one caller.
 *
 * It is opened over the declarations `pd gen --ts` wrote, and nothing about it
 * is per entity: `table` answers with a typed handle for any of them, and the
 * rules -- normalize, reconcile, apply -- are written once.
 */
export class Store {
	readonly db: DexieDb
	readonly by: Map<string, EntityDesc>

	private constructor(db: DexieDb, by: Map<string, EntityDesc>) {
		this.db = db
		this.by = by
	}

	/**
	 * open answers with the store for `opts.identity`.
	 *
	 * The schema version is 1 and stays 1: what changes an app's tables is a
	 * change to its protobuf schema, and the answer to that is to throw the
	 * local copy away and let a `Watch` fill it again. A migration would be
	 * writing a database migration for a cache, which is work in exchange for
	 * saving one refill of something the server has anyway.
	 */
	static open(entities: readonly EntityDesc[], opts: Opts): Store {
		const db = new dexie.Dexie(`${opts.name}:${opts.identity}`)

		const stores: Record<string, string> = {}
		const by = new Map<string, EntityDesc>()
		for (const v of entities) {
			stores[v.typeName] = v.index
			by.set(v.typeName, v)
		}

		db.version(1).stores(stores)

		return new Store(db, by)
	}

	/** table is the handle for one entity. */
	table(typeName: string): Table<Row, string> {
		if (!this.by.has(typeName)) {
			throw new Error(`store: this app has no ${typeName}`)
		}

		return this.db.table(typeName)
	}

	/** desc is the declaration for one entity. */
	desc(typeName: string): EntityDesc {
		const v = this.by.get(typeName)
		if (v === undefined) throw new Error(`store: this app has no ${typeName}`)

		return v
	}

	/**
	 * put writes a message and everything it was carrying.
	 *
	 * One transaction, over every table a nested neighbour could land in: a row
	 * whose tenant was written and whose own write then failed would leave the
	 * store saying a robot exists that does not, which a page would draw.
	 */
	async put(typeName: string, ...vs: Message[]): Promise<void> {
		const names = [...this.by.keys()]

		await this.db.transaction('rw', names.map((n) => this.db.table(n)), async () => {
			for (const v of vs) await this.write(typeName, v)
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
	private async write(typeName: string, v: Message): Promise<void> {
		const desc = this.desc(typeName)
		const { row, nested } = flatten(desc, v)
		if (row.id === '') return

		const was = await this.table(typeName).get(row.id)
		if (newer(desc, row, was)) {
			// Merged rather than replaced. A response carries what the request
			// selected, so a row that arrived with three of its eight fields is
			// not a row whose other five are now empty -- and treating it as one
			// is how a page loses a name it was showing a moment ago.
			await this.table(typeName).put({ ...was, ...row })
		}

		for (const n of nested) await this.write(n.to, n.value)
	}

	/**
	 * get answers with a row as the message it came from, or undefined.
	 *
	 * The neighbours are **not** filled back in. What comes back is the row,
	 * with a `<field>Id` where the reference was, and reading a neighbour is
	 * asking for it by name -- which is one lookup against a primary key rather
	 * than a join nobody asked for. A page that wants both asks for both.
	 */
	async get<T extends Message>(typeName: string, id: Uint8Array | string): Promise<T | undefined> {
		const row = await this.table(typeName).get(key(id))
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
	async apply(
		typeName: string,
		items: readonly { id: Uint8Array; value?: Message | undefined }[],
	): Promise<void> {
		const names = [...this.by.keys()]

		await this.db.transaction('rw', names.map((n) => this.db.table(n)), async () => {
			for (const item of items) {
				if (item.value === undefined) {
					// Absence is how a removal is said. Nothing else says it,
					// and nothing needs to.
					await this.table(typeName).delete(key(item.id))
					continue
				}

				await this.write(typeName, item.value)
			}
		})
	}

	/** close lets go of the database. */
	close(): void {
		this.db.close()
	}

	/**
	 * forget throws this caller's copy away.
	 *
	 * What it is for is logging out. The rows are not a secret -- the server
	 * only ever sent what that caller could see -- but they are *that caller's*,
	 * and leaving them where the next one opens the same page is the kind of
	 * thing that looks like a leak whether or not it is one.
	 */
	async forget(): Promise<void> {
		await this.db.delete()
	}
}

/** roundtrip is a message copied through the wire form, for a test. */
export function roundtrip<T extends Message>(desc: EntityDesc, v: T): T {
	return fromBinary(desc.schema, toBinary(desc.schema, v)) as T
}

export type { EntityTable }
