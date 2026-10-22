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
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import 'fake-indexeddb/auto'

import { pdid } from '@lesomnus/payday'
import { Queries } from '@lesomnus/payday/query'
import { Store, type Disk } from '@lesomnus/payday/store'
import { deleteDisk, deleteOtherDisks, openDisk } from '@lesomnus/payday/store/idb'

import { entities, Robot, Tenant } from '../gen/entities.js'
import { RobotSchema, type Robot as RobotMsg } from '../gen/app/robot_pb.js'
import { RobotService, RobotListResponseSchema } from '../gen/app/robot_svc_pb.js'
import { TenantSchema } from '../gen/app/payday/tenant_pb.js'
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

		// What a page that signs out does: forget and close, with nothing
		// awaited between them. The close is awaited only to know when to look.
		void a.forget()
		await a.close()

		const b = await opened()
		expect(b.row(Robot.typeName, v.id)).toBeUndefined()
		expect(b.all(Tenant.typeName)).toEqual([])
		await b.close()
	})
})

describe('a mirror is closed after what it was told', () => {
	it('keeps a row written just before it closed', async () => {
		const a = await opened()
		const v = robot('arm-01', new Date('2026-01-01T00:00:00Z'))
		a.put(Robot.typeName, v)
		await a.close()

		const b = await opened()
		expect(b.get<RobotMsg>(Robot.typeName, v.id)?.alias).toBe('arm-01')
		await b.close()
	})

	it('is not held open by rows that keep arriving', async () => {
		const a = await opened()
		const closed = a.close()

		// A `Watch` that has not stopped yet, still feeding the store.
		const late = robot('arm-02', new Date('2026-01-01T00:00:00Z'))
		a.put(Robot.typeName, late)
		await within(closed)

		expect(a.get<RobotMsg>(Robot.typeName, late.id)?.alias).toBe('arm-02')
		const b = await opened()
		expect(b.row(Robot.typeName, late.id)).toBeUndefined()
		await b.close()
	})

	it('answers the same however often it is asked', async () => {
		const a = await opened()
		expect(a.close()).toBe(a.close())
		await a.close()
	})
})

describe('a forget that did not reach the mirror', () => {
	/** refusing is a mirror that cannot be cleared, and says whether it was closed. */
	function refusing(): Disk & { closed: boolean } {
		return {
			closed: false,
			async load() {
				return { rows: [], blobs: [] }
			},
			async save() {},
			async clear() {
				throw new Error('cleared nothing')
			},
			close() {
				this.closed = true
			},
		}
	}

	it('says so, and so does the close, which closes the mirror all the same', async () => {
		const disk = refusing()
		const store = Store.open(entities, { ...at, disk })
		store.put(Robot.typeName, robot('arm-01', new Date('2026-01-01T00:00:00Z')))

		const forgot = store.forget()
		const closed = store.close()

		await expect(forgot).rejects.toThrow('cleared nothing')
		await expect(closed).rejects.toThrow('cleared nothing')
		expect(disk.closed).toBe(true)

		// And the store forgot whatever its mirror did.
		expect(store.all(Robot.typeName)).toEqual([])
	})

	it('is a forget that went well, if the next one does', async () => {
		let refuse = true
		const disk: Disk = {
			async load() {
				return { rows: [], blobs: [] }
			},
			async save() {},
			async clear() {
				if (refuse) throw new Error('cleared nothing')
			},
			close() {},
		}
		const store = Store.open(entities, { ...at, disk })

		await expect(store.forget()).rejects.toThrow('cleared nothing')
		refuse = false
		await store.forget()
		await store.close()
	})

	it('is not a row that did not reach it, which is for flushed to say', async () => {
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
		store.put(Robot.typeName, robot('arm-01', new Date('2026-01-01T00:00:00Z')))

		await expect(store.flushed()).rejects.toThrow('quota')
		await store.close()
	})

	it('is refused once the mirror is closed, rather than answered as done', async () => {
		const a = await opened()
		a.put(Robot.typeName, robot('arm-01', new Date('2026-01-01T00:00:00Z')))
		await a.close()

		await expect(a.forget()).rejects.toThrow(/closed before it was told to forget/)
	})

	it('is no failure when another page deleted the mirror, which leaves nothing of theirs', async () => {
		const a = await opened()
		a.put(Robot.typeName, robot('arm-01', new Date('2026-01-01T00:00:00Z')))
		await a.flushed()

		await within(deleteDisk(at))

		await a.forget()
		await a.close()
	})

	it('is one when another page moved the mirror on, which still holds it', async () => {
		const a = await opened()
		a.put(Robot.typeName, robot('arm-01', new Date('2026-01-01T00:00:00Z')))
		await a.flushed()

		// A later deploy's page, opening the same caller's mirror at a later
		// version: this page lets go of it, and the rows stay where that page
		// holds them.
		const later = await held(`payday/${at.name}:${at.identity}`, 3)
		try {
			await expect(a.forget()).rejects.toThrow(/let go/)
			await expect(a.close()).rejects.toThrow(/let go/)
		} finally {
			later.close()
		}
	})
})

/** within is `p`, or a rejection saying it waited -- for what must not hang. */
function within<T>(p: Promise<T>, ms = 2_000): Promise<T> {
	return Promise.race([
		p,
		new Promise<never>((_, no) => setTimeout(() => no(new Error(`it waited ${ms}ms`)), ms)),
	])
}

/** held is a database opened the way a page written before this let go of nothing opened it. */
function held(name: string, version: number): Promise<IDBDatabase> {
	return new Promise((ok, no) => {
		const r = indexedDB.open(name, version)
		r.onsuccess = () => ok(r.result)
		r.onerror = () => no(r.error)
	})
}

describe('a mirror another page holds', () => {
	it('is let go when another page deletes it, rather than holding that up', async () => {
		const a = await opened()
		const v = robot('arm-01', new Date('2026-01-01T00:00:00Z'))
		a.put(Robot.typeName, v)
		await a.flushed()

		// `a` is still open: a tab on this store, and another page wiping it.
		await within(deleteDisk(at))

		// The store goes on in memory, and what needs the disk says why it
		// cannot have it.
		expect(a.row(Robot.typeName, v.id)).toBeDefined()
		await expect(a.writes.add({ id: 'x', method: 'app.RobotService/Add', input: new Uint8Array(), at: 0 })).rejects.toThrow(
			/let go/,
		)
	})

	it('is refused, rather than waited on, while an older page holds it at an older version', async () => {
		const old = await held(`payday/${at.name}:${at.identity}`, 1)

		await expect(within(openDisk(entities, at))).rejects.toThrow(/another page holds/)
		old.close()

		// And once it has let go, it opens.
		const b = await opened()
		b.close()
	})
})

describe('a mirror nobody will open again', () => {
	/** names is every database this origin has. */
	async function names(): Promise<(string | undefined)[]> {
		return (await indexedDB.databases()).map((v) => v.name)
	}

	it('goes once somebody else is using the app', async () => {
		const theirs = { name: at.name, identity: `${at.identity}-other` }
		const another = { name: `${at.name}-other`, identity: at.identity }
		const prefixed = { name: `${at.name}:x`, identity: at.identity }

		for (const o of [at, theirs, another, prefixed]) {
			const s = await opened(o)
			s.put(Robot.typeName, robot('arm-01', new Date('2026-01-01T00:00:00Z')))
			await s.flushed()
			s.close()
		}

		await deleteOtherDisks(at)

		const left = await names()
		expect(left).toContain(`payday/${at.name}:${at.identity}`)
		expect(left).not.toContain(`payday/${theirs.name}:${theirs.identity}`)

		// Other apps', including one whose name begins with this one's.
		expect(left).toContain(`payday/${another.name}:${another.identity}`)
		expect(left).toContain(`payday/${prefixed.name}:${prefixed.identity}`)

		for (const o of [another, prefixed]) await deleteDisk(o)
	})

	it('does not wait on a page that will not let go', async () => {
		const theirs = { name: at.name, identity: `${at.identity}-other` }
		const old = await held(`payday/${theirs.name}:${theirs.identity}`, 2)

		await within(deleteOtherDisks(at))
		old.close()
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
		// which is the part of a mirror that most wants throwing away. Forgotten
		// and closed as a page signing out does it, awaiting nothing between.
		void a.forget()
		await a.close()

		const b = await opened()
		const qb = new Queries(b, answering([], () => new Promise<never>(() => {})), entities)
		expect(qb.get(RobotService.method.list, req, { watch: false }).state).toBe('pending')
		b.close()
	})
})

describe('the mirror does not grow forever', () => {
	const day = 24 * 60 * 60 * 1000

	beforeEach(() => {
		// Only the clock. Faking timers as well would stop `fake-indexeddb`,
		// which is asynchronous by the same mechanism a real one is.
		vi.useFakeTimers({ toFake: ['Date'] })
		vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
	})

	afterEach(() => {
		vi.useRealTimers()
	})

	/** kept is how many entries the mirror is holding, read past the store. */
	async function kept(o: typeof at): Promise<number> {
		const disk = await openDisk(entities, o, { keep: Infinity })
		const held = await disk.load()
		const n = [...held.rows].length + [...held.blobs].length
		disk.close()

		return n
	}

	it('drops what has not been written for a while, and keeps what has', async () => {
		const a = Store.open(entities, { ...at, disk: await openDisk(entities, at, { keep: 7 * day }) })
		a.put(Robot.typeName, robot('old', new Date('2026-01-01T00:00:00Z')))
		await a.flushed()
		a.close()

		vi.setSystemTime(new Date('2026-01-06T00:00:00Z'))

		const b = Store.open(entities, { ...at, disk: await openDisk(entities, at, { keep: 7 * day }) })
		b.put(Robot.typeName, robot('new', new Date('2026-01-06T00:00:00Z')))
		await b.flushed()
		b.close()

		// Nine days after the first was written and four after the second.
		vi.setSystemTime(new Date('2026-01-10T00:00:00Z'))

		const c = Store.open(entities, { ...at, disk: await openDisk(entities, at, { keep: 7 * day }) })
		await c.hydrate()

		const aliases = c.all(Robot.typeName).map((v) => v['alias'])
		expect(aliases).toEqual(['new'])
		c.close()

		// And it is gone from the disk, not merely skipped on the way in --
		// otherwise the mirror still grows and only the reading is bounded.
		// One robot and the tenant it carried.
		expect(await kept(at)).toBe(2)
	})

	it('counts from when this last wrote it, not from what the server stamped', async () => {
		// A row the server has not changed since 2020 and this fetched today is
		// current. Expiring by `dateUpdated` would throw away exactly the rows
		// that never change.
		const a = Store.open(entities, { ...at, disk: await openDisk(entities, at, { keep: 7 * day }) })
		a.put(Robot.typeName, robot('ancient', new Date('2020-01-01T00:00:00Z')))
		await a.flushed()
		a.close()

		vi.setSystemTime(new Date('2026-01-03T00:00:00Z'))

		const b = Store.open(entities, { ...at, disk: await openDisk(entities, at, { keep: 7 * day }) })
		await b.hydrate()
		expect(b.all(Robot.typeName)).toHaveLength(1)
		b.close()
	})

	it('keeps everything when told to', async () => {
		const a = Store.open(entities, { ...at, disk: await openDisk(entities, at, { keep: Infinity }) })
		a.put(Robot.typeName, robot('forever', new Date('2026-01-01T00:00:00Z')))
		await a.flushed()
		a.close()

		vi.setSystemTime(new Date('2030-01-01T00:00:00Z'))

		const b = Store.open(entities, { ...at, disk: await openDisk(entities, at, { keep: Infinity }) })
		await b.hydrate()
		expect(b.all(Robot.typeName)).toHaveLength(1)
		b.close()
	})
})
