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
 * # And the writes that could not be sent
 *
 * The one thing here that is not a copy. A row on disk is something the server
 * also has; a write made without a connection is something nobody else has
 * heard of yet, and the mirror is the only record that it was made. So it is
 * kept apart from the rest -- not stamped, not expired -- and a mirror may keep
 * none, which is a queue that lives as long as the page. See [Queue].
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

/**
 * Queued is one write this caller made that the server has not answered, as a
 * mirror keeps it.
 *
 * The request is its wire form, and the method is named rather than held,
 * because this outlives the page that made it: what reads it back is a page
 * with descriptors of its own, which may be a later deploy's -- and reading the
 * bytes of an older message is what protobuf is for.
 */
export interface Queued {
	/** What this write is called: minted when it was made, and ordered by when. */
	readonly id: string

	/** `<service>/<method>`, by the descriptors' full names. */
	readonly method: string

	readonly input: Uint8Array

	/** When it was made, in milliseconds. */
	readonly at: number

	/**
	 * What the server said when it refused this, and absent while it is still
	 * to be sent.
	 *
	 * A refused write is kept until somebody lets it go: it is something this
	 * caller did that did not happen, and that is worth a line on a screen.
	 */
	readonly refused?: Refusal
}

/** Refusal is a refused write's answer, in a shape a mirror can hold. */
export interface Refusal {
	readonly code: number
	readonly message: string

	/** The error's details as the wire carried them, so a form can still say which field. */
	readonly details: readonly { readonly type: string; readonly value: Uint8Array }[]
}

/**
 * Queue is where a mirror keeps the writes waiting to be sent.
 *
 * Every method answers once what it did is on disk, and the store awaits each
 * one. That is the other half of the exception in the note above: a row may
 * reach the mirror a turn late, and a write may not -- the page that keeps it
 * is the page a person may close next.
 */
export interface Queue {
	/** Every write held, in the order they were made. */
	load(): Promise<Queued[]>

	/** Keep one, or replace the one with its identifier. */
	put(v: Queued): Promise<void>

	/** Let one go. */
	drop(id: string): Promise<void>

	/**
	 * Let all of them go, in a transaction opened before this answers -- see
	 * [Writes.clear] for why that matters.
	 */
	clear(): Promise<void>
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

	/** Throw the mirror away, the writes waiting in it with it. */
	clear(): Promise<void>

	/** Let go of whatever holds it open. */
	close(): void

	/**
	 * Where the writes made without a connection wait, if this mirror keeps
	 * them; see [Store.writes].
	 *
	 * Absent is a queue in memory, which holds a write for as long as the page
	 * is open and loses it to the reload after -- still a queue, and the right
	 * one for a mirror that was never meant to outlive anything.
	 */
	readonly queue?: Queue
}
