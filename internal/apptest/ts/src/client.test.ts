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

import { createGrpcTransport } from '@connectrpc/connect-node'
import { ConnectError, Code } from '@connectrpc/connect'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { pdid, slug } from '@lesomnus/payday'
import * as pderr from '@lesomnus/payday/pderr'

import { app, type App } from './client.js'
import { RobotDomain, TenantDomain } from '../gen/domains.js'

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
