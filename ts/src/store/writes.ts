/**
 * The writes this caller made that the server has not answered yet.
 *
 * # Why it is the store's
 *
 * A write is tied to a credential the way a row is: it is something *this
 * caller* did, and sending it under the next one would be the next one doing
 * it. So it lives where the credential is known, goes to the mirror the rows go
 * to, and goes when [Store.forget] says this caller is done.
 *
 * What it does not do is send anything. That is the query layer's, which holds
 * the transport; see [Queries.send].
 *
 * # The disk is the truth here, and that is the exception
 *
 * Everywhere else in this store memory is the truth and the mirror a copy of
 * it. Rows can be that way round because a row is **state**: two tabs writing
 * the same row write the same thing, and the later one wins harmlessly. A write
 * is an **effect**, and two tabs that each sent what they hydrated would send it
 * twice. So whoever is about to send re-reads what is waiting from the mirror
 * first -- see [Writes.reload] -- and lets each one go on disk as soon as it is
 * answered, before the next is sent.
 *
 * @module
 */

import type { Queue, Queued, Refusal } from './disk.js'

/** Writes is one store's queue, in memory and, when there is one, on disk. */
export class Writes {
	private list: readonly Queued[] = []
	private readonly disk: Queue | undefined
	private readonly told: () => void

	/**
	 * How many times this was cleared. A read of the mirror that began before
	 * the caller was done answers with what they were holding, and is dropped
	 * rather than put back; see [Writes.clear].
	 */
	private gen = 0

	/** Set by [Writes.clear], and never unset; see [Writes.closed]. */
	private done = false

	/**
	 * Made by the store, with the mirror's queue if it has one and the way to
	 * tell everything subscribed that the list moved.
	 */
	constructor(disk: Queue | undefined, told: () => void) {
		this.disk = disk
		this.told = told
	}

	/**
	 * all is every write held, in the order they were made, **now**.
	 *
	 * The same array until something changes, which is what a reactive binding
	 * needs of a snapshot.
	 */
	all(): readonly Queued[] {
		return this.list
	}

	/**
	 * closed is whether the caller is done: [Store.forget] was called, so
	 * nothing is kept and nothing is sent from here again. A write kept after
	 * it would be sent as whoever the transport carries next.
	 */
	get closed(): boolean {
		return this.done
	}

	/** has is whether one is still held, for whoever is about to send it. */
	has(id: string): boolean {
		return this.list.some((w) => w.id === id)
	}

	/** add keeps one more, and answers once it is on disk. */
	async add(v: Queued): Promise<void> {
		if (this.done) {
			throw new Error('store: this caller is done, so a write kept now would be sent as whoever signs in next')
		}

		const gen = this.gen
		await this.disk?.put(v)
		if (gen !== this.gen) return

		this.set([...this.list.filter((w) => w.id !== v.id), v].sort(byId))
	}

	/** refuse keeps what the server said about one, in place of sending it again. */
	async refuse(id: string, refused: Refusal): Promise<void> {
		const was = this.list.find((w) => w.id === id)
		if (was === undefined) return

		const gen = this.gen
		const v: Queued = { ...was, refused }
		await this.disk?.put(v)
		if (gen !== this.gen) return

		this.set(this.list.map((w) => (w.id === id ? v : w)))
	}

	/** drop lets one go, on disk first. */
	async drop(id: string): Promise<void> {
		await this.disk?.drop(id)

		this.set(this.list.filter((w) => w.id !== id))
	}

	/**
	 * reload reads the queue back from the mirror, which is what another page
	 * on this store has been writing to as well.
	 *
	 * [Store.hydrate] calls it once, and whoever sends calls it again first;
	 * see the note above for why that is the one read of the mirror that is not
	 * once.
	 */
	async reload(): Promise<void> {
		if (this.disk === undefined) return

		const gen = this.gen
		const vs = await this.disk.load()
		if (gen !== this.gen) return

		this.set(vs)
	}

	/**
	 * clear forgets them, for [Store.forget].
	 *
	 * The mirror's table is cleared **now**, not behind the rows' writes the way
	 * [Store.forget] clears the rest. A transaction runs after the ones opened
	 * before it, so a write kept a moment earlier is cleared with the others,
	 * and a read opened after this one finds nothing -- where a clear that
	 * waited its turn would leave a read in between free to bring back a write
	 * of somebody who has gone.
	 */
	clear(): void {
		this.gen++
		this.done = true
		this.set([])

		void this.disk?.clear().catch(() => {})
	}

	private set(v: readonly Queued[]): void {
		if (v.length === 0 && this.list.length === 0) return

		this.list = v
		this.told()
	}
}

function byId(a: Queued, b: Queued): number {
	return a.id < b.id ? -1 : a.id > b.id ? 1 : 0
}
