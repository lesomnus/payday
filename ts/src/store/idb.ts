/**
 * The mirror, on IndexedDB.
 *
 *   const at = { name: 'acme', identity: await identityOf(credential) }
 *   const store = Store.open(entities, { ...at, disk: await openDisk(entities, at) })
 *   await store.hydrate()
 *
 * It is a separate entry because persistence is a separate decision: an app
 * that does not want a copy of its rows on somebody's disk never imports this,
 * and nothing else in the package reaches for it.
 *
 * # Why raw IndexedDB
 *
 * Because what is stored is a key and a blob, and that is all. See `disk.ts`
 * for why there is nothing to index: the memory store never queries, so the
 * mirror never has to answer a question -- only hand back what it was given.
 *
 * A library for that would be a dependency whose whole surface goes unused, and
 * one of them would cost something real: declaring the tables up front means a
 * version bump every time an app adds an entity, which is a schema migration
 * for a cache. One object store keyed by `<entity>/<id>` needs none.
 *
 * @module
 */

import type { EntityDesc, Row } from './desc.js'
import { digest } from './digest.js'
import type { Changes, Disk, Held } from './disk.js'
import type { Key } from './store.js'

// The whole of what this module needs from a browser, declared rather than by
// putting `DOM` in `lib` -- which would make every browser global compile in a
// package that also runs in a worker and in Node. See `digest.ts` for the same
// reasoning, and note that this list is also the documentation of how little
// IndexedDB is being asked for.
interface Req<T> {
	readonly result: T
	readonly error: unknown
	onsuccess: (() => void) | null
	onerror: (() => void) | null
}

interface OpenReq extends Req<Db> {
	onupgradeneeded: (() => void) | null
}

interface Db {
	readonly objectStoreNames: { contains(name: string): boolean }
	createObjectStore(name: string, opts?: { keyPath?: string }): Table
	transaction(names: string | string[], mode: 'readonly' | 'readwrite'): Tx
	close(): void
}

interface Tx {
	readonly error: unknown
	objectStore(name: string): Table
	oncomplete: (() => void) | null
	onerror: (() => void) | null
	onabort: (() => void) | null
}

interface Table {
	put(value: unknown, key?: unknown): Req<unknown>
	get(key: unknown): Req<unknown>
	delete(key: unknown): Req<unknown>
	getAll(): Req<unknown[]>
	clear(): Req<unknown>
}

declare const indexedDB: {
	open(name: string, version?: number): OpenReq
	deleteDatabase(name: string): Req<unknown>
}

/** Rows, keyed in line by `k` so that reading everything is one request. */
const ROWS = 'rows'

/** What a layer above kept beside them; see [Store.blob]. */
const BLOBS = 'blobs'

/** One entry: which schema wrote what is in the other two. */
const META = 'meta'

const TABLES = [ROWS, BLOBS]

/** At is which store this is the mirror of; the same shape [Store.open] takes. */
export interface At {
	readonly name: string
	readonly identity: string
}

/**
 * openDisk answers with the mirror for one caller's store, ready to read.
 *
 * The database name carries the identity, so two people on one browser have two
 * mirrors and neither can read the other's rows back. The identity is already a
 * digest of a credential -- see `identityOf` -- which matters here more than
 * anywhere else, because this name is the one thing about a store that a person
 * can see in devtools.
 */
export async function openDisk(entities: readonly EntityDesc[], at: At): Promise<Disk> {
	const stamp = await stampOf(entities)
	const name = `payday/${at.name}:${at.identity}`

	const req = indexedDB.open(name, 1)
	req.onupgradeneeded = (): void => {
		const db = req.result
		for (const t of TABLES) {
			if (!db.objectStoreNames.contains(t)) db.createObjectStore(t, { keyPath: 'k' })
		}
		if (!db.objectStoreNames.contains(META)) db.createObjectStore(META)
	}

	const db = await done(req)

	// Two transactions rather than one, deliberately. A transaction commits by
	// itself once nothing is pending on it, and an `await` between two requests
	// is exactly that gap -- which works until the day it does not. So: read the
	// stamp, decide, and then write without awaiting anything in between.
	const was = await done(db.transaction(META, 'readonly').objectStore(META).get('stamp'))
	if (was !== stamp) {
		const tx = db.transaction([...TABLES, META], 'readwrite')
		for (const t of TABLES) tx.objectStore(t).clear()
		tx.objectStore(META).put(stamp, 'stamp')
		await settled(tx)
	}

	return new Mirror(db)
}

/** deleteDisk removes one caller's mirror entirely, for a test or a wipe. */
export async function deleteDisk(at: At): Promise<void> {
	await done(indexedDB.deleteDatabase(`payday/${at.name}:${at.identity}`))
}

class Mirror implements Disk {
	private readonly db: Db

	constructor(db: Db) {
		this.db = db
	}

	async load(): Promise<Held> {
		// One transaction over both, so what comes back is one moment rather
		// than two -- an answer restored beside rows it does not match would be
		// a list drawing something that is not there.
		const tx = this.db.transaction(TABLES, 'readonly')
		const rows = tx.objectStore(ROWS).getAll()
		const blobs = tx.objectStore(BLOBS).getAll()
		await settled(tx)

		return {
			rows: (rows.result as { k: Key; v: Row }[]).map((r) => [r.k, r.v] as const),
			blobs: (blobs.result as { k: string; v: Uint8Array }[]).map((r) => [r.k, r.v] as const),
		}
	}

	async save(changes: Changes): Promise<void> {
		const tx = this.db.transaction(TABLES, 'readwrite')

		// Every request is made before anything is awaited, so the whole batch
		// is one transaction and one commit -- which is also why a response
		// carrying twenty rows costs one write and not twenty.
		for (const [at, of] of [
			[ROWS, changes.rows],
			[BLOBS, changes.blobs],
		] as const) {
			const t = tx.objectStore(at)
			for (const [k, v] of of) {
				if (v === undefined) t.delete(k)
				else t.put({ k, v })
			}
		}

		await settled(tx)
	}

	async clear(): Promise<void> {
		const tx = this.db.transaction(TABLES, 'readwrite')
		for (const t of TABLES) tx.objectStore(t).clear()

		await settled(tx)
	}

	close(): void {
		this.db.close()
	}
}

/**
 * stampOf is what the rows in a mirror were written against.
 *
 * Every entity's name, and every field's number, name and kind. Which is the
 * question actually being asked: a field renamed, retyped, added or dropped
 * means the rows on disk mean something slightly different from what reading
 * them would conclude, and the cheap answer to that is to throw them away and
 * let the server fill it in again.
 *
 * Not a version anybody has to remember to bump. A deploy that changed the
 * schema changed this, and a deploy that did not, did not.
 */
async function stampOf(entities: readonly EntityDesc[]): Promise<string> {
	const parts: string[] = []

	for (const e of [...entities].sort((a, b) => (a.typeName < b.typeName ? -1 : a.typeName > b.typeName ? 1 : 0))) {
		const fields = e.schema.fields.map((f) => `${f.number}:${f.localName}:${f.fieldKind}`).join(',')
		parts.push(`${e.typeName}[${e.version ?? ''}](${fields})`)
	}

	return digest(parts.join('|'))
}

function done<T>(r: Req<T>): Promise<T> {
	return new Promise<T>((ok, no) => {
		r.onsuccess = (): void => ok(r.result)
		r.onerror = (): void => no(r.error ?? new Error('idb: the request failed'))
	})
}

function settled(tx: Tx): Promise<void> {
	return new Promise<void>((ok, no) => {
		tx.oncomplete = (): void => ok()
		tx.onerror = (): void => no(tx.error ?? new Error('idb: the transaction failed'))
		tx.onabort = (): void => no(tx.error ?? new Error('idb: the transaction was aborted'))
	})
}
