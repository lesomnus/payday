/**
 * What a generated entity declaration says, and what the store makes of it.
 *
 * The whole design of this layer is in the shape of this file: the generator
 * writes **data**, and the behaviour is here, once. The generator this replaces
 * emitted the bodies of `get`, `_hydrate`, `_dehydrate` and `_compare` for
 * every entity, which is why it was hundreds of lines of string-building for a
 * handful of tables and why nobody could read what came out.
 *
 * Everything those bodies did can be done at run time, because a protobuf-es
 * descriptor carries every field's name, number and kind. What it does not
 * carry is the four things below.
 *
 * @module
 */

import type { DescMessage } from '@bufbuild/protobuf'

/** Ref is a field that names another entity rather than holding a value. */
export interface RefDesc {
	/** The field, as protobuf-es names it: `tenant`. */
	readonly field: string

	/** What it names, by full protobuf name: `payday.Tenant`. */
	readonly to: string
}

/**
 * EntityDesc is one entity, as `pd gen --ts` declares it.
 *
 * Four things, and each is something protobuf does not say:
 *
 *   - `domain`, which is the byte an identifier of this entity carries;
 *   - `version`, the field two answers about one row are ordered by;
 *   - `index`, which columns the database has an index on -- taken from the
 *     same `indexes:` the server's database is built from, so that a page
 *     cannot filter fast on something the server filters slowly on;
 *   - `refs`, which fields name other entities, so that a row that arrived with
 *     its neighbours nested can be taken apart.
 */
export interface EntityDesc {
	readonly typeName: string
	readonly schema: DescMessage
	readonly domain: number

	/**
	 * The field two answers about one row are ordered by, as protobuf-es names
	 * it: `dateUpdated`.
	 *
	 * Absent only for an entity nothing watches. Generation refuses a `watch:`
	 * without one, because a store with nothing to compare replaces
	 * unconditionally -- and a stale answer overwriting a fresh one is a screen
	 * that is wrong with nothing having failed.
	 */
	readonly version?: string

	/** The Dexie index declaration for this entity's table. */
	readonly index: string

	readonly refs?: readonly RefDesc[]
}

/** Row is what a table holds: a message with its neighbours taken out. */
export type Row = Record<string, unknown> & { id: string }

/**
 * key is an identifier as a table key.
 *
 * Hex rather than the bytes themselves, because IndexedDB compares a
 * `Uint8Array` by identity in some engines and by content in others -- and a
 * store whose primary key means different things in two browsers is a bug that
 * only one person ever sees. A string means the same thing everywhere, sorts in
 * the order the identifiers were minted, and is what a URL holds anyway.
 */
export function key(v: Uint8Array | string | undefined): string {
	if (v === undefined) return ''
	if (typeof v === 'string') return v

	let s = ''
	for (const b of v) s += b.toString(16).padStart(2, '0')

	return s
}

/** bytes is [key] read back. */
export function bytes(v: string): Uint8Array {
	const b = new Uint8Array(v.length >> 1)
	for (let i = 0; i < b.length; i++) b[i] = Number.parseInt(v.slice(i * 2, i * 2 + 2), 16)

	return b
}
