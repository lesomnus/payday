import 'fake-indexeddb/auto'
import { create, type MessageShape } from '@bufbuild/protobuf'
import { Code, ConnectError, createRouterTransport, type Transport } from '@connectrpc/connect'
import { describe, expect, it } from 'vitest'
import { pdid } from '@lesomnus/payday'
import { Queries } from '@lesomnus/payday/query'
import { DeviceQueue, Store, key } from '@lesomnus/payday/store'
import { deleteOtherDisks, openDisk, openQueue } from '@lesomnus/payday/store/idb'
import { entities, Robot } from '../gen/entities.js'
import { RobotSchema } from '../gen/app/robot_pb.js'
import { RobotService } from '../gen/app/robot_svc_pb.js'
import { RobotDomain } from '../gen/domains.js'

// The server records the wire metadata, while IndexedDB and the query/cache
// code are real. Go tests cover the verifier and generated permission/audit path.
describe('device-owned effects', () => {
	it('replays A after sign-out, mirror sweeping, a reload and B taking over; no A row enters B cache', async () => {
		const at = { name: `device-${pdid.newId(RobotDomain)}`, identity: 'device-credential-scope' }
		let device = new DeviceQueue({ ...at, disk: await openQueue(at) })
		let offline = true
		let lose = true
		const seen: { method: string; subject: string | null; presence: string | null; identity: string | null; recorded: string | null }[] = []
		const rows = new Map<string, MessageShape<typeof RobotSchema>>()
		const router = createRouterTransport((router) => router.service(RobotService, {
			add(req) {
				if (rows.has(key(req.id))) throw new ConnectError('already written', Code.AlreadyExists)
				const row = create(RobotSchema, { id: req.id, alias: req.alias })
				rows.set(key(req.id), row)
				return row
			},
			get(req) {
				const row = rows.get(key(req.ref?.key.case === 'id' ? req.ref.key.value : undefined))
				if (!row) throw new ConnectError('not found', Code.NotFound)
				return row
			},
		}))
		const transport: Transport = {
			async unary(method, signal, timeout, header, input, context) {
				if (offline) throw new ConnectError('offline', Code.Unavailable)
				const h = new Headers(header)
				seen.push({ method: method.name, subject: h.get('payday-subject'), presence: h.get('payday-presence'), identity: h.get('payday-identity'), recorded: h.get('payday-recorded') })
				const result = await router.unary(method, signal, timeout, header, input, context)
				if (lose) { lose = false; throw new ConnectError('response lost', Code.Unavailable) }
				return result
			},
			stream: (...args) => router.stream(...args),
		}
		const a = await device.represent({ reference: 'session-a', subject: 'person-a' })
		const storeA = Store.open(entities, { name: at.name, identity: a.identity, disk: await openDisk(entities, { name: at.name, identity: a.identity }) })
		const queriesA = new Queries(storeA, transport, entities, { representation: a })
		const id = pdid.newId(RobotDomain).bytes
		await queriesA.send(RobotService.method.add, { id, alias: 'a-work' })
		await queriesA.flush()
		queriesA.close()
		await storeA.forget()
		await storeA.close()
		expect(device.writes.all()).toHaveLength(1)
		await expect(queriesA.send(RobotService.method.add, { alias: 'after-signout' })).rejects.toThrow('operator is done')
		device.close()

		device = new DeviceQueue({ ...at, disk: await openQueue(at) })
		await device.hydrate()
		const b = await device.represent({ reference: 'session-b', subject: 'person-b' })
		expect(b.identity).not.toBe(a.identity)
		await deleteOtherDisks({ name: at.name, identity: b.identity })
		const storeB = Store.open(entities, { name: at.name, identity: b.identity })
		const queriesB = new Queries(storeB, transport, entities, { representation: b })
		expect(queriesB.writes()).toHaveLength(0) // A's request is not B's screen data.
		expect(() => new Queries(storeB, transport, entities, { representation: a })).toThrow('Store identity')
		offline = false
		await queriesB.flush() // committed, response lost
		expect(device.writes.all()).toHaveLength(1)
		await queriesB.flush() // AlreadyExists, then Get under the original presence
		expect(device.writes.all()).toHaveLength(0)
		expect(seen.map((v) => v.method)).toEqual(['Add', 'Add', 'Get'])
		for (const v of seen) {
			expect(v.subject).toBe('person-a'); expect(v.presence).toBe('session-a'); expect(v.identity).toBe(at.identity)
			expect(v.recorded).toBe(seen[0]?.recorded)
			expect(Date.parse(v.recorded!)).not.toBeNaN()
		}
		expect(storeB.get(Robot.typeName, key(id))).toBeUndefined()
		const own = pdid.newId(RobotDomain).bytes
		await queriesB.send(RobotService.method.add, { id: own, alias: 'b-work' })
		await queriesB.flush()
		expect(seen.at(-1)?.subject).toBe('person-b')
		expect(storeB.get(Robot.typeName, key(own))).toBeDefined()
		await device.forget(); device.close()
	})
	it('keeps a verifier outage waiting, then refuses ended A work without blocking B', async () => {
		const device = new DeviceQueue({ name: 'denials', identity: 'station' })
		const a = await device.represent({ reference: 'ended', subject: 'person-a' })
		const b = await device.represent({ reference: 'current', subject: 'person-b' })
		let unavailable = true
		const transport = createRouterTransport((router) => router.service(RobotService, {
			add(req, ctx) {
				if (unavailable) throw new ConnectError('verifier down', Code.Unavailable)
				if (ctx.requestHeader.get('payday-presence') === 'ended') throw new ConnectError('late work refused', Code.PermissionDenied)
				return create(RobotSchema, { id: req.id, alias: req.alias })
			},
		}))
		const qa = new Queries(Store.open(entities, { name: 'denials', identity: a.identity }), transport, entities, { representation: a })
		const qb = new Queries(Store.open(entities, { name: 'denials', identity: b.identity }), transport, entities, { representation: b })
		await qa.send(RobotService.method.add, { id: pdid.newId(RobotDomain).bytes, alias: 'a' })
		await qa.flush()
		qa.close()
		await qb.send(RobotService.method.add, { id: pdid.newId(RobotDomain).bytes, alias: 'b' })
		await qb.flush()
		expect(device.writes.all()).toHaveLength(2)
		unavailable = false
		await qb.flush()
		expect(device.writes.all()).toHaveLength(1)
		expect(device.writes.all()[0]?.refused?.code).toBe(Code.PermissionDenied)
		expect(qb.writes()).toHaveLength(0)
		await device.forget()
	})

	it('cannot refill a signed-out cache with an in-flight response', async () => {
		const device = new DeviceQueue({ name: 'closing', identity: 'station' })
		const representation = await device.represent({ reference: 'a', subject: 'person-a' })
		let entered!: () => void
		const started = new Promise<void>((resolve) => { entered = resolve })
		let release!: () => void
		const pending = new Promise<void>((resolve) => { release = resolve })
		const transport = createRouterTransport((router) => router.service(RobotService, {
			async add(req) { entered(); await pending; return create(RobotSchema, { id: req.id, alias: req.alias }) },
		}))
		const store = Store.open(entities, { name: 'closing', identity: representation.identity })
		const queries = new Queries(store, transport, entities, { representation })
		const id = pdid.newId(RobotDomain).bytes
		const response = queries.call(RobotService.method.add, { id, alias: 'a' })
		await started
		queries.close()
		await store.forget()
		release()
		await expect(response).rejects.toMatchObject({ code: Code.Canceled })
		expect(store.get(Robot.typeName, key(id))).toBeUndefined()
	})

})
