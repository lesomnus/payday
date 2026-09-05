/**
 * The generated declarations, read the way something generic reads them.
 *
 * The store gets what it needs from `schema` and the four fields beside it, and
 * that is covered by `store.test.ts`. What is covered here is the other half of
 * a declaration's job: getting from an entity to the call that answers about
 * it, which is what anything written for *every* entity has to do -- a devtools
 * panel, a seeding script, a generic admin page.
 */

import type { DescService } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'

import type { EntityDesc } from '@lesomnus/payday/store'

import { entities, Cell, Robot, Thing } from '../gen/entities.js'
import { RobotService, CellService } from '../gen/app/robot_svc_pb.js'
import { ThingService } from '../gen/shared/thing_svc_pb.js'

describe('an entity declaration', () => {
	it('carries the service that answers about it', () => {
		expect(Robot.service).toBe(RobotService)
		expect(Cell.service).toBe(CellService)

		// Including one in another proto package, which is a different file in
		// a different directory -- the case a path rule gets wrong.
		expect(Thing.service).toBe(ThingService)
	})

	it('is the service of that entity and not of its neighbour', () => {
		for (const e of entities) {
			expect(e.service?.typeName, e.typeName).toBe(`${e.typeName}Service`)
		}
	})

	// Which RPCs there are is the service's to say, so nothing declares it
	// twice. `Robot` lists and `Cell` does not, in the same file and the same
	// package, which is what makes this an assertion rather than a tautology.
	it('says which RPCs it has by having them', () => {
		expect(RobotService.method.list).toBeDefined()
		expect(CellService.method).not.toHaveProperty('list')
	})
})

/**
 * listable is what a picker over every entity does, and it is here to pin the
 * shape rather than the answer: read as `EntityDesc`, the concrete types
 * collapse and `service` is a `DescService`, which is what makes one line work
 * for an app of any size.
 */
function listable(vs: readonly EntityDesc[]): DescService[] {
	return vs.flatMap((v) => (v.service?.method.list === undefined ? [] : [v.service]))
}

describe('reading every entity generically', () => {
	it('finds the ones that answer a List', () => {
		const got = listable(entities).map((s) => s.typeName)

		expect(got).toContain('app.RobotService')
		expect(got).toContain('shared.ThingService')
		expect(got).not.toContain('app.CellService')
	})

	it('finds one for every entity that has a List, and no others', () => {
		const want = entities.filter((e) => 'list' in (e.service?.method ?? {})).length

		expect(listable(entities)).toHaveLength(want)
		expect(want).toBeGreaterThan(0)
	})
})
