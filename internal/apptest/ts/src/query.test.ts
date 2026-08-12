/**
 * The query layer, against the actual server.
 *
 * What is worth checking here is the claim the whole layer exists for: a
 * component draws a row, the framework knows it drew that row because the call
 * went through it, and when the row changes everything drawing it is told --
 * with nothing declared anywhere.
 *
 * So the assertions are about **who was told**, not about what a request looks
 * like. Against a mock that would prove nothing: a mock agrees with whatever
 * this file believes, and the two things that can be wrong here are the shape
 * the server actually sends and the moment a subscriber is called.
 */

import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

import { createGrpcTransport } from '@connectrpc/connect-node'
import { createClient, type Transport } from '@connectrpc/connect'
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import { pdid } from '@lesomnus/payday'
import { Queries } from '@lesomnus/payday/query'
import { Store } from '@lesomnus/payday/store'

import { entities, Robot, Tenant } from '../gen/entities.js'
import { RobotService } from '../gen/app/robot_svc_pb.js'
import { TenantService } from '../gen/app/payday/tenant_svc_pb.js'
import { RobotDomain } from '../gen/domains.js'
import type { Robot as RobotMsg } from '../gen/app/robot_pb.js'

const here = dirname(fileURLToPath(import.meta.url))
const repo = resolve(here, '../../../..')

let srv: ChildProcessWithoutNullStreams
let transport: Transport

function started(p: ChildProcessWithoutNullStreams): Promise<string> {
	return new Promise((ok, no) => {
		const t = setTimeout(() => no(new Error('the server printed no address')), 60_000)
		let buf = ''

		p.stdout.on('data', (b: Buffer) => {
			buf += b.toString()
			const i = buf.indexOf('\n')
			if (i < 0) return

			clearTimeout(t)
			ok(buf.slice(0, i).trim())
		})
		p.on('error', no)
		p.stderr.on('data', (b: Buffer) => process.stderr.write(b))
	})
}

beforeAll(async () => {
	srv = spawn('go', ['run', './internal/apptest/testsrv'], { cwd: repo })
	const addr = await started(srv)

	transport = createGrpcTransport({
		baseUrl: `http://${addr}`,
		interceptors: [
			(next) => (req) => {
				req.header.set('authorization', 'Plain @acme/admin')
				return next(req)
			},
		],
	})
}, 90_000)

afterAll(() => {
	srv?.kill()
})

let store: Store
let queries: Queries
let n = 0

beforeEach(() => {
	store = Store.open(entities, { name: 'q', identity: `t${n++}` })
	queries = new Queries(store, transport, entities)
})

/**
 * settled waits for one query to stop being pending, and **keeps drawing it**.
 *
 * The unsubscribe is deliberately not called: a query nobody is drawing rests,
 * which is what makes coming back to a page instant, and a test that let go
 * would be measuring a query that had stopped listening.
 */
function settled(key: string): Promise<void> {
	return new Promise<void>((ok) => {
		queries.subscribe(key, () => ok())
	})
}

/** until waits for something to become true, or gives up loudly. */
async function until(f: () => boolean, ms = 10_000): Promise<void> {
	const end = Date.now() + ms
	while (!f()) {
		if (Date.now() > end) throw new Error('it never happened')
		await new Promise((ok) => setTimeout(ok, 25))
	}
}

async function tenantId(): Promise<Uint8Array> {
	const t = createClient(TenantService, transport)
	const tenant = await t.get({ ref: { key: { case: 'alias', value: 'acme' } } })

	return tenant.id
}

/** add writes past the query layer, which is how "somebody else did it" looks. */
async function add(alias: string): Promise<RobotMsg> {
	const c = createClient(RobotService, transport)

	return c.add({ tenant: { key: { case: 'id', value: await tenantId() } }, alias })
}

/** named is an alias short enough for the column and unique enough for a test. */
function named(prefix: string): string {
	return `${prefix}-${pdid.newId(RobotDomain)}`.slice(0, 24)
}

describe('a query names the rows it drew', () => {
	it('answers, and puts what it carried in the store', async () => {
		const made = await add(named('q'))

		const e = queries.get(RobotService.method.list, {
			filters: [{ ref: { key: { case: 'id', value: made.id } } }],
		})
		expect(e.state).toBe('pending')

		await settled(e.key)

		const got = queries.get(RobotService.method.list, {
			filters: [{ ref: { key: { case: 'id', value: made.id } } }],
		})
		expect(got.state).toBe('ok')
		expect(got.data?.items).toHaveLength(1)
		expect(got.data?.items[0]?.alias).toBe(made.alias)

		// A row, not a reply: the store holds it by identifier, which is what
		// makes it the same row when something else asks for it.
		expect(store.row(Robot.typeName, made.id)).toBeDefined()

		// And the tenant is a row of its own rather than a copy inside the
		// robot. This entity declares `with: ["tenant"]`, so the list buys the
		// join and the answer carries the whole tenant -- which is a second row
		// here, once, however many robots named it.
		expect(store.all(Tenant.typeName)).toHaveLength(1)
		expect(store.row(Robot.typeName, made.id)?.['tenantId']).toBeTypeOf('string')
		expect(store.row(Robot.typeName, made.id)?.['tenant']).toBeUndefined()
	})

	it('asks once for the same question twice', async () => {
		const made = await add(named('q2'))
		const req = { filters: [{ ref: { key: { case: 'id' as const, value: made.id } } }] }

		const a = queries.get(RobotService.method.list, req)

		// Built again, field by field, which is what a component re-rendering
		// does. The same question is the same entry.
		const b = queries.get(RobotService.method.list, {
			filters: [{ ref: { key: { case: 'id' as const, value: made.id } } }],
		})

		expect(b).toBe(a)
	})
})

describe('a row changing re-renders everything drawing it', () => {
	it('tells two queries about one row', { timeout: 30_000 }, async () => {
		const made = await add(named('q3'))

		// Two different questions whose answers both carry this row: the list it
		// is in, and the row itself. On a page these are two components.
		const list = queries.get(RobotService.method.list, {
			filters: [{ ref: { key: { case: 'id', value: made.id } } }],
		})
		const one = queries.get(RobotService.method.get, {
			ref: { key: { case: 'id', value: made.id } },
		})

		await Promise.all([settled(list.key), settled(one.key)])

		let toldList = 0
		let toldOne = 0
		queries.subscribe(list.key, () => toldList++)
		queries.subscribe(one.key, () => toldOne++)

		// Somebody else changes it -- which on a page is another tab, another
		// user, or this app's own write somewhere far from these components.
		const c = createClient(RobotService, transport)
		const now = await c.get({ ref: { key: { case: 'id', value: made.id } } })
		const renamed = `${made.alias}-x`.slice(0, 32)
		await c.patch({
			ref: { key: { case: 'id', value: made.id } },
			alias: renamed,
			dateUpdated: now.dateUpdated,
		})

		// The news comes back over the `Watch` the list opened, which is a round
		// trip. Waiting for the **value** rather than for the first
		// notification: the watch says the current state as soon as it is
		// established, so a test that woke on the first news would be woken by
		// the snapshot and assert into the gap.
		await until(() => store.get<RobotMsg>(Robot.typeName, made.id)?.alias === renamed)

		// Nothing here told either of them about the other. What did is that
		// both answers named the same row, and the change went into one store.
		expect(toldList).toBeGreaterThan(0)
		expect(toldOne).toBeGreaterThan(0)

		expect(store.get<RobotMsg>(Robot.typeName, made.id)?.alias).toBe(renamed)

		// Asked again rather than read off the object from before: an entry is
		// a new object every time it changes, which is what lets a binding tell
		// that it did. Holding the old one is holding the old answer.
		const now2 = queries.get(RobotService.method.list, {
			filters: [{ ref: { key: { case: 'id', value: made.id } } }],
		})
		expect(now2.data?.items[0]?.alias).toBe(renamed)
	})
})

describe('a write lands where everything is reading', () => {
	it('stores what it answered with, without a watch bringing it back', async () => {
		const alias = named('w')
		const made = await queries.call(RobotService.method.add, {
			tenant: { key: { case: 'id', value: await tenantId() } },
			alias,
		})

		// No round trip and no stream: the write's own answer is the row, and
		// it went in on the way past.
		expect(store.get<RobotMsg>(Robot.typeName, made.id)?.alias).toBe(alias)
	})

	it('grows a list somebody is drawing', { timeout: 30_000 }, async () => {
		// **No watch.** Which is the point of the test: what puts the new row in
		// this list is the write, not a stream telling it afterwards.
		const req = { filters: [], size: 100 }
		const list = queries.get(RobotService.method.list, req, { watch: false })
		await settled(list.key)

		let told = 0
		queries.subscribe(list.key, () => told++)

		const before = queries.get(RobotService.method.list, req, { watch: false }).data?.items.length ?? 0

		const alias = named('w2')
		await queries.call(RobotService.method.add, {
			tenant: { key: { case: 'id', value: await tenantId() } },
			alias,
		})

		// The list is read again because the write touched an entity it answers
		// with a set of -- nothing here declared that, and the request the list
		// was made with could not have contained it.
		await until(() => {
			const items = queries.get(RobotService.method.list, req, { watch: false }).data?.items
			return items !== undefined && items.some((v) => v.alias === alias)
		})

		expect(told).toBeGreaterThan(0)
		expect(queries.get(RobotService.method.list, req, { watch: false }).data?.items.length).toBe(before + 1)
	})

	it('does not read again for a write told not to', { timeout: 30_000 }, async () => {
		const req = { filters: [], size: 100 }
		const list = queries.get(RobotService.method.list, req, { watch: false })
		await settled(list.key)

		const before = queries.get(RobotService.method.list, req, { watch: false }).data?.items.length ?? 0

		const alias = named('w3')
		const made = await queries.call(
			RobotService.method.add,
			{ tenant: { key: { case: 'id', value: await tenantId() } }, alias },
			{ revalidate: false },
		)

		// The row is still stored -- absorbing the answer is not the part that
		// was turned off, and a caller writing in a loop still wants it.
		expect(store.row(Robot.typeName, made.id)).toBeDefined()

		// But the list was not asked again, so it answers with what it had.
		expect(queries.get(RobotService.method.list, req, { watch: false }).data?.items.length).toBe(before)
	})

	it('takes an erased row off the screen at once', { timeout: 30_000 }, async () => {
		const made = await add(named('w4'))

		const req = { filters: [{ ref: { key: { case: 'id' as const, value: made.id } } }] }
		const list = queries.get(RobotService.method.list, req, { watch: false })
		await settled(list.key)
		expect(queries.get(RobotService.method.list, req, { watch: false }).data?.items).toHaveLength(1)

		await queries.call(RobotService.method.erase, { key: { case: 'id', value: made.id } })

		// The answer to an `Erase` is empty, so what says which row is gone is
		// the request -- and it says it now rather than a round trip from now.
		expect(store.row(Robot.typeName, made.id)).toBeUndefined()
		expect(queries.get(RobotService.method.list, req, { watch: false }).data?.items).toHaveLength(0)
	})
})

describe('forget is how somebody says the world moved', () => {
	it('reads again, for a change nothing here could have known about', { timeout: 30_000 }, async () => {
		const req = { filters: [], size: 100 }
		const list = queries.get(RobotService.method.list, req, { watch: false })
		await settled(list.key)

		// Written past the query layer and with no watch open, which is the one
		// case the layer genuinely cannot see: another tab, another person, a
		// job on the server.
		const alias = named('f')
		await add(alias)

		const has = (): boolean =>
			queries.get(RobotService.method.list, req, { watch: false }).data?.items.some((v) => v.alias === alias) ??
			false
		expect(has()).toBe(false)

		queries.forget()
		await until(has)
	})
})
