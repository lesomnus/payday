/**
 * The store, over this app's own generated declarations.
 *
 * It is here rather than in payday's package because what is worth testing is
 * the pair: a runtime written once, and declarations a generator wrote from a
 * real schema. A test with a hand-written declaration would be a test of the
 * runtime against a fixture somebody chose to suit it.
 */

import 'fake-indexeddb/auto'

import { create } from '@bufbuild/protobuf'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { beforeEach, describe, expect, it } from 'vitest'

import { pdid } from '@lesomnus/payday'
import { Store } from '@lesomnus/payday/store'

import { entities, Robot, Tenant } from '../gen/entities.js'
import { RobotSchema, type Robot as RobotMsg } from '../gen/app/robot_pb.js'
import { TenantSchema } from '../gen/payday/tenant_pb.js'
import { RobotDomain, TenantDomain } from '../gen/domains.js'

let store: Store
let n = 0

beforeEach(async () => {
	store = Store.open(entities, { name: 'apptest', identity: `t${n++}` })
	await store.db.open()
})

/** a robot, with a tenant nested the way the server sends one. */
function robot(alias: string, at: Date, tenant?: { id: Uint8Array; alias: string }): RobotMsg {
	const t = tenant ?? { id: pdid.newId(TenantDomain).bytes, alias: 'acme' }

	return create(RobotSchema, {
		id: pdid.newId(RobotDomain).bytes,
		alias,
		dateUpdated: timestampFromDate(at),
		tenant: create(TenantSchema, { id: t.id, alias: t.alias }),
	})
}

describe('a store holds rows and not replies', () => {
	it('takes a nested response apart', async () => {
		const v = robot('arm-01', new Date())
		await store.put(Robot.typeName, v)

		// The robot is a row with a key where its tenant was.
		const row = await store.table(Robot.typeName).get(pdid.from(v.id).toString().replaceAll('-', ''))
		expect(row).toBeDefined()
		expect(row?.alias).toBe('arm-01')
		expect(row?.['tenant']).toBeUndefined()
		expect(row?.['tenantId']).toBeTypeOf('string')

		// And the tenant is a row of its own, once, in its own table -- which is
		// what makes a rename land everywhere rather than in one of six copies.
		const tenants = await store.table(Tenant.typeName).toArray()
		expect(tenants).toHaveLength(1)
		expect(tenants[0]?.alias).toBe('acme')
	})

	it('reads a row back as the message it came from', async () => {
		const v = robot('arm-01', new Date())
		await store.put(Robot.typeName, v)

		const got = await store.get<RobotMsg>(Robot.typeName, v.id)
		expect(got?.alias).toBe('arm-01')
		expect(pdid.from(got!.id).toString()).toBe(pdid.from(v.id).toString())

		// The neighbour comes back as a reference and not as a copy: reading it
		// is one lookup by primary key, rather than a join nobody asked for.
		expect(got?.tenant?.id).toBeDefined()
		expect(got?.tenant?.alias).toBe('')
	})

	it('keeps one copy of a row two responses carried', async () => {
		const t = { id: pdid.newId(TenantDomain).bytes, alias: 'acme' }
		await store.put(Robot.typeName, robot('arm-01', new Date(), t), robot('arm-02', new Date(), t))

		expect(await store.table(Tenant.typeName).count()).toBe(1)
		expect(await store.table(Robot.typeName).count()).toBe(2)
	})
})

describe('two answers about one row are put in order', () => {
	it('a stale answer does not overwrite a fresh one', async () => {
		const early = robot('arm-01', new Date('2026-01-01T00:00:00Z'))

		const late = create(RobotSchema, {
			...early,
			alias: 'renamed',
			dateUpdated: timestampFromDate(new Date('2026-01-02T00:00:00Z')),
		})

		// The late one first, and the early one after it -- which is what a
		// snapshot racing an event looks like, and what an outbox draining after
		// a restart looks like.
		await store.put(Robot.typeName, late)
		await store.put(Robot.typeName, early)

		const got = await store.get<RobotMsg>(Robot.typeName, early.id)
		expect(got?.alias).toBe('renamed')
	})

	it('a fresh answer does overwrite a stale one', async () => {
		const early = robot('arm-01', new Date('2026-01-01T00:00:00Z'))
		const late = create(RobotSchema, {
			...early,
			alias: 'renamed',
			dateUpdated: timestampFromDate(new Date('2026-01-02T00:00:00Z')),
		})

		await store.put(Robot.typeName, early)
		await store.put(Robot.typeName, late)

		const got = await store.get<RobotMsg>(Robot.typeName, early.id)
		expect(got?.alias).toBe('renamed')
	})

	it('a partial answer does not erase what it did not carry', async () => {
		const v = robot('arm-01', new Date('2026-01-01T00:00:00Z'))
		await store.put(Robot.typeName, v)

		// What a request that selected only the name looks like coming back.
		const thin = create(RobotSchema, {
			id: v.id,
			alias: 'renamed',
			dateUpdated: timestampFromDate(new Date('2026-01-02T00:00:00Z')),
		})
		await store.put(Robot.typeName, thin)

		const row = await store.table(Robot.typeName).get(pdid.from(v.id).toString().replaceAll('-', ''))
		expect(row?.alias).toBe('renamed')
		expect(row?.['tenantId']).toBeTypeOf('string')
		expect(row?.['tenantId']).not.toBe('')
	})

	it('a reference-only neighbour does not replace the whole one', async () => {
		const t = { id: pdid.newId(TenantDomain).bytes, alias: 'acme' }
		await store.put(Robot.typeName, robot('arm-01', new Date(), t))

		// A second robot whose tenant came back as nothing but its identifier,
		// which is what a request that did not select it looks like. Storing
		// that as if it were the row would leave the tenant nameless.
		const bare = create(RobotSchema, {
			id: pdid.newId(RobotDomain).bytes,
			alias: 'arm-02',
			dateUpdated: timestampFromDate(new Date()),
			tenant: create(TenantSchema, { id: t.id }),
		})
		await store.put(Robot.typeName, bare)

		const tenants = await store.table(Tenant.typeName).toArray()
		expect(tenants).toHaveLength(1)
		expect(tenants[0]?.alias).toBe('acme')
	})
})

describe('a watch keeps it up to date', () => {
	it('writes what an item carries', async () => {
		const v = robot('arm-01', new Date())
		await store.apply(Robot.typeName, [{ id: v.id, value: v }])

		expect((await store.get<RobotMsg>(Robot.typeName, v.id))?.alias).toBe('arm-01')
	})

	it('takes a row away when the value is absent', async () => {
		const v = robot('arm-01', new Date())
		await store.put(Robot.typeName, v)

		// Which is the whole of how a removal is said: the row is named, the
		// value is not there, and the RPC that did it says what happened. There
		// is no tombstone to recognise.
		await store.apply(Robot.typeName, [{ id: v.id }])

		expect(await store.get(Robot.typeName, v.id)).toBeUndefined()
	})
})

describe('a store belongs to whoever it was opened for', () => {
	it('another identity is another database', async () => {
		const v = robot('arm-01', new Date())
		await store.put(Robot.typeName, v)

		const other = Store.open(entities, { name: 'apptest', identity: 'somebody-else' })
		await other.db.open()

		// Nothing leaked -- the server never sent this to them. What is wrong
		// without this is the screen: rows a narrowed credential can no longer
		// read, still drawn.
		expect(await other.get(Robot.typeName, v.id)).toBeUndefined()

		await other.forget()
	})

	it('forgetting throws the copy away', async () => {
		const v = robot('arm-01', new Date())
		await store.put(Robot.typeName, v)
		await store.forget()

		const again = Store.open(entities, { name: 'apptest', identity: store.db.name.split(':')[1]! })
		await again.db.open()
		expect(await again.get(Robot.typeName, v.id)).toBeUndefined()
	})
})

describe('the index comes from the schema', () => {
	it('is the one the server has, so a page cannot be fast on what the server is slow on', () => {
		// `[alias+tenantId]` is the unique index the database is built from,
		// written the way Dexie writes a compound one -- and `tenant` became
		// `tenantId` because an edge is a nested message there and a key here.
		expect(Robot.index).toBe('&id,[dateCreated+id],[alias+tenantId]')

		// And the store can actually use it, which is the half a string
		// comparison does not prove.
		expect(store.table(Robot.typeName).schema.indexes.map((i: { name: string }) => i.name)).toContain('[alias+tenantId]')
	})
})
