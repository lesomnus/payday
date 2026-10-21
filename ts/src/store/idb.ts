/**
 * The mirror, on IndexedDB.
 *
 *   const at = { name: 'acme', identity: await identityOf(credential) }
 *   const store = Store.open(entities, { ...at, disk: await openDisk(entities, at) })
 *   void deleteOtherDisks(at)
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
import type { Changes, Disk, Held, Queue, Queued } from './disk.js'
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
	onblocked: (() => void) | null
}

/** What `deleteDatabase` answers with: a request that may be blocked too. */
interface DeleteReq extends Req<unknown> {
	onblocked: (() => void) | null
}

interface Db {
	readonly objectStoreNames: { contains(name: string): boolean }
	createObjectStore(name: string, opts?: { keyPath?: string }): Table
	transaction(names: string | string[], mode: 'readonly' | 'readwrite'): Tx
	close(): void
	onversionchange: (() => void) | null
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
	deleteDatabase(name: string): DeleteReq

	/** Absent from a browser too old to list them; see [deleteOtherDisks]. */
	databases?(): Promise<readonly { readonly name?: string }[]>
}

/** Rows, keyed in line by `k` so that reading everything is one request. */
const ROWS = 'rows'

/** What a layer above kept beside them; see [Store.blob]. */
const BLOBS = 'blobs'

/** One entry: which schema wrote what is in the other two. */
const META = 'meta'

/**
 * The writes waiting to be sent; see [Store.writes].
 *
 * Outside [TABLES] on purpose: those two are copies of what the server has,
 * and are thrown away when the schema moves or when they are a week old. A
 * write is not a copy of anything -- it is the only record that it was made --
 * so neither of those gets to take one. Its request is in wire form, which is
 * what protobuf reads across a schema change; one that no longer reads is
 * refused where it is read, and said so, rather than dropped here.
 */
const WRITES = 'writes'

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
 * digest -- see `identityOf` -- which matters here more than anywhere else,
 * because this name is the one thing about a store that a person can see in
 * devtools.
 *
 * # Another page holding it
 *
 * A mirror is open in every page on its store, and a database cannot be
 * moved to a later version, or deleted, while a page holds it open. So this
 * one lets go the moment another page asks for either -- it is closed, and
 * every use of it after that fails saying why -- and a page that would rather
 * keep it cannot hold up the deploy after it.
 *
 * A page that does not let go is one written before mirrors did, holding
 * the mirror at an older version. Opening it then is refused, where it used
 * to wait for as long as that page stayed open.
 */
export async function openDisk(entities: readonly EntityDesc[], at: At, opts: DiskOpts = {}): Promise<Disk> {
	const stamp = await stampOf(entities)
	const name = nameOf(at)

	// Version 2 is the queue: a mirror written before it gains the table and
	// keeps everything else.
	const req = indexedDB.open(name, 2)
	req.onupgradeneeded = (): void => {
		const db = req.result
		for (const t of [...TABLES, WRITES]) {
			if (!db.objectStoreNames.contains(t)) db.createObjectStore(t, { keyPath: 'k' })
		}
		if (!db.objectStoreNames.contains(META)) db.createObjectStore(META)
	}

	const db = await opening(req, name)
	const mirror = new Mirror(db, opts.keep ?? KEEP)
	db.onversionchange = (): void => mirror.release()

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

	return mirror
}

/**
 * deleteDisk removes one caller's mirror entirely, for a test or a wipe.
 *
 * Refused rather than waited on while a page written before mirrors let go of
 * themselves is holding it open; see [openDisk].
 */
export async function deleteDisk(at: At): Promise<void> {
	const name = nameOf(at)
	const req = indexedDB.deleteDatabase(name)

	await new Promise<void>((ok, no) => {
		req.onsuccess = (): void => ok()
		req.onerror = (): void => no(req.error ?? new Error('idb: the request failed'))
		req.onblocked = (): void => no(new Error(`idb: another page holds ${name} open and will not let go of it`))
	})
}

/**
 * deleteOtherDisks removes every mirror of this app but the one for `at`: what
 * a page calls once it knows whose store it is opening.
 *
 * A mirror is swept only when its store is hydrated, and a store is hydrated
 * only by its own caller -- so the mirror of anybody who stopped using this
 * browser stays, rows and answers and the writes they never sent, for as long
 * as the browser keeps the database. Once somebody else's store is open here,
 * nobody is coming back for it in a way that should find it: a person signing
 * in again on a browser somebody else has since used is not owed what they
 * left on it, and the person using it now is owed not finding it.
 *
 * Which is also what it costs: writes another caller made and never sent are
 * thrown away with their mirror. A page that would rather not lose them sends
 * them before it lets that caller go.
 *
 * Best effort. A browser too old to list its databases keeps them, and one held
 * open by a page that will not let go is left for the next time. It answers
 * once each is gone or so held -- which a request another page is still
 * waiting on can delay, since a database takes its requests in turn -- so a
 * page asks for it and draws without waiting for the answer.
 */
export async function deleteOtherDisks(at: At): Promise<void> {
	// Never a rejection, since nothing waits on it to hear one.
	let all: readonly { readonly name?: string }[] | undefined
	try {
		all = await indexedDB.databases?.()
	} catch {
		return
	}
	if (all === undefined) return

	const prefix = `payday/${at.name}:`
	const mine = nameOf(at)

	await Promise.all(
		all.map(({ name }) => {
			// Of this app, and not of another whose name begins with this one's:
			// an identity has no colon in it, so a name with one after the
			// prefix is somebody else's prefix.
			if (name === undefined || name === mine || !name.startsWith(prefix)) return
			if (name.slice(prefix.length).includes(':')) return

			const req = indexedDB.deleteDatabase(name)

			return new Promise<void>((ok) => {
				req.onsuccess = (): void => ok()
				req.onerror = (): void => ok()
				req.onblocked = (): void => ok()
			})
		}),
	)
}

/** nameOf is the database one caller's mirror is kept in. */
function nameOf(at: At): string {
	return `payday/${at.name}:${at.identity}`
}

/**
 * opening is an open request's database, refused if another page is holding it
 * at an older version and will not let go; see [openDisk].
 *
 * A request that was refused can still succeed later -- it is queued, and goes
 * once the other page closes -- so a database that arrives after the refusal
 * is closed rather than left open with nobody holding it.
 */
function opening(req: OpenReq, name: string): Promise<Db> {
	return new Promise<Db>((ok, no) => {
		let refused = false

		req.onsuccess = (): void => {
			if (refused) req.result.close()
			else ok(req.result)
		}
		req.onerror = (): void => no(req.error ?? new Error('idb: the request failed'))
		req.onblocked = (): void => {
			refused = true
			no(new Error(`idb: another page holds ${name} open at an older version; close it, or reload it, and this one opens`))
		}
	})
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

	/** Let go of for another page; see [Mirror.release]. */
	private released = false

	constructor(db: Db, keep: number) {
		this.db = db
		this.keep = keep
	}

	/**
	 * release closes the mirror because another page asked to move it to a
	 * later version or to delete it, and neither can happen while this page
	 * holds it; see [openDisk].
	 *
	 * The store above goes on in memory. What fails is what needed the disk --
	 * a write kept, a write sent and let go -- and it fails saying why, so
	 * whatever sees it knows the page wants reloading.
	 */
	release(): void {
		this.released = true
		this.db.close()
	}

	/** tx is a transaction on the mirror, while this page still holds it. */
	private tx(names: string | string[], mode: 'readonly' | 'readwrite'): Tx {
		if (this.released) {
			throw new Error('idb: this mirror was let go for another page, which moved it on or deleted it; reload')
		}

		return this.db.transaction(names, mode)
	}

	/**
	 * One transaction per call, and each answers when it has committed: see
	 * [Queue] for why a write may not reach the disk a turn late the way a row
	 * may. Read back in key order, which is the order they were made.
	 */
	readonly queue: Queue = {
		load: async (): Promise<Queued[]> => {
			const tx = this.tx(WRITES, 'readonly')
			const all = tx.objectStore(WRITES).getAll()
			await settled(tx)

			return (all.result as Kept<Queued>[]).map((r) => r.v)
		},
		put: async (v: Queued): Promise<void> => {
			const tx = this.tx(WRITES, 'readwrite')
			tx.objectStore(WRITES).put({ k: v.id, v, at: Date.now() })

			await settled(tx)
		},
		drop: async (id: string): Promise<void> => {
			const tx = this.tx(WRITES, 'readwrite')
			tx.objectStore(WRITES).delete(id)

			await settled(tx)
		},
		clear: (): Promise<void> => {
			// Opened before anything is awaited, which is the whole of the
			// promise this makes. And a mirror that can no longer open one
			// says so in the promise, as every other method here does, rather
			// than throwing at the caller who is signing out.
			try {
				const tx = this.tx(WRITES, 'readwrite')
				tx.objectStore(WRITES).clear()

				return settled(tx)
			} catch (err) {
				return Promise.reject(err)
			}
		},
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
		const tx = this.tx(TABLES, 'readonly')
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
		const tx = this.tx(TABLES, 'readwrite')
		for (const [at, k] of vs) tx.objectStore(at).delete(k)

		await settled(tx)
	}

	async save(changes: Changes): Promise<void> {
		const tx = this.tx(TABLES, 'readwrite')

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
		// The writes too: this is a caller being done, and what they never sent
		// is theirs as much as what they read. See [Store.forget].
		const tx = this.tx([...TABLES, WRITES], 'readwrite')
		for (const t of [...TABLES, WRITES]) tx.objectStore(t).clear()

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
