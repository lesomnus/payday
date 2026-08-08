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
import { TenantService } from '../gen/payday/tenant_svc_pb.js'
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

async function add(alias: string): Promise<RobotMsg> {
	const c = createClient(RobotService, transport)
	const t = createClient(TenantService, transport)
	const tenant = await t.get({ ref: { key: { case: 'alias', value: 'acme' } } })

	return c.add({ tenant: { key: { case: 'id', value: tenant.id } }, alias })
}

describe('a query names the rows it drew', () => {
	it('answers, and puts what it carried in the store', async () => {
		const made = await add(`q-${pdid.newId(RobotDomain)}`.slice(0, 24))

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
		const made = await add(`q2-${pdid.newId(RobotDomain)}`.slice(0, 24))
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
		const made = await add(`q3-${pdid.newId(RobotDomain)}`.slice(0, 24))

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
