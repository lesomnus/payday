/**
 * The mirror, over this app's own generated declarations.
 *
 * # About the database under it
 *
 * `fake-indexeddb` is the IndexedDB here, and it is an implementation of the
 * specification rather than a stub that agrees with whatever this file
 * believes. What it proves is the part that can be wrong in the code: the key
 * shape, the schema stamp, that a batch is one transaction, that a delete is a
 * delete, that a row survives being written and read back as the message it
 * came from.
 *
 * What it cannot prove is the browser: quota, a private window that refuses to
 * open a database, two tabs on one origin. Those are true things and none of
 * them is a reason to leave the logic untested.
 */

import { create } from '@bufbuild/protobuf'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import type { Transport } from '@connectrpc/connect'
import { beforeEach, describe, expect, it } from 'vitest'

import 'fake-indexeddb/auto'

import { pdid } from '@lesomnus/payday'
import { Queries } from '@lesomnus/payday/query'
import { Store, type Disk } from '@lesomnus/payday/store'
import { deleteDisk, openDisk } from '@lesomnus/payday/store/idb'

import { entities, Robot, Tenant } from '../gen/entities.js'
import { RobotSchema, type Robot as RobotMsg } from '../gen/app/robot_pb.js'
import { RobotService, RobotListResponseSchema } from '../gen/app/robot_svc_pb.js'
import { TenantSchema } from '../gen/payday/tenant_pb.js'
import { RobotDomain, TenantDomain } from '../gen/domains.js'

let at: { name: string; identity: string }
let n = 0

beforeEach(async () => {
	at = { name: 'idb', identity: `t${n++}` }
	await deleteDisk(at)
})

/** opened is a store with its mirror, filled from whatever is on disk. */
async function opened(o = at, es: readonly (typeof entities)[number][] = entities): Promise<Store> {
	const store = Store.open(es, { ...o, disk: await openDisk(es, o) })
	await store.hydrate()

	return store
}

/** a robot, with a tenant nested the way the server sends one. */
function robot(alias: string, at: Date, id?: Uint8Array): RobotMsg {
	return create(RobotSchema, {
		id: id ?? pdid.newId(RobotDomain).bytes,
		alias,
		dateUpdated: timestampFromDate(at),
		tenant: create(TenantSchema, { id: pdid.newId(TenantDomain).bytes, alias: 'acme' }),
	})
}

describe('a reload draws what the last one had', () => {
	it('comes back with the rows, as the messages they were', async () => {
		const a = await opened()
		const v = robot('arm-01', new Date('2026-01-01T00:00:00Z'))
		a.put(Robot.typeName, v)
		await a.flushed()
		a.close()

		// A second store over the same name and the same caller, which is what
		// a page that was reloaded is.
		const b = await opened()

		const got = b.get<RobotMsg>(Robot.typeName, v.id)
		expect(got?.alias).toBe('arm-01')

		// Read back as a message and not as whatever the database happened to
		// hold: the timestamp is a `Timestamp` again, and the identifier is
		// bytes again.
		expect(got?.dateUpdated?.seconds).toBe(v.dateUpdated?.seconds)
		expect(got?.id).toEqual(v.id)

		// And the neighbour is its own row, the way it was in memory -- the
		// mirror copies the store rather than the response.
		expect(b.all(Tenant.typeName)).toHaveLength(1)
		expect(b.row(Robot.typeName, v.id)?.['tenantId']).toBeTypeOf('string')

		b.close()
	})

	it('does not undo something the page already read', async () => {
		const a = await opened()
		const v = robot('arm-01', new Date('2026-01-01T00:00:00Z'))
		a.put(Robot.typeName, v)
		await a.flushed()
		a.close()

		// The page comes back and reads the server before the mirror has been
		// waited on -- which is the ordinary race, since one is a disk and the
		// other is a request that was already in flight.
		const b = Store.open(entities, { ...at, disk: await openDisk(entities, at) })
		b.put(Robot.typeName, robot('arm-02', new Date('2026-06-01T00:00:00Z'), v.id))
		await b.hydrate()

		// The older copy loses, by the same rule two answers from the server
		// are ordered by. Hydrating late is safe, and that is why.
		expect(b.get<RobotMsg>(Robot.typeName, v.id)?.alias).toBe('arm-02')
		b.close()
	})

	it('forgets a removal too', async () => {
		const a = await opened()
		const v = robot('arm-01', new Date('2026-01-01T00:00:00Z'))
		a.put(Robot.typeName, v)
		await a.flushed()

		a.apply(Robot.typeName, [{ id: v.id }])
		await a.flushed()
		a.close()

		const b = await opened()
		expect(b.row(Robot.typeName, v.id)).toBeUndefined()
		b.close()
	})
})

describe('the mirror is that caller and that schema', () => {
	it('is a different mirror for a different credential', async () => {
		const a = await opened()
		const v = robot('arm-01', new Date('2026-01-01T00:00:00Z'))
		a.put(Robot.typeName, v)
		await a.flushed()
		a.close()

		// The same app, the same browser, another credential. Nothing here
		// leaked -- the server sent those rows to that caller -- and a screen
		// drawn from them would still be showing the wrong person's world.
		const other = { name: at.name, identity: 'somebody-else' }
		await deleteDisk(other)
		const b = await opened(other)

		expect(b.row(Robot.typeName, v.id)).toBeUndefined()
		b.close()
	})

	it('throws the mirror away when the schema moved', async () => {
		const a = await opened()
		const v = robot('arm-01', new Date('2026-01-01T00:00:00Z'))
		a.put(Robot.typeName, v)
		await a.flushed()
		a.close()

		// A deploy in which the entity is not what it was. The rows on disk are
		// still readable and they no longer mean what reading them would
		// conclude, so they go -- rather than being drawn as a page that is
		// wrong with nothing having failed.
		const moved = entities.map((e) =>
			e.typeName !== Robot.typeName
				? e
				: { ...e, schema: { ...e.schema, fields: e.schema.fields.slice(1) } },
		) as unknown as typeof entities

		const b = await opened(at, moved)
		expect(b.row(Robot.typeName, v.id)).toBeUndefined()
		b.close()

		// And it really was thrown away rather than shadowed: opening again
		// with the original schema finds nothing either.
		const c = await opened()
		expect(c.row(Robot.typeName, v.id)).toBeUndefined()
		c.close()
	})

	it('goes when the caller logs out', async () => {
		const a = await opened()
		const v = robot('arm-01', new Date('2026-01-01T00:00:00Z'))
		a.put(Robot.typeName, v)
		await a.flushed()

		a.forget()
		await a.flushed()
		a.close()

		const b = await opened()
		expect(b.row(Robot.typeName, v.id)).toBeUndefined()
		b.close()
	})
})

describe('writing out stays behind the reads', () => {
	it('costs one transaction for a batch, however many rows it carried', async () => {
		const writes: number[] = []
		const disk: Disk = {
			async load() {
				return { rows: [], blobs: [] }
			},
			async save(changes) {
				writes.push([...changes.rows].length)
			},
			async clear() {},
			close() {},
		}

		const store = Store.open(entities, { ...at, disk })

		// Three robots, each carrying a tenant of its own: six rows, one call.
		store.put(
			Robot.typeName,
			robot('a', new Date('2026-01-01T00:00:00Z')),
			robot('b', new Date('2026-01-01T00:00:00Z')),
			robot('c', new Date('2026-01-01T00:00:00Z')),
		)
		await store.flushed()

		expect(writes).toEqual([6])
	})

	it('says so when the disk refused, and goes on being right', async () => {
		const disk: Disk = {
			async load() {
				return { rows: [], blobs: [] }
			},
			async save() {
				throw new Error('quota')
			},
			async clear() {},
			close() {},
		}

		const store = Store.open(entities, { ...at, disk })
		const v = robot('arm-01', new Date('2026-01-01T00:00:00Z'))
		store.put(Robot.typeName, v)

		// The write did not throw and the row is there: memory is the truth,
		// and a mirror that failed costs the mirror.
		expect(store.get<RobotMsg>(Robot.typeName, v.id)?.alias).toBe('arm-01')

		await expect(store.flushed()).rejects.toThrow('quota')
	})
})

describe('a reloaded page draws its list on the first frame', () => {
	/** answering is a server that says what it is given, once and then never. */
	function answering(items: RobotMsg[], after?: () => Promise<never>): Transport {
		return {
			async unary(method: { name: string }) {
				if (after !== undefined) return after()

				return {
					stream: false,
					service: RobotService,
					method,
					header: new Headers(),
					message: create(RobotListResponseSchema, { items }),
					trailer: new Headers(),
				}
			},
			async *stream() {
				throw new Error('no stream')
			},
		} as never
	}

	const req = { filters: [] }

	it('answers from the mirror before the server has said anything', async () => {
		const v = robot('arm-01', new Date('2026-01-01T00:00:00Z'))

		const a = await opened()
		const qa = new Queries(a, answering([v]), entities)
		const entry = qa.get(RobotService.method.list, req, { watch: false })
		await new Promise<void>((ok) => qa.subscribe(entry.key, () => ok()))
		await a.flushed()
		a.close()

		// The page comes back, and the server is not answering -- which is what
		// a cold load over a slow connection is, and the moment the whole thing
		// is for.
		const b = await opened()
		const qb = new Queries(b, answering([], () => new Promise<never>(() => {})), entities)

		const got = qb.get(RobotService.method.list, req, { watch: false })
		expect(got.state).toBe('ok')
		expect(got.data?.items).toHaveLength(1)
		expect(got.data?.items[0]?.alias).toBe('arm-01')

		// Read through the store, as everything is: the list is an order and a
		// membership, and the row it names is the one copy.
		expect(b.row(Robot.typeName, v.id)).toBeDefined()
		b.close()
	})

	it('lets the read behind it correct what was drawn', async () => {
		const v = robot('arm-01', new Date('2026-01-01T00:00:00Z'))

		const a = await opened()
		const qa = new Queries(a, answering([v]), entities)
		const first = qa.get(RobotService.method.list, req, { watch: false })
		await new Promise<void>((ok) => qa.subscribe(first.key, () => ok()))
		await a.flushed()
		a.close()

		// While the tab was closed somebody erased it. The mirror still names
		// it, so it is drawn -- and then it is not.
		const b = await opened()
		const qb = new Queries(b, answering([]), entities)

		const got = qb.get(RobotService.method.list, req, { watch: false })
		expect(got.data?.items).toHaveLength(1)

		await new Promise<void>((ok) => qb.subscribe(got.key, () => ok()))
		expect(qb.get(RobotService.method.list, req, { watch: false }).data?.items).toHaveLength(0)
		b.close()
	})

	it('drops the answers when the caller logs out', async () => {
		const v = robot('arm-01', new Date('2026-01-01T00:00:00Z'))

		const a = await opened()
		const qa = new Queries(a, answering([v]), entities)
		const entry = qa.get(RobotService.method.list, req, { watch: false })
		await new Promise<void>((ok) => qa.subscribe(entry.key, () => ok()))
		await a.flushed()

		// An answer is a list of identifiers this caller was allowed to see,
		// which is the part of a mirror that most wants throwing away.
		a.forget()
		await a.flushed()
		a.close()

		const b = await opened()
		const qb = new Queries(b, answering([], () => new Promise<never>(() => {})), entities)
		expect(qb.get(RobotService.method.list, req, { watch: false }).state).toBe('pending')
		b.close()
	})
})
