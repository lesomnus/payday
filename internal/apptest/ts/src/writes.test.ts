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

import { entities, Robot, Tenant } from '../gen/entities.js'
import { CellService, RobotService } from '../gen/app/robot_svc_pb.js'
import { HolderService } from '../gen/app/payday/holder_svc_pb.js'
import { TenantService } from '../gen/app/payday/tenant_svc_pb.js'
import { ThingService } from '../gen/shared/thing_svc_pb.js'
import { CellDomain, RobotDomain, ThingDomain } from '../gen/domains.js'
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

/** flakyOver is `t` with the network this file can take away; see the note at the top. */
function flakyOver(t: Transport): Transport {
	return {
		async unary(method, ...rest) {
			if (offline) throw gone()

			calls.push(method.name)
			const res = await t.unary(method, ...rest)

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

			return t.stream(method, ...rest)
		},
	}
}

const flaky: Transport = {
	unary: (...args) => flakyOver(transport).unary(...args),
	stream: (...args) => flakyOver(transport).stream(...args),
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
async function opened(over: Transport = flaky): Promise<{ store: Store; queries: Queries }> {
	const store = Store.open(entities, { ...at, disk: await openDisk(entities, at) })
	await store.hydrate()

	return { store, queries: new Queries(store, over, entities) }
}

/**
 * Browser is one cookie jar, as a browser has one: every page calling through
 * it carries whoever signed in last, and no `authorization` header.
 */
interface Browser {
	/** What every page of this browser calls through, with the network this file takes away. */
	readonly transport: Transport

	/** signIn makes a session for one holder, and it is the browser's now. */
	signIn(holder: Uint8Array): Promise<void>

	/** which is what `GET /session` answers this browser. */
	which(): Promise<{ identity: string }>

	/** end ends the browser's session at the server, behind the browser's back: what an idle clock does. */
	end(): Promise<void>
}

function browser(): Browser {
	let cookie = ''
	const at = (path: string) => `http://${srv.http}${path}`

	return {
		transport: flakyOver(
			createGrpcTransport({
				baseUrl: `http://${srv.addr}`,
				interceptors: [
					(next) => (req) => {
						if (cookie !== '') req.header.set('cookie', cookie)
						return next(req)
					},
				],
			}),
		),
		async signIn(holder) {
			const res = await fetch(at('/session'), {
				method: 'POST',
				headers: { 'content-type': 'application/json' },
				body: JSON.stringify({ Id: pdid.from(holder).toString() }),
			})
			expect(res.status).toBe(204)

			cookie = res.headers.getSetCookie()[0]?.split(';')[0] ?? ''
			expect(cookie).not.toBe('')
		},
		async which() {
			const res = await fetch(at('/session'), { headers: { cookie } })
			expect(res.status).toBe(200)

			return (await res.json()) as { identity: string }
		},
		async end() {
			const res = await fetch(at('/session'), { method: 'DELETE', headers: { cookie } })
			expect(res.status).toBe(204)
		},
	}
}

/** holder is the identifier of a holder of `acme`, made if it is not there. */
async function holder(alias: string): Promise<Uint8Array> {
	const holders = createClient(HolderService, transport)
	const ref = { key: { case: 'slug' as const, value: { alias, tenant: { key: { case: 'alias' as const, value: 'acme' } } } } }

	try {
		return (await holders.get({ ref })).id
	} catch {
		return (await holders.add({ tenant: await tenantRef(), alias })).id
	}
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

	it('reads the rows an AddMany already made, when its answer was lost', async () => {
		const { store, queries } = await opened()
		const ref = await tenantRef()
		const items = [named('w'), named('w')].map((alias) => ({ id: pdid.newId(RobotDomain).bytes, tenant: ref, alias }))

		lose = true
		await queries.send(RobotService.method.addMany, { items })
		await queries.flush()

		// Refused `AlreadyExists` the second time, and every row read by the
		// identifier this side minted for it.
		expect(calls).toEqual(['AddMany', 'AddMany', 'Get', 'Get'])
		expect(queries.writes()).toEqual([])
		for (const v of items) expect(store.row(Robot.typeName, v.id)?.['alias']).toBe(v.alias)
	})

	it('is not an AddMany a row of which is not there', async () => {
		const ref = await tenantRef()
		const alias = named('w')
		await createClient(RobotService, transport).add({ tenant: ref, alias })

		// Its second row's alias is a robot's that is not this one. The first
		// row is not there, so the call never happened, and the refusal stands.
		const { queries } = await opened()
		await queries.send(RobotService.method.addMany, {
			items: [
				{ id: pdid.newId(RobotDomain).bytes, tenant: ref, alias: named('w') },
				{ id: pdid.newId(RobotDomain).bytes, tenant: ref, alias },
			],
		})
		await queries.flush()

		const [w] = queries.writes()
		expect(w?.state).toBe('refused')
		expect(w?.error?.code).toBe(Code.AlreadyExists)
		expect(calls).toEqual(['AddMany', 'Get'])
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

describe('a write is sent as the caller its store is for', () => {
	/** page is one page signed in by cookie: its store named as the server said, over the browser. */
	async function page(b: Browser): Promise<{ store: Store; queries: Queries }> {
		at = { name: 'writes', identity: (await b.which()).identity }
		await deleteDisk(at)

		return opened(b.transport)
	}

	it('waits while somebody else is signed in, and goes once its own caller is back', async () => {
		const ref = await tenantRef()
		const alice = await holder('admin')
		const bob = await holder('bob')
		const id = pdid.newId(RobotDomain).bytes
		const alias = named('w')

		const b = browser()
		await b.signIn(alice)
		const { queries } = await page(b)

		offline = true
		await queries.send(RobotService.method.add, { id, tenant: ref, alias })

		// Somebody else signs in, in another tab. The browser's one cookie is
		// theirs now, and every page of it sends that.
		await b.signIn(bob)
		offline = false
		await queries.flush()

		// Not sent as them: refused for the store it was made for, and kept.
		expect(queries.writes().map((w) => w.state)).toEqual(['waiting'])
		await expect(robot(id)).rejects.toMatchObject({ code: Code.NotFound })

		// The one who made it signs in again -- another session, and the same
		// store -- and it goes, as them.
		const was = at.identity
		await b.signIn(alice)
		expect((await b.which()).identity).toBe(was)

		await queries.flush()
		expect(queries.writes()).toEqual([])
		expect((await robot(id)).alias).toBe(alias)
	})

	it('waits for a sign-in when its session ended, rather than being refused', async () => {
		const ref = await tenantRef()
		const id = pdid.newId(RobotDomain).bytes

		const b = browser()
		await b.signIn(await holder('admin'))
		const { queries } = await page(b)

		offline = true
		await queries.send(RobotService.method.add, { id, tenant: ref, alias: named('w') })

		// It idles out while the page is offline: the server has forgotten the
		// session, and the browser still carries its cookie.
		await b.end()
		offline = false
		await queries.flush()
		expect(queries.writes().map((w) => w.state)).toEqual(['waiting'])

		await b.signIn(await holder('admin'))
		await queries.flush()
		expect(queries.writes()).toEqual([])
		expect((await robot(id)).id).toEqual(id)
	})

	it('reads nothing into its store under somebody else', async () => {
		const ref = await tenantRef()

		const b = browser()
		await b.signIn(await holder('admin'))
		const { store, queries } = await page(b)

		await b.signIn(await holder('bob'))

		// Bob may read this tenant. Answered, it would be his answer in the
		// store of the one before him.
		const entry = queries.get(TenantService.method.get, { ref }, { watch: false })
		await new Promise<void>((ok) => queries.subscribe(entry.key, () => ok()))

		const got = queries.get(TenantService.method.get, { ref }, { watch: false })
		expect(got.state).toBe('error')
		expect(ConnectError.from(got.error).code).toBe(Code.Unauthenticated)
		expect(store.all(Tenant.typeName)).toEqual([])
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

		// And one row of many, which is the same secret on the same disk.
		await expect(
			queries.send(RobotService.method.addMany, {
				items: [
					{ id: pdid.newId(RobotDomain).bytes, tenant: ref, alias: named('w') },
					{ id: pdid.newId(RobotDomain).bytes, tenant: ref, alias: named('w'), secret: new Uint8Array([1, 2, 3]) },
				],
			}),
		).rejects.toThrow(/secret/)

		expect(queries.writes()).toEqual([])
		expect((await opened()).queries.writes()).toEqual([])
		expect(calls).toEqual([])
	})

	it('is refused as a patch, whatever the patch names', async () => {
		const { queries } = await opened()
		offline = true

		// What an `Apply` writes is a document only the server reads, and it
		// maps every column the entity has -- `secret` among them. This one
		// sets it; one that does not would be refused the same, since telling
		// the two apart here is reading the document as the server does.
		await expect(
			queries.send(RobotService.method.apply, {
				ref: { key: { case: 'id', value: pdid.newId(RobotDomain).bytes } },
				patch: {
					messageType: Robot.typeName,
					delta: {
						entries: [
							{
								scope: {
									case: 'targets',
									value: { selectors: [{ kind: { case: 'key', value: { kind: { case: 'field', value: { name: 'secret' } } } } }] },
								},
								kind: { case: 'assign', value: { value: { kind: { case: 'x', value: new Uint8Array([1, 2, 3]) } } } },
							},
						],
					},
				},
			}),
		).rejects.toThrow(/secret/)
		expect(queries.writes()).toEqual([])
		expect((await opened()).queries.writes()).toEqual([])

		// And a patch to an entity that keeps nothing secret is kept, as any
		// write is.
		const w = await queries.send(ThingService.method.apply, {
			ref: { key: { case: 'id', value: pdid.newId(ThingDomain).bytes } },
			patch: { messageType: 'shared.Thing' },
		})
		expect(w.state).toBe('waiting')
		await queries.dismiss(w.id)
	})

	it('still goes as a call', async () => {
		const ref = await tenantRef()
		const { queries } = await opened()
		const id = pdid.newId(RobotDomain).bytes

		await queries.call(RobotService.method.add, { id, tenant: ref, alias: named('w'), secret: new Uint8Array([1, 2, 3]) })
		expect((await robot(id)).id).toEqual(id)
	})
})
