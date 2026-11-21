import { create } from '@bufbuild/protobuf'
import { Code, ConnectError, type Transport } from '@connectrpc/connect'
import { describe, expect, it } from 'vitest'
import { ResourceService } from '../../gen/payday/resource_pb.js'
import { SandboxLink } from './link.js'

type AbortSignal = NonNullable<Parameters<Transport['unary']>[1]>
declare const AbortController: { new (): { signal: AbortSignal; abort(): void } }
declare const Headers: { new (): Awaited<ReturnType<Transport['unary']>>['header'] }

const method = ResourceService.method.watch
const unaryMethod = { ...method, methodKind: 'unary' as const }
const input = async function* () { yield {} }
const unary = (link: SandboxLink, signal?: AbortSignal) => link.transport.unary(unaryMethod, signal, undefined, undefined, {})
const stream = (link: SandboxLink, signal?: AbortSignal) => link.transport.stream(method, signal, undefined, undefined, input())
const never = () => new Promise<never>(() => {})

function fixture(before: (signal: AbortSignal | undefined) => Promise<unknown> = async () => {}) {
	let calls = 0
	let last: AbortSignal | undefined
	const inner: Transport = {
		async unary(method, signal) {
			calls++; last = signal
			await before(signal)
			return { stream: false, service: method.parent, method, header: new Headers(), trailer: new Headers(), message: create(method.output) }
		},
		async stream(method, signal) {
			calls++; last = signal
			await before(signal)
			return {
				stream: true, service: method.parent, method, header: new Headers(), trailer: new Headers(),
				message: (async function* () { yield create(method.output); await never() })(),
			}
		},
	}
	return { link: new SandboxLink(inner), calls: () => calls, signal: () => last }
}

describe('sandbox reachability', () => {
	it('keeps one transport, rejects offline calls and reconnects new calls', async () => {
		const f = fixture()
		const transport = f.link.transport
		await unary(f.link)
		f.link.reachable = false
		await expect(unary(f.link)).rejects.toMatchObject({ code: Code.Unavailable })
		await expect(stream(f.link)).rejects.toMatchObject({ code: Code.Unavailable })
		expect(f.calls()).toBe(1)
		f.link.reachable = true
		expect(f.link.transport).toBe(transport)
		await unary(f.link)
		expect(f.calls()).toBe(2)
	})

	it('interrupts in-flight unary calls even when a transport ignores abort', async () => {
		const f = fixture(never)
		const pending = unary(f.link)
		const refused = expect(pending).rejects.toMatchObject({ code: Code.Unavailable })
		f.link.reachable = false
		f.link.reachable = true
		await refused
		expect(f.signal()?.aborted).toBe(true)
	})

	it('reports Unavailable when abort makes the inner transport report Canceled', async () => {
		const f = fixture(signal => new Promise((_, reject) => {
			signal?.addEventListener('abort', () => reject(new ConnectError('aborted', Code.Canceled)))
		}))
		const pending = unary(f.link)
		const refused = expect(pending).rejects.toMatchObject({ code: Code.Unavailable })
		f.link.reachable = false
		await refused
	})

	it('interrupts a stream awaiting headers or awaiting its next message', async () => {
		const opening = fixture(never)
		const pending = stream(opening.link)
		const refused = expect(pending).rejects.toMatchObject({ code: Code.Unavailable })
		opening.link.reachable = false
		await refused

		const f = fixture()
		const response = await stream(f.link)
		const iterator = response.message[Symbol.asyncIterator]()
		expect((await iterator.next()).done).toBe(false)
		const next = iterator.next()
		const ended = expect(next).rejects.toMatchObject({ code: Code.Unavailable })
		f.link.reachable = false
		await ended
		expect(f.signal()?.aborted).toBe(true)
		f.link.reachable = true
		expect((await iterator.next()).done).toBe(true)
	})

	it('remembers a disconnect between reads and cancels when the consumer stops', async () => {
		const f = fixture()
		const response = await stream(f.link)
		const iterator = response.message[Symbol.asyncIterator]()
		await iterator.next()
		f.link.reachable = false
		f.link.reachable = true
		await expect(iterator.next()).rejects.toMatchObject({ code: Code.Unavailable })
		const other = await stream(f.link)
		for await (const _ of other.message) break
		expect(f.signal()?.aborted).toBe(true)
	})

	it('preserves caller cancellation and server refusals', async () => {
		const f = fixture(never)
		const stop = new AbortController()
		const pending = unary(f.link, stop.signal)
		const refused = expect(pending).rejects.toMatchObject({ code: Code.Canceled })
		stop.abort()
		await refused
		const denial = new ConnectError('not allowed', Code.PermissionDenied)
		const denied = fixture(async () => { throw denial })
		await expect(unary(denied.link)).rejects.toBe(denial)
	})

	it('stays closed after the server is stopped', async () => {
		const f = fixture()
		f.link.close()
		expect(f.link.reachable).toBe(false)
		expect(() => { f.link.reachable = true }).toThrow('sandbox is closed')
		await expect(unary(f.link)).rejects.toMatchObject({ code: Code.Unavailable })
		expect(f.calls()).toBe(0)
	})
})
