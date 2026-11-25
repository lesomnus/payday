import { create, createFileRegistry, type DescMessage } from '@bufbuild/protobuf'
import { FileDescriptorProtoSchema } from '@bufbuild/protobuf/wkt'
import { describe, expect, it } from 'vitest'

import type { EntityDesc } from '../store/index.js'
import { versioned } from './query.js'

// An entity and its select as the generators write them: `Thing` is versioned
// by `date_updated`, `Owner` is not, and `Thing` has an edge to `Owner`.
const file = create(FileDescriptorProtoSchema, {
	name: 't.proto',
	package: 't',
	syntax: 'proto3',
	messageType: [
		{ name: 'Owner', field: [{ name: 'id', number: 1, type: 12, label: 1 }, { name: 'alias', number: 4, type: 9, label: 1 }] },
		{
			name: 'Thing',
			field: [
				{ name: 'id', number: 1, type: 12, label: 1 },
				{ name: 'alias', number: 4, type: 9, label: 1 },
				{ name: 'owner', number: 2, type: 11, typeName: '.t.Owner', label: 1 },
			],
		},
		{ name: 'OwnerSelect', field: [{ name: 'all', number: 1, type: 8, label: 1 }, { name: 'alias', number: 4, type: 8, label: 1 }] },
		{
			name: 'ThingSelect',
			field: [
				{ name: 'all', number: 1, type: 8, label: 1 },
				{ name: 'owner', number: 2, type: 11, typeName: '.t.OwnerSelect', label: 1 },
				{ name: 'alias', number: 4, type: 8, label: 1 },
				{ name: 'date_updated', number: 13, type: 8, label: 1 },
			],
		},
		{
			name: 'ThingListRequest',
			field: [
				{ name: 'size', number: 2, type: 5, label: 1 },
				{ name: 'select', number: 6, type: 11, typeName: '.t.ThingSelect', label: 1 },
			],
		},
	],
})
const registry = createFileRegistry(file, () => undefined)
const msg = (name: string) => registry.getMessage(`t.${name}`) as DescMessage

const entities = new Map<string, EntityDesc>([
	['t.Thing', { typeName: 't.Thing', schema: msg('Thing'), version: 'dateUpdated' } as unknown as EntityDesc],
	['t.Owner', { typeName: 't.Owner', schema: msg('Owner') } as unknown as EntityDesc],
])

const list = msg('ThingListRequest')
const read = (v: unknown) => v as Record<string, Record<string, unknown>>

describe('what the query layer sends for the store', () => {
	it('asks for the version of an entity its select reads', () => {
		const req = create(list, { size: 3, select: { alias: true } } as never)
		const sent = read(versioned(list, req, entities))

		expect(sent['select']?.['dateUpdated']).toBe(true)
		expect(sent['select']?.['alias']).toBe(true)
		expect((sent as unknown as { size: number }).size).toBe(3)

		// And the request it was given is as it was: the key of a query is
		// what the caller wrote.
		expect(read(req)['select']?.['dateUpdated']).toBe(false)
	})

	it('at every edge the select fills, and not where there is no version', () => {
		const req = create(list, { select: { alias: true, owner: { alias: true } } } as never)
		const sent = read(versioned(list, req, entities))
		const owner = sent['select']?.['owner'] as Record<string, unknown>

		expect(sent['select']?.['dateUpdated']).toBe(true)
		expect(owner['alias']).toBe(true)
		expect(Object.keys(owner).filter((k) => owner[k] === true)).toEqual(['alias'])
	})

	it('leaves alone a request with no select, and one that asks for all', () => {
		const none = create(list, { size: 1 } as never)
		expect(versioned(list, none, entities)).toBe(none)

		const all = create(list, { select: { all: true } } as never)
		expect(read(versioned(list, all, entities))['select']?.['dateUpdated']).toBe(false)
	})
})
