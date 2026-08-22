/**
 * The client, against the actual server.
 *
 * A mock would agree with whatever this file believes, which is the one thing
 * not worth checking. What is worth knowing is whether the descriptors
 * protobuf-es wrote and the ones protoc-gen-go wrote describe the same thing --
 * and the only way to find that out is to send bytes.
 */

import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

import { create } from '@bufbuild/protobuf'
import { anyPack, anyUnpack } from '@bufbuild/protobuf/wkt'
import { createGrpcTransport } from '@connectrpc/connect-node'
import { ConnectError, Code } from '@connectrpc/connect'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { pdid, slug } from '@lesomnus/payday'
import * as pderr from '@lesomnus/payday/pderr'

import { Store } from '@lesomnus/payday/store'

import { app, type App } from './client.js'
import { entities, Robot, Tenant } from '../gen/entities.js'
import { RobotDomain, JointDomain, TenantDomain, ThingDomain } from '../gen/domains.js'
import { RobotSchema } from '../gen/app/robot_pb.js'
import { RobotAddRequestSchema, JointAddRequestSchema } from '../gen/app/robot_svc_pb.js'

const here = dirname(fileURLToPath(import.meta.url))
const repo = resolve(here, '../../../..')

let srv: ChildProcessWithoutNullStreams
let c: App

/** started answers with the address the server printed, or gives up loudly. */
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

	c = app(
		createGrpcTransport({
			baseUrl: `http://${addr}`,
			// The credential, which `Plain` believes. It is what a test and a
			// sandbox use and is not something to serve where anyone can reach
			// it -- the point here is that a credential rides in metadata at all,
			// and that this end knows how to put one there.
			interceptors: [
				(next) => (req) => {
					req.header.set('authorization', 'Plain @acme/admin')
					return next(req)
				},
			],
		}),
	)
}, 90_000)

afterAll(() => {
	srv?.kill()
})

describe('the generated descriptors describe the same server', () => {
	it('writes a row and reads it back', async () => {
		const tenant = await c.tenant.get({ ref: { key: { case: 'alias', value: 'acme' } } })
		expect(tenant.alias).toBe('acme')

		const made = await c.robot.add({
			tenant: { key: { case: 'id', value: tenant.id } },
			alias: 'arm-01',
		})
		expect(made.alias).toBe('arm-01')

		const got = await c.robot.get({ ref: { key: { case: 'id', value: made.id } } })
		expect(got.alias).toBe('arm-01')
	})

	it('reads an identifier the same way the server wrote it', async () => {
		const tenant = await c.tenant.get({ ref: { key: { case: 'alias', value: 'acme' } } })

		// The bytes the server sent, read by the layer that owes no protobuf
		// and no transport. This is the whole of why layer 0 exists: the rule
		// about what an identifier is written once and kept by both halves.
		const id = pdid.from(tenant.id)
		expect(id.domain).toBe(TenantDomain)
		expect(pdid.domainName(id.domain)).toBe('tenant')
	})

	it('reaches an entity from a package of its own', async () => {
		// The TypeScript half of what docs/guide/packages.md claims about
		// `shared.Thing`: it generates, serves and is reached like any other
		// entity. Generation is the file this imports; this is the row going
		// in and coming back out of the same server every other call here
		// travels to, over the same transport and the same credential.
		//
		// No tenant, because the entity says `global: {}` -- which is the only
		// line of this that reads differently from a `robot.add`, and it is
		// about being global rather than about being shared.
		const made = await c.thing.add({ alias: 'gizmo' })
		expect(made.alias).toBe('gizmo')

		const got = await c.thing.get({ ref: { key: { case: 'id', value: made.id } } })
		expect(got.alias).toBe('gizmo')

		// And the identifier is read by the same layer 0 that reads the app's
		// own, with the domain the shared schema declared: the rule about what
		// an identifier is does not stop at a package boundary either.
		expect(pdid.from(got.id).domain).toBe(ThingDomain)
		expect(pdid.domainName(ThingDomain)).toBe('thing')
	})

	it('mints an identifier this side, and the server keeps it', async () => {
		const tenant = await c.tenant.get({ ref: { key: { case: 'alias', value: 'acme' } } })

		// Which is what makes a batch need no placeholder language: a client
		// that names its own rows can write a batch that refers to itself.
		const mine = pdid.newId(RobotDomain)
		const made = await c.robot.add({
			id: mine.bytes,
			tenant: { key: { case: 'id', value: tenant.id } },
			alias: 'arm-minted',
		})
		expect(pdid.from(made.id).toString()).toBe(mine.toString())
	})

	it('is refused an identifier of the wrong kind, by the server', async () => {
		const tenant = await c.tenant.get({ ref: { key: { case: 'alias', value: 'acme' } } })

		// The domain byte doing its job across the wire: the client made a
		// well-formed identifier of the wrong kind, and the server says which
		// kind it was given and which it wanted -- from the byte alone, without
		// looking anything up.
		const wrong = pdid.newId(TenantDomain)
		await expect(
			c.robot.add({
				id: wrong.bytes,
				tenant: { key: { case: 'id', value: tenant.id } },
				alias: 'arm-wrong',
			}),
		).rejects.toThrow(/names a tenant, and this is a robot/)
	})

	it('is refused a name that is not one, and told which field', async () => {
		const tenant = await c.tenant.get({ ref: { key: { case: 'alias', value: 'acme' } } })

		// The same rule `slug.validate` keeps on this side, kept by the server
		// on the way in -- so a form can check before it sends and get the same
		// answer if it does not.
		expect(() => slug.parseAlias('Not An Alias')).toThrow()

		try {
			await c.robot.add({
				tenant: { key: { case: 'id', value: tenant.id } },
				alias: 'Not An Alias',
			})
			expect.unreachable('a name that is not one was taken')
		} catch (e) {
			expect(e).toBeInstanceOf(ConnectError)
			const err = e as ConnectError
			expect(err.code).toBe(Code.InvalidArgument)

			// And it says which box, which is what `pderr` is for. It arrives
			// as a `google.rpc.BadRequest` in the error details.
			expect(err.message).toMatch(/lowercase/)
		}
	})

	it('walks a server stream', async () => {
		const tenant = await c.tenant.get({ ref: { key: { case: 'alias', value: 'acme' } } })

		const seen: string[] = []
		const stream = c.robot.watch({
			filters: [
				{
					ref: {
						key: {
							case: 'slug',
							value: {
								alias: 'arm-01',
								tenant: { key: { case: 'id', value: tenant.id } },
							},
						},
					},
				},
			],
		})

		for await (const res of stream) {
			for (const item of res.items) {
				if (item.value) seen.push(item.value.alias)
			}
			// The first message is what is already there, which is the reason a
			// client does not have to List and subscribe and race the two.
			break
		}

		expect(seen).toEqual(['arm-01'])
	})
})

describe('a refusal says which box', () => {
	it('carries the field path beside the message and not inside it', async () => {
		const tenant = await c.tenant.get({ ref: { key: { case: 'alias', value: 'acme' } } })

		try {
			await c.robot.add({
				tenant: { key: { case: 'id', value: tenant.id } },
				alias: 'Not An Alias',
			})
			expect.unreachable('a name that is not one was taken')
		} catch (e) {
			// The check a form makes first: everything else -- gone, not
			// allowed, not reachable -- is a different thing to show, and none
			// of it belongs under a box.
			expect(pderr.isInvalid(e)).toBe(true)

			const vs = pderr.violations(e)
			expect(vs).toHaveLength(1)
			expect(vs[0]?.field).toBe('alias')
			expect(vs[0]?.why).toMatch(/lowercase/)

			// And keyed by the box, which is what a form actually wants.
			expect([...pderr.byField(e).keys()]).toEqual(['alias'])

			// The words a page shows are the app's, from the app's own table.
			// payday carries no language and no tone: what a UI matches on is
			// the code and the field path.
			const said = pderr.messages(e, (field) =>
				field === 'alias' ? '이름은 소문자, 숫자, 하이픈만 쓸 수 있습니다' : '입력을 확인해 주세요',
			)
			expect(said.get('alias')).toEqual(['이름은 소문자, 숫자, 하이픈만 쓸 수 있습니다'])
		}
	})

	it('has nothing to place for a refusal that was never about a form', async () => {
		try {
			await c.robot.get({ ref: { key: { case: 'id', value: pdid.newId(RobotDomain).bytes } } })
			expect.unreachable('a row that is not there was found')
		} catch (e) {
			expect(pderr.isInvalid(e)).toBe(false)
			expect(pderr.violations(e)).toEqual([])
		}
	})
})

describe('what the server actually sends', () => {
	it('does not write an empty neighbour into the store', async () => {
		const tenant = await c.tenant.get({ ref: { key: { case: 'alias', value: 'acme' } } })

		const made = await c.robot.add({
			tenant: { key: { case: 'id', value: tenant.id } },
			alias: 'arm-store',
		})

		// What a Get with no Select carries: the tenant as a reference. This is
		// the shape the store has to recognise, and building it by hand in a
		// test is not the same thing -- the server fills the timestamps in.
		const got = await c.robot.get({ ref: { key: { case: 'id', value: made.id } } })

		const store = Store.open(entities, { name: 'wire', identity: 'x' })
		store.put(Robot.typeName, got)

		// The tenant it named was never a whole row here, so nothing about it
		// should have been written -- a nameless Tenant is worse than no
		// Tenant, because a page draws it.
		expect(store.all(Tenant.typeName)).toEqual([])

		// And the reference is kept, which is the other half: the row knows
		// which tenant it is in without holding a copy of it.
		expect(store.row(Robot.typeName, made.id)?.['tenantId']).toBeTypeOf('string')
	})
})

/**
 * A batch, sent from here.
 *
 * The Go tests cover what a batch means -- the transaction, the four rules
 * applied per operation, the trail. What they cannot cover is this end: an `Op`
 * carries a `google.protobuf.Any`, and packing one means protobuf-es writing a
 * type URL that `anypb` in Go resolves to the same message. Nothing else in
 * either half sends an `Any` at all, so that agreement was never exercised by
 * anything, in a service whose entire request type is `Any`.
 *
 * A browser is also where a batch is most useful. An optimistic UI writes
 * several rows and shows them at once, and a transaction is what makes "shows
 * them at once" honest.
 */
describe('a batch, packed here and unpacked there', () => {
	it('writes two rows that refer to each other, with no placeholder language', async () => {
		const tenant = await c.tenant.get({ ref: { key: { case: 'alias', value: 'acme' } } })

		// Both identifiers, minted before either row exists. This is the whole
		// of why payday needs no `$0.id`: the client names its own rows, so the
		// second operation can refer to the first by writing the same bytes.
		const robot = pdid.newId(RobotDomain)
		const joint = pdid.newId(JointDomain)

		const res = await c.batch.do({
			ops: [
				{
					method: '/app.RobotService/Add',
					request: anyPack(
						RobotAddRequestSchema,
						create(RobotAddRequestSchema, {
							id: robot.bytes,
							tenant: { key: { case: 'id', value: tenant.id } },
							alias: 'arm-batched',
						}),
					),
				},
				{
					method: '/app.JointService/Add',
					request: anyPack(
						JointAddRequestSchema,
						create(JointAddRequestSchema, {
							id: joint.bytes,
							robot: { key: { case: 'id', value: robot.bytes } },
							alias: 'elbow-batched',
						}),
					),
				},
			],
		})

		// One answer per operation, in the order they were written, and each is
		// an `Any` this end has to unpack -- which is the same agreement in the
		// other direction.
		expect(res.results).toHaveLength(2)

		const made = anyUnpack(res.results[0]!, RobotSchema)
		expect(made).toBeDefined()
		expect(pdid.from(made!.id).toString()).toBe(robot.toString())

		// And read back, because a response echoes what it was given and a row
		// that was never committed would answer exactly the same way.
		const got = await c.joint.get({ ref: { key: { case: 'id', value: joint.bytes } } })
		expect(got.alias).toBe('elbow-batched')
		expect(pdid.from(got.robot!.id).toString()).toBe(robot.toString())
	})

	it('undoes the first when the second refuses', async () => {
		const tenant = await c.tenant.get({ ref: { key: { case: 'alias', value: 'acme' } } })

		const robot = pdid.newId(RobotDomain)

		await expect(
			c.batch.do({
				ops: [
					{
						method: '/app.RobotService/Add',
						request: anyPack(
							RobotAddRequestSchema,
							create(RobotAddRequestSchema, {
								id: robot.bytes,
								tenant: { key: { case: 'id', value: tenant.id } },
								alias: 'arm-undone',
							}),
						),
					},
					// An identifier of the wrong kind, refused by the minter on
					// the byte alone. Any refusal would do; this one is refused
					// before the database is touched, which is the harder case
					// for a rollback to get right.
					{
						method: '/app.JointService/Add',
						request: anyPack(
							JointAddRequestSchema,
							create(JointAddRequestSchema, {
								id: pdid.newId(RobotDomain).bytes,
								robot: { key: { case: 'id', value: robot.bytes } },
								alias: 'elbow-undone',
							}),
						),
					},
				],
			}),
		).rejects.toThrow(/ops\[1\]/)

		// The first operation is gone, which is the difference between a batch
		// and a loop over the same two calls.
		await expect(
			c.robot.get({ ref: { key: { case: 'id', value: robot.bytes } } }),
		).rejects.toThrow()
	})

	it('is refused an Any that is not what the method takes', async () => {
		const tenant = await c.tenant.get({ ref: { key: { case: 'alias', value: 'acme' } } })

		// A well-formed request for a different RPC. It is refused rather than
		// coerced: a request that decoded into another message would be a write
		// the caller did not ask for, and both halves are only agreeing about
		// the type URL if this fails.
		await expect(
			c.batch.do({
				ops: [
					{
						method: '/app.JointService/Add',
						request: anyPack(
							RobotAddRequestSchema,
							create(RobotAddRequestSchema, {
								tenant: { key: { case: 'id', value: tenant.id } },
								alias: 'arm-mismatched',
							}),
						),
					},
				],
			}),
		).rejects.toThrow(/ops\[0\]/)
	})
})
