/**
 * Where a store's rows are mirrored, and the seam that keeps the store from
 * knowing where that is.
 *
 * # Memory is the truth and disk is a copy of it
 *
 * Not the other way round, and that is the whole design. Reading has to answer
 * **now** -- a component asks for a row while it renders -- and no database a
 * browser has can. So the rows live in memory and are written out behind them;
 * nothing reads the mirror except [Store.hydrate], and it reads it once.
 *
 * What that buys is the reload. A page that comes back draws what it had
 * instantly and finds out what changed behind that, instead of showing a
 * spinner for something it was holding a second ago.
 *
 * What it costs is a copy of the server's data on somebody's disk, with all
 * that implies: it is **that caller's** copy, keyed on their credential, and it
 * goes when they log out. See [Store.forget].
 *
 * It also expires. A mirror with nothing to bound it holds every question the
 * app ever asked, forever; and a restored answer is drawn as though it were
 * true for the one round trip it takes to replace it, so how old it may be is
 * worth having an answer to. See `DiskOpts.keep`.
 *
 * # There are no indexes here
 *
 * The mirror is a key and a blob, and there is nothing to query by. A local
 * index would only be useful for a local query, and a local query runs over
 * whatever this store happens to have fetched -- so "the first twenty by name"
 * computed here is not the first twenty, and it would be wrong confidently.
 * Order and membership are the server's answers; see [Store.all].
 *
 * That is also why there is no Dexie: everything it is good at is a question
 * this never asks.
 *
 * # Rows are not the whole of what a page draws
 *
 * A list is an **order and a membership**, and neither of those is in any row.
 * Bringing back the rows alone would mean a reload that still shows a spinner
 * for the list it had a second ago, which is the one thing persistence was for.
 *
 * So a mirror holds blobs beside the rows: something a layer above keeps under
 * the same credential and drops at the same moment. The query layer keeps its
 * answers there. See [Store.blob].
 *
 * @module
 */

import type { Row } from './desc.js'
import type { Key } from './store.js'

/** Held is everything one mirror has. */
export interface Held {
	readonly rows: Iterable<readonly [Key, Row]>
	readonly blobs: Iterable<readonly [string, Uint8Array]>
}

/** Changes is what to write out; a key with nothing against it is gone. */
export interface Changes {
	readonly rows: Iterable<readonly [Key, Row | undefined]>
	readonly blobs: Iterable<readonly [string, Uint8Array | undefined]>
}

/** Disk is a mirror of one caller's store. */
export interface Disk {
	/**
	 * Everything the mirror holds.
	 *
	 * Empty when there is nothing -- and empty when what is there was written
	 * against a different schema. An old shape read back as the current one is
	 * a screen that is wrong with nothing having failed, so a mirror that does
	 * not match is thrown away rather than reinterpreted.
	 */
	load(): Promise<Held>

	/** Mirror what changed, rows and blobs together in one go. */
	save(changes: Changes): Promise<void>

	/** Throw the mirror away. */
	clear(): Promise<void>

	/** Let go of whatever holds it open. */
	close(): void
}
