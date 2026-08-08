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

/** DiskOpts is what a mirror may be told beyond which store it is for. */
export interface DiskOpts {
	/**
	 * How long something is kept after it was last written, in milliseconds.
	 *
	 * A week by default, and it is on by default because the alternative is a
	 * mirror that grows for as long as the app is ever used -- every question a
	 * page asked, kept forever, on somebody's disk.
	 *
	 * It also bounds how wrong a reload can be. A restored answer is drawn as
	 * though it were true for the one round trip it takes to replace it, and
	 * "true a week ago" is a different proposition from "true in 2023".
	 *
	 * Measured from **when this last wrote it**, not from anything the server
	 * stamped: a row fetched today that has not changed since 2020 is current,
	 * and expiring it by `dateUpdated` would throw away exactly the rows that
	 * never change.
	 *
	 * `Infinity` keeps everything, which is a choice an app can make and should
	 * make deliberately.
	 */
	readonly keep?: number
}

/** A week. Long enough that a daily user keeps their instant reload. */
const KEEP = 7 * 24 * 60 * 60 * 1000

/**
 * openDisk answers with the mirror for one caller's store, ready to read.
 *
 * The database name carries the identity, so two people on one browser have two
 * mirrors and neither can read the other's rows back. The identity is already a
 * digest of a credential -- see `identityOf` -- which matters here more than
 * anywhere else, because this name is the one thing about a store that a person
 * can see in devtools.
 */
export async function openDisk(entities: readonly EntityDesc[], at: At, opts: DiskOpts = {}): Promise<Disk> {
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

	return new Mirror(db, opts.keep ?? KEEP)
}

/** deleteDisk removes one caller's mirror entirely, for a test or a wipe. */
export async function deleteDisk(at: At): Promise<void> {
	await done(indexedDB.deleteDatabase(`payday/${at.name}:${at.identity}`))
}

/** Kept is one entry: the key, what it holds, and when this last wrote it. */
interface Kept<T> {
	k: string
	v: T
	at: number
}

class Mirror implements Disk {
	private readonly db: Db
	private readonly keep: number

	constructor(db: Db, keep: number) {
		this.db = db
		this.keep = keep
	}

	/**
	 * load is everything still worth keeping, and it drops what is not.
	 *
	 * The sweep is here rather than on a timer because this is the one moment
	 * the whole mirror is already being read -- so expiring costs a comparison
	 * per entry and nothing else. Which does mean a store that is opened and
	 * never hydrated is never swept; hydrating is the call every app makes, and
	 * a mirror nobody reads is one nobody is growing either.
	 */
	async load(): Promise<Held> {
		// One transaction over both, so what comes back is one moment rather
		// than two -- an answer restored beside rows it does not match would be
		// a list drawing something that is not there.
		const tx = this.db.transaction(TABLES, 'readonly')
		const rows = tx.objectStore(ROWS).getAll()
		const blobs = tx.objectStore(BLOBS).getAll()
		await settled(tx)

		const old = Date.now() - this.keep
		const gone: [string, string][] = []

		const live = <T>(at: string, vs: unknown[]): Kept<T>[] => {
			const out: Kept<T>[] = []
			for (const r of vs as Kept<T>[]) {
				if (r.at > old) {
					out.push(r)
					continue
				}

				gone.push([at, r.k])
			}

			return out
		}

		const held = {
			rows: live<Row>(ROWS, rows.result).map((r) => [r.k as Key, r.v] as const),
			blobs: live<Uint8Array>(BLOBS, blobs.result).map((r) => [r.k, r.v] as const),
		}

		if (gone.length > 0) await this.drop(gone)

		return held
	}

	private async drop(vs: readonly (readonly [string, string])[]): Promise<void> {
		const tx = this.db.transaction(TABLES, 'readwrite')
		for (const [at, k] of vs) tx.objectStore(at).delete(k)

		await settled(tx)
	}

	async save(changes: Changes): Promise<void> {
		const tx = this.db.transaction(TABLES, 'readwrite')

		// Every request is made before anything is awaited, so the whole batch
		// is one transaction and one commit -- which is also why a response
		// carrying twenty rows costs one write and not twenty.
		// One clock reading for the batch, so everything a response carried
		// expires together rather than a millisecond apart.
		const now = Date.now()

		for (const [at, of] of [
			[ROWS, changes.rows],
			[BLOBS, changes.blobs],
		] as const) {
			const t = tx.objectStore(at)
			for (const [k, v] of of) {
				if (v === undefined) t.delete(k)
				else t.put({ k, v, at: now })
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
	// The shape of a record here, not only the shape of an entity. An entry
	// written before this carried a timestamp would read back with none, and
	// "no timestamp" is indistinguishable from "written at zero" -- so the
	// mirror is thrown away once instead, which is what it is for.
	const parts: string[] = ['payday/idb@2']

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
