/**
 * Writes made without a connection, against the actual server.
 *
 * The network is the one thing replaced: the transport below throws what
 * connect-web throws for a page with no network -- a ConnectError with no code
 * of its own around the `fetch` that failed -- for as long as a test says it is
 * offline, and can lose an answer on the way back once. Everything on the other
 * side of it is the real server, because what can be wrong here is what it
 * says when a write arrives twice.
 *
 * The mirror is `fake-indexeddb`, for the same reason `idb.test.ts` gives.
 */

import { createGrpcTransport } from '@connectrpc/connect-node'
import { Code, ConnectError, createClient, type Transport } from '@connectrpc/connect'
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import 'fake-indexeddb/auto'

import { pdid } from '@lesomnus/payday'
import { violations } from '@lesomnus/payday/pderr'
import { Queries } from '@lesomnus/payday/query'
import { Store } from '@lesomnus/payday/store'
import { deleteDisk, openDisk } from '@lesomnus/payday/store/idb'

import { entities, Robot } from '../gen/entities.js'
import { CellService, RobotService } from '../gen/app/robot_svc_pb.js'
import { TenantService } from '../gen/app/payday/tenant_svc_pb.js'
import { CellDomain, RobotDomain } from '../gen/domains.js'
import { start, type Server } from './testsrv.js'

let srv: Server
let transport: Transport

beforeAll(async () => {
	srv = await start()

	transport = createGrpcTransport({
		baseUrl: `http://${srv.addr}`,
		interceptors: [
			(next) => (req) => {
				req.header.set('authorization', 'Plain @acme/admin')
				return next(req)
			},
		],
	})
}, 90_000)

afterAll(() => {
	srv?.stop()
})

/** What the transport below does to the next call; see the note at the top. */
let offline = false
let lose = false

/** calls is every method the layer sent, by name, in order. */
let calls: string[] = []

/** gone is what connect-web throws when `fetch` itself fails. */
function gone(): ConnectError {
	return new ConnectError('Failed to fetch', Code.Unknown, undefined, undefined, new TypeError('Failed to fetch'))
}

const flaky: Transport = {
	async unary(method, ...rest) {
		if (offline) throw gone()

		calls.push(method.name)
		const res = await transport.unary(method, ...rest)

		// Sent, done, and the answer lost: the row exists and this side was
		// never told.
		if (lose) {
			lose = false
			throw gone()
		}

		return res
	},
	stream(method, ...rest) {
		if (offline) return Promise.reject(gone())

		return transport.stream(method, ...rest)
	},
}

let at: { name: string; identity: string }
let n = 0

beforeEach(async () => {
	offline = false
	lose = false
	calls = []

	at = { name: 'writes', identity: `t${n++}` }
	await deleteDisk(at)
})

/** opened is one page on the store at `at`: its mirror, hydrated, and its queries. */
async function opened(): Promise<{ store: Store; queries: Queries }> {
	const store = Store.open(entities, { ...at, disk: await openDisk(entities, at) })
	await store.hydrate()

	return { store, queries: new Queries(store, flaky, entities) }
}

/** until waits for something to become true, or gives up loudly. */
async function until(f: () => boolean, ms = 10_000): Promise<void> {
	const end = Date.now() + ms
	while (!f()) {
		if (Date.now() > end) throw new Error('it never happened')
		await new Promise((ok) => setTimeout(ok, 25))
	}
}

let tenant: Uint8Array | undefined

async function tenantRef(): Promise<{ key: { case: 'id'; value: Uint8Array } }> {
	if (tenant === undefined) {
		const t = await createClient(TenantService, transport).get({ ref: { key: { case: 'alias', value: 'acme' } } })
		tenant = t.id
	}

	return { key: { case: 'id', value: tenant } }
}

/** named is an alias short enough for the column and unique enough for a test. */
function named(prefix: string): string {
	return `${prefix}-${pdid.newId(RobotDomain)}`.slice(0, 24)
}

/** robot is what the server holds for one identifier, read past every layer here. */
function robot(id: Uint8Array) {
	return createClient(RobotService, transport).get({ ref: { key: { case: 'id', value: id } } })
}

describe('a write made with no connection', () => {
	it('waits, and goes when there is one', async () => {
		const { store, queries } = await opened()
		const ref = await tenantRef()
		const id = pdid.newId(RobotDomain).bytes
		const alias = named('w')

		offline = true
		const w = await queries.send(RobotService.method.add, { id, tenant: ref, alias })
		expect(w.state).toBe('waiting')
		expect(w.name).toBe('app.RobotService/Add')

		// Trying again with nobody there leaves it where it was.
		await queries.flush()
		expect(queries.writes()).toHaveLength(1)

		// And it is a write waiting, not a row: what the row will be is the
		// server's answer, and there has not been one.
		expect(store.row(Robot.typeName, id)).toBeUndefined()

		offline = false
		await queries.flush()

		expect(queries.writes()).toHaveLength(0)
		expect(store.row(Robot.typeName, id)?.['alias']).toBe(alias)
		expect((await robot(id)).alias).toBe(alias)
	})

	it('is sent in the order it was made', async () => {
		const { queries } = await opened()
		const ref = await tenantRef()
		const cell = pdid.newId(CellDomain).bytes
		const id = pdid.newId(RobotDomain).bytes

		// The robot names a cell that exists only once the first write is in.
		// Sent the other way round, the second is refused for naming nothing.
		offline = true
		await queries.send(CellService.method.add, { id: cell, tenant: ref, alias: named('c') })
		await queries.send(RobotService.method.add, {
			id,
			tenant: ref,
			alias: named('w'),
			cell: { key: { case: 'id', value: cell } },
		})
		expect(queries.writes().map((w) => w.name)).toEqual(['app.CellService/Add', 'app.RobotService/Add'])

		offline = false
		await queries.flush()

		expect(queries.writes()).toEqual([])
		expect((await robot(id)).cell?.id).toEqual(cell)
	})

	it('is kept across a reload', async () => {
		const ref = await tenantRef()
		const id = pdid.newId(RobotDomain).bytes
		const alias = named('w')

		offline = true
		{
			const { store, queries } = await opened()
			await queries.send(RobotService.method.add, { id, tenant: ref, alias })
			store.close()
		}

		// A page that was closed, opened again. What it hydrates is a write
		// that names its method and carries its request, read back by
		// descriptors this page has of its own.
		const { queries } = await opened()
		const [w] = queries.writes()
		expect(w?.state).toBe('waiting')
		expect(w?.method?.name).toBe('Add')
		expect((w?.input as { alias?: string } | undefined)?.alias).toBe(alias)

		offline = false
		await queries.flush()

		expect(queries.writes()).toEqual([])
		expect((await robot(id)).alias).toBe(alias)
	})

	it('goes when anything comes back, without being asked', async () => {
		const { queries } = await opened()
		const ref = await tenantRef()
		const id = pdid.newId(RobotDomain).bytes

		offline = true
		await queries.send(RobotService.method.add, { id, tenant: ref, alias: named('w') })

		// A read that answers is a connection that came back.
		offline = false
		queries.get(TenantService.method.get, { ref })

		await until(() => queries.writes().length === 0)
		expect((await robot(id)).id).toEqual(id)
	})
})

describe('a write sent twice is the same write', () => {
	it('reads the row an Add already made, when its answer was lost', async () => {
		const { store, queries } = await opened()
		const ref = await tenantRef()
		const id = pdid.newId(RobotDomain).bytes
		const alias = named('w')

		// The first send reaches the server and its answer does not come back.
		lose = true
		await queries.send(RobotService.method.add, { id, tenant: ref, alias })
		await queries.flush()

		// The second is refused `AlreadyExists`, and the row is read by the
		// identifier this side minted -- which is the answer the first never got.
		expect(calls).toEqual(['Add', 'Add', 'Get'])
		expect(queries.writes()).toEqual([])
		expect(store.row(Robot.typeName, id)?.['alias']).toBe(alias)
	})

	it('is sent once by two pages on one store', async () => {
		const ref = await tenantRef()
		const id = pdid.newId(RobotDomain).bytes

		offline = true
		{
			const { store, queries } = await opened()
			await queries.send(RobotService.method.add, { id, tenant: ref, alias: named('w') })
			store.close()
		}

		// Two tabs, each hydrated with the same write waiting.
		const a = await opened()
		const b = await opened()
		expect(a.queries.writes()).toHaveLength(1)
		expect(b.queries.writes()).toHaveLength(1)

		offline = false
		await Promise.all([a.queries.flush(), b.queries.flush()])

		expect(calls.filter((v) => v === 'Add')).toEqual(['Add'])
		expect(a.queries.writes()).toEqual([])
		expect(b.queries.writes()).toEqual([])
	})
})

describe('a write the server refuses', () => {
	it('is kept, refused the way a call is, until it is let go', async () => {
		const ref = await tenantRef()
		const alias = named('w')
		await createClient(RobotService, transport).add({ tenant: ref, alias })

		// The alias is taken, by a robot that is not this one. `AlreadyExists`
		// again -- and the identifier this side minted names nothing, so this
		// one is a refusal and not a write that already happened.
		const { queries } = await opened()
		await queries.send(RobotService.method.add, { id: pdid.newId(RobotDomain).bytes, tenant: ref, alias })
		await queries.flush()

		const [w] = queries.writes()
		expect(w?.state).toBe('refused')
		expect(w?.error).toBeInstanceOf(ConnectError)
		expect(w?.error?.code).toBe(Code.AlreadyExists)

		// Kept on disk with the rest, so a reload still says it.
		const again = await opened()
		expect(again.queries.writes()[0]?.state).toBe('refused')

		await again.queries.dismiss(w!.id)
		expect(again.queries.writes()).toEqual([])
		expect((await opened()).queries.writes()).toEqual([])
	})

	it('says which field, as a refused call does', async () => {
		const ref = await tenantRef()

		const { queries } = await opened()
		await queries.send(RobotService.method.add, { id: pdid.newId(RobotDomain).bytes, tenant: ref, alias: 'Not A Slug!' })
		await queries.flush()

		const [w] = queries.writes()
		expect(w?.state).toBe('refused')
		expect(violations(w?.error).map((v) => v.field)).toContain('alias')
	})

	it('does not hold up the writes behind it', async () => {
		const ref = await tenantRef()
		const id = pdid.newId(RobotDomain).bytes

		offline = true
		const { queries } = await opened()
		await queries.send(RobotService.method.add, { id: pdid.newId(RobotDomain).bytes, tenant: ref, alias: 'Not A Slug!' })
		await queries.send(RobotService.method.add, { id, tenant: ref, alias: named('w') })

		offline = false
		await queries.flush()

		expect(queries.writes().map((w) => w.state)).toEqual(['refused'])
		expect((await robot(id)).id).toEqual(id)
	})
})

describe('a caller who is done', () => {
	it('takes what they never sent with them', async () => {
		const ref = await tenantRef()

		offline = true
		const { store, queries } = await opened()
		await queries.send(RobotService.method.add, { id: pdid.newId(RobotDomain).bytes, tenant: ref, alias: named('w') })

		let told = 0
		queries.subscribeWrites(() => told++)

		store.forget()
		await store.flushed()

		expect(told).toBeGreaterThan(0)
		expect(queries.writes()).toEqual([])
		expect((await opened()).queries.writes()).toEqual([])
	})

	it('keeps nothing more, and sends nothing another page left behind', async () => {
		const ref = await tenantRef()

		// One page signs out. Another on the same store -- a second tab that
		// has not heard yet -- goes on keeping writes.
		const gone = await opened()
		const other = await opened()
		gone.store.forget()
		await gone.store.flushed()

		await expect(
			gone.queries.send(RobotService.method.add, { id: pdid.newId(RobotDomain).bytes, tenant: ref, alias: named('w') }),
		).rejects.toThrow(/done/)

		offline = true
		await other.queries.send(RobotService.method.add, { id: pdid.newId(RobotDomain).bytes, tenant: ref, alias: named('w') })
		await other.queries.flush()

		// The page that signed out does not know who it would be sending as
		// any more, so what the other page kept is not its to send.
		offline = false
		await gone.queries.flush()
		expect(calls).not.toContain('Add')
		expect(other.queries.writes()).toHaveLength(1)
	})
})

describe('a write that carries a secret', () => {
	it('is refused before anything is kept', async () => {
		const ref = await tenantRef()
		const { queries } = await opened()

		// `secret` is `(payday.field).secret` on Robot: written, and never
		// answered with. Kept, it would wait on disk in the clear.
		offline = true
		await expect(
			queries.send(RobotService.method.add, {
				id: pdid.newId(RobotDomain).bytes,
				tenant: ref,
				alias: named('w'),
				secret: new Uint8Array([1, 2, 3]),
			}),
		).rejects.toThrow(/secret/)
		await expect(
			queries.send(RobotService.method.patch, {
				ref: { key: { case: 'id', value: pdid.newId(RobotDomain).bytes } },
				secret: new Uint8Array([1, 2, 3]),
			}),
		).rejects.toThrow(/secret/)

		expect(queries.writes()).toEqual([])
		expect((await opened()).queries.writes()).toEqual([])
		expect(calls).toEqual([])
	})

	it('still goes as a call', async () => {
		const ref = await tenantRef()
		const { queries } = await opened()
		const id = pdid.newId(RobotDomain).bytes

		await queries.call(RobotService.method.add, { id, tenant: ref, alias: named('w'), secret: new Uint8Array([1, 2, 3]) })
		expect((await robot(id)).id).toEqual(id)
	})
})
