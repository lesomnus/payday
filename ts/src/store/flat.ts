/**
 * Taking a message apart, and putting one back together.
 *
 * This is the half of the store that the generator used to write per entity,
 * and the reason it does not have to is that a protobuf-es descriptor knows
 * every field's name and kind at run time. So `flatten` and `nest` are written
 * once, here, for every entity there will ever be.
 *
 * @module
 */

import { type DescField, type DescMessage, type Message } from '@bufbuild/protobuf'

import { key } from './desc.js'
import type { EntityDesc, Row } from './desc.js'

/**
 * flatten turns a message into the row a table holds, and answers with the
 * neighbours it was carrying.
 *
 * Two things happen, and both are what makes this a store rather than a cache
 * of responses.
 *
 * **A reference becomes a key.** A row that arrived with its tenant nested is
 * stored as `tenantId`, and the tenant is stored once in its own table. Keeping
 * the nested copy would mean a tenant renamed in one place and stale in five
 * others, which is the ordinary way a cache goes wrong.
 *
 * **Bytes become text.** An identifier is sixteen bytes on the wire and a hex
 * string here; see [key] for why. It also means every reference in the store is
 * comparable with `===`, which a `Uint8Array` is not.
 */
export function flatten(
	desc: EntityDesc,
	v: Message,
): { row: Row; nested: Array<{ to: string; value: Message }> } {
	const refs = new Map((desc.refs ?? []).map((r) => [r.field, r.to]))
	const row: Record<string, unknown> = {}
	const nested: Array<{ to: string; value: Message }> = []

	for (const f of fields(desc.schema)) {
		const name = f.localName
		const got = (v as Record<string, unknown>)[name]
		if (got === undefined || got === null) continue

		const to = refs.get(name)
		if (to !== undefined) {
			// A neighbour. What is kept here is its key, and the neighbour
			// itself goes to its own table -- unless the server sent only the
			// key, which is what a request that did not select it looks like.
			const id = (got as Record<string, unknown>).id
			row[`${name}Id`] = key(id as Uint8Array | undefined)

			if (isWhole(f.message ?? desc.schema, got as Message)) nested.push({ to, value: got as Message })

			continue
		}

		row[name] = isBytes(f) ? key(got as Uint8Array) : got
	}

	return { row: row as Row, nested }
}

/**
 * isWhole reports whether a nested message is a row or only a reference to one.
 *
 * A response carries as much of a neighbour as the request selected: sometimes
 * the whole row, sometimes nothing but the identifier. Storing the second as if
 * it were the first replaces a complete row with an almost-empty one, and does
 * it silently -- an empty string is a legal name.
 *
 * The test is whether any field besides the key differs from its default, and
 * it is asked of the **descriptor** rather than of the object. That is not
 * fussiness: a message built by protobuf-es has every field present, so
 * `Object.entries` sees `labels: {}` and `alias: ""` on a message that carries
 * nothing at all. Reading the descriptor is the difference between "this field
 * exists" and "this field was said", and only the second one means anything
 * here.
 *
 * There is no flag on the wire for this, and there should not be: a request
 * asked for some of a row and got what it asked for. What is missing is the
 * question nobody asked.
 */
function isWhole(d: DescMessage, v: Message): boolean {
	const got = v as Record<string, unknown>

	for (const f of d.fields) {
		if (f.localName === 'id') continue

		// The timestamps are never evidence. A caller does not write them --
		// the server stamps them -- so their presence says nothing about
		// whether this message is a row or a reference to one. It is not a
		// nicety: a Go server sends an unselected `date_updated` as Go's zero
		// time, which is the year 1, which is a value. Reading that as data is
		// how a reference-only tenant gets stored as a nameless tenant.
		if (f.fieldKind === 'message' && f.message?.typeName === 'google.protobuf.Timestamp') continue

		const w = got[f.localName]
		if (w === undefined || w === null) continue

		switch (typeof w) {
			case 'string':
				if (w !== '') return true
				continue
			case 'number':
				if (w !== 0) return true
				continue
			case 'bigint':
				if (w !== 0n) return true
				continue
			case 'boolean':
				if (w) return true
				continue
		}

		if (w instanceof Uint8Array) {
			if (w.length > 0) return true
			continue
		}
		if (Array.isArray(w)) {
			if (w.length > 0) return true
			continue
		}
		if (typeof w === 'object') {
			// A map or a nested message. Both are objects with keys, and an
			// empty one of either says nothing.
			if (Object.keys(w as object).some((k) => k !== '$typeName')) return true
			continue
		}
	}

	return false
}

/** fields is every declared field of a message, including those in a oneof. */
export function* fields(d: DescMessage): Generator<DescField> {
	for (const f of d.fields) yield f
}

function isBytes(f: DescField): boolean {
	return f.fieldKind === 'scalar' && f.scalar === 12 /* ScalarType.BYTES */
}

/**
 * newer reports whether `a` should replace `b`.
 *
 * The version is a timestamp the server stamps on every write, and comparing it
 * is the whole of what keeps a late answer from erasing a fresh one. Two
 * answers about one row arrive out of order often enough to be ordinary: a
 * snapshot racing an event, a reconnection replaying, an outbox draining after
 * a restart.
 *
 * An entity with no version always replaces, and that is not a compromise --
 * generation refuses a `watch:` without one, so the only entities that get here
 * are ones nothing streams, whose rows change only when this client asked them
 * to.
 */
export function newer(desc: EntityDesc, a: Row, b: Row | undefined): boolean {
	if (b === undefined) return true
	if (desc.version === undefined) return true

	return stamp(a[desc.version]) >= stamp(b[desc.version])
}

/** stamp is a protobuf Timestamp as a number, and 0 for one that is not there. */
function stamp(v: unknown): number {
	if (v === undefined || v === null) return 0

	const t = v as { seconds?: bigint; nanos?: number }
	if (t.seconds === undefined) return 0

	return Number(t.seconds) * 1e9 + (t.nanos ?? 0)
}
