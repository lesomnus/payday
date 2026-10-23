/**
 * The registry of this app's event messages, `gen/events.ts`, against the JSON
 * a page is answered in (payday#151).
 *
 * A row of payday's Event keeps its value as an Any, and Connect's JSON names
 * the message an Any holds by its type: `{"@type": "type.googleapis.com/…",
 * …fields}`. protobuf-es reads that only with the type registered -- which is
 * why a page's transport is handed `events`, and why a List of events read
 * without it fails whole, on the first row that holds a value.
 */

import { fromJson, toJson, type JsonObject } from '@bufbuild/protobuf'
import { anyUnpack } from '@bufbuild/protobuf/wkt'
import { describe, expect, it } from 'vitest'

import { EventListResponseSchema } from '../gen/app/payday/event_svc_pb.js'
import { MachineServicedSchema, MachineStartedSchema, Mode } from '../gen/app/machine_pb.js'
import { events, kinds } from '../gen/events.js'

/** A machine's identifier: sixteen bytes, as base64, which is how JSON has bytes. */
const machine = 'BwcHBwcHBwcHBwcHBwcHBw=='

/** An event holding what a service did, as the server writes one. */
const serviced = {
	kind: 'machine.service',
	value: {
		'@type': 'type.googleapis.com/app.MachineServiced',
		machine,
		mode: 'MODE_SERVICE',
		parts: [{ serial: 'P-1', count: 2 }],
		swapped: { left: { serial: 'P-0', count: 1 } },
		due: '2026-11-01T00:00:00Z',
	},
} satisfies JsonObject

/**
 * One holding nothing, which is what a row written before its app had a
 * message for its kind looks like.
 */
const noted = { kind: 'machine.note' } satisfies JsonObject

/** The two, as the server answers a List of them. */
const page = { items: [serviced, noted] } satisfies JsonObject

describe('the event registry', () => {
	it('names each event message by the kind its rows are written under', () => {
		expect(kinds['machine.service']).toBe(MachineServicedSchema)

		// A message that names no kind is written under its full name.
		expect(kinds['app.MachineStarted']).toBe(MachineStartedSchema)
		expect(Object.keys(kinds).sort()).toEqual(['app.MachineStarted', 'correct', 'machine.service', 'past.expire', 'robot.move'])

		for (const v of Object.values(kinds)) {
			expect(events.getMessage(v.typeName)).toBe(v)
		}
	})

	it('reads a List of events whose values are Anys, and a List is not read without it', () => {
		const res = fromJson(EventListResponseSchema, page, { registry: events })
		expect(res.items).toHaveLength(2)

		const v = res.items[0]?.value
		expect(v?.typeUrl).toBe('type.googleapis.com/app.MachineServiced')
		expect(res.items[1]?.value).toBeUndefined()

		// As one kind's message, typed, and as whichever the registry says.
		const m = anyUnpack(v!, kinds['machine.service'])
		expect(m?.machine).toEqual(new Uint8Array(16).fill(7))
		expect(m?.mode).toBe(Mode.SERVICE)
		expect(m?.parts.map((p) => [p.serial, p.count])).toEqual([['P-1', 2]])
		expect(m?.swapped['left']?.serial).toBe('P-0')
		expect(anyUnpack(v!, events)?.$typeName).toBe('app.MachineServiced')

		// And back to what the server sent.
		expect(toJson(EventListResponseSchema, res, { registry: events })).toEqual(page)

		// Without it, nothing of the page is read.
		expect(() => fromJson(EventListResponseSchema, page)).toThrow(/type registry/)

		// Every row of it, the one holding nothing included: the page is lost
		// whole, not the row.
		expect(() => fromJson(EventListResponseSchema, { items: [noted, serviced] })).toThrow(/type registry/)

		// A row with no value needs none.
		expect(fromJson(EventListResponseSchema, { items: [noted] }).items).toHaveLength(1)
	})
})
