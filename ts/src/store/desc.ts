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

import type { DescMessage, DescService } from '@bufbuild/protobuf'

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
 * Four things, and each is something protobuf does not say **here**:
 *
 *   - `domain`, which is the byte an identifier of this entity carries;
 *   - `version`, the field two answers about one row are ordered by;
 *   - `refs`, which fields name other entities, so that a row that arrived with
 *     its neighbours nested can be taken apart;
 *   - `service`, which is the one that protobuf does carry and cannot be
 *     reached from here: a message descriptor knows the file it is in, and the
 *     service that answers about it is in another one.
 *
 * The server's `indexes:` are **not** here, and used to be. Nothing on this
 * side has a question to answer with one: rows are reached by identifier, and
 * order and membership are what the server answered with. A local index would
 * serve only a local query, and a local query runs over whatever this store
 * happens to hold -- so it would answer a different question confidently. See
 * [Store.all].
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

	readonly refs?: readonly RefDesc[]

	/**
	 * The service that answers about this entity, which is what turns a
	 * declaration into a call.
	 *
	 * It is here because it cannot be worked out from `schema`: an entity's
	 * messages and its service are declared in two `.proto` files -- the
	 * schema's and the contract generation writes beside it -- so the message
	 * descriptor's own file holds no service at all.
	 *
	 * What it does not decide is which RPCs there are. That is the service's to
	 * say, and it says it by having them: `service.method.list` is undefined
	 * for an entity that declared no `list:`, which is the same answer a
	 * boolean here would have given and one fewer thing to disagree with.
	 *
	 * Absent for an entity that is stored and not served. The store does not
	 * read this -- it is for a caller that has a declaration and wants the call
	 * that goes with it.
	 */
	readonly service?: DescService
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
