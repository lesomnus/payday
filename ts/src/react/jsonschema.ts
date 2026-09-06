/**
 * A JSON Schema for a message, from its descriptor.
 *
 * The panel shows a row as protobuf JSON and lets it be edited, and an editor
 * over a document with no schema is a text box: no completion, no hover, and a
 * typo found by the server rather than by the caret. A descriptor already says
 * every field's name, kind, cardinality and enum values -- which is exactly
 * what a JSON Schema is -- so this is a translation and not a design.
 *
 * What it produces is draft-07, because that is what the editors that consume
 * this understand: Monaco's JSON language service, and VS Code's, are the same
 * implementation.
 *
 * # What it deliberately does not say
 *
 * A `oneof` is written as ordinary optional properties rather than as
 * `oneOf: [{required: [a]}, {required: [b]}]`. The strict form is expressible
 * and it is worse to work in: completion inside it offers the union of every
 * branch with a diagnostic under whichever one is not finished yet, so typing
 * the second field of a reference is an error until the first is deleted. The
 * server refuses two anyway, and it says which two.
 *
 * Field names are the JSON ones -- `dateUpdated` and not `date_updated`.
 * Protobuf JSON accepts both, and offering both doubles every completion list
 * with a spelling nothing here emits.
 *
 * @module
 */

import { ScalarType, type DescField, type DescMessage } from '@bufbuild/protobuf'

/** Schema is a JSON Schema document, as far as anything here cares. */
export type Schema = Record<string, unknown>

/**
 * jsonSchemaOf is the schema for `desc` and everything it reaches.
 *
 * Everything it reaches is in `definitions` and referred to by `$ref`, which is
 * not tidiness: an entity's fields reach other entities and often reach back,
 * so a schema written inline would not terminate.
 */
export function jsonSchemaOf(desc: DescMessage): Schema {
	const defs: Record<string, Schema> = {}
	const at = (v: DescMessage): string => v.typeName

	const walk = (v: DescMessage): void => {
		if (at(v) in defs) return

		// Written before it is filled in, so that a message reaching itself
		// finds something already there rather than recurring.
		const out: Schema = { type: 'object', additionalProperties: false }
		defs[at(v)] = out

		const props: Record<string, Schema> = {}
		for (const f of v.fields) props[f.jsonName] = ofField(f, walk)

		out.properties = props
		if (v.oneofs.length > 0) {
			out.description = v.oneofs.map((o) => `${o.name}: one of ${o.fields.map((f) => f.jsonName).join(', ')}`).join('; ')
		}
	}

	walk(desc)

	return {
		$schema: 'http://json-schema.org/draft-07/schema#',
		$ref: `#/definitions/${at(desc)}`,
		definitions: defs,
	}
}

function ofField(f: DescField, walk: (v: DescMessage) => void): Schema {
	const one = (): Schema => {
		switch (f.fieldKind === 'list' ? f.listKind : f.fieldKind === 'map' ? f.mapKind : f.fieldKind) {
			case 'message':
				return ofMessage(f.message as DescMessage, walk)

			case 'enum': {
				// The names and not the numbers. Protobuf JSON takes either,
				// and a list of numbers is a completion nobody can read -- the
				// point of an enum is that it has names.
				const e = f.enum
				if (e === undefined) return {}

				return { enum: e.values.map((v) => v.name) }
			}

			default:
				return ofScalar((f.fieldKind === 'list' ? f.scalar : f.fieldKind === 'map' ? f.scalar : f.scalar) as ScalarType)
		}
	}

	if (f.fieldKind === 'list') return { type: 'array', items: one() }
	if (f.fieldKind === 'map') return { type: 'object', additionalProperties: one() }

	return one()
}

/**
 * ofMessage is a nested message, and the well-known ones are not `$ref`s.
 *
 * A `Timestamp` in protobuf JSON is an RFC 3339 string and not an object with a
 * `seconds` and a `nanos`, so a schema that pointed at its descriptor would
 * flag every correct document and complete the wrong fields.
 */
function ofMessage(m: DescMessage, walk: (v: DescMessage) => void): Schema {
	const known = wellKnown[m.typeName]
	if (known !== undefined) return known

	walk(m)

	return { $ref: `#/definitions/${m.typeName}` }
}

/**
 * The 64-bit ones are strings **or** numbers, which is not hedging: protobuf
 * JSON writes them as strings, because a `uint64` does not survive a JavaScript
 * number, and accepts both on the way in. Saying only `string` would put a
 * diagnostic under a document somebody typed by hand and is right.
 */
function ofScalar(t: ScalarType): Schema {
	switch (t) {
		case ScalarType.BOOL:
			return { type: 'boolean' }

		case ScalarType.STRING:
			return { type: 'string' }

		case ScalarType.BYTES:
			return { type: 'string', description: 'base64' }

		case ScalarType.INT64:
		case ScalarType.UINT64:
		case ScalarType.SINT64:
		case ScalarType.FIXED64:
		case ScalarType.SFIXED64:
			return { type: ['string', 'number'] }

		default:
			return { type: 'number' }
	}
}

/**
 * The well-known types that are not objects in JSON.
 *
 * Only the ones payday's own schemas reach plus the wrappers, because a list
 * that guessed at the rest would be a list nobody has checked -- an entry that
 * is wrong here is a red squiggle under a correct document, which is worse than
 * no schema for that field at all.
 */
const wellKnown: Record<string, Schema> = {
	'google.protobuf.Timestamp': { type: 'string', format: 'date-time' },
	'google.protobuf.Duration': { type: 'string', description: 'seconds, as `1.5s`' },
	'google.protobuf.FieldMask': { type: 'string', description: 'comma-separated paths' },
	'google.protobuf.Struct': { type: 'object' },
	'google.protobuf.ListValue': { type: 'array' },
	'google.protobuf.Value': {},
	'google.protobuf.Any': { type: 'object' },
	'google.protobuf.Empty': { type: 'object' },
	'google.protobuf.BoolValue': { type: 'boolean' },
	'google.protobuf.StringValue': { type: 'string' },
	'google.protobuf.BytesValue': { type: 'string', description: 'base64' },
	'google.protobuf.DoubleValue': { type: 'number' },
	'google.protobuf.FloatValue': { type: 'number' },
	'google.protobuf.Int32Value': { type: 'number' },
	'google.protobuf.UInt32Value': { type: 'number' },
	'google.protobuf.Int64Value': { type: ['string', 'number'] },
	'google.protobuf.UInt64Value': { type: ['string', 'number'] },
}
