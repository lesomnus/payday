import { Code, ConnectError, type Transport } from '@connectrpc/connect'

type Signal = NonNullable<Parameters<Transport['unary']>[1]>
declare const AbortController: {
	new (): { readonly signal: Signal; abort(reason?: unknown): void }
}

const unreachable = () => new ConnectError('sandbox is unreachable', Code.Unavailable)
const canceled = () => new ConnectError('sandbox call canceled', Code.Canceled)

/** One stable transport, with a new set of calls after every reconnection. */
export class SandboxLink {
	readonly transport: Transport
	private online = true
	private closed = false
	private readonly calls = new Set<Flight>()

	constructor(inner: Transport) {
		this.transport = {
			unary: async (method, signal, timeout, headers, input, context) => {
				const call = this.begin(signal)
				try {
					return await call.run(() => inner.unary(method, call.signal, timeout, headers, input, context))
				} finally {
					call.finish()
				}
			},
			stream: async (method, signal, timeout, headers, input, context) => {
				const call = this.begin(signal)
				try {
					const response = await call.run(() => inner.stream(method, call.signal, timeout, headers, input, context))
					return { ...response, message: messages(response.message, call) }
				} catch (err) {
					call.finish()
					throw err
				}
			},
		}
	}

	get reachable(): boolean { return this.online }
	set reachable(value: boolean) {
		if (this.closed && value) throw new ConnectError('sandbox is closed', Code.Unavailable)
		if (this.online === value) return
		this.online = value
		if (!value) for (const call of this.calls) call.abort(unreachable())
	}

	close(): void {
		this.closed = true
		this.reachable = false
	}

	private begin(signal: Signal | undefined): Flight {
		if (!this.online) throw unreachable()
		if (signal?.aborted) throw ConnectError.from(signal.reason, Code.Canceled)
		const call = new Flight(signal, () => this.calls.delete(call))
		this.calls.add(call)
		return call
	}
}

// Race the transport with the disconnect as well as signaling it. Otherwise a
// transport waiting on an idle receive could keep the page waiting forever.
class Flight {
	private readonly controller = new AbortController()
	readonly signal = this.controller.signal
	private readonly stopped: Promise<never>
	private reject!: (reason: unknown) => void
	private failure: ConnectError | undefined
	private readonly cancel = () => this.abort(ConnectError.from(this.parent?.reason, Code.Canceled))

	constructor(private readonly parent: Signal | undefined, private readonly release: () => void) {
		this.stopped = new Promise<never>((_, reject) => { this.reject = reject })
		// A stream can be disconnected between next() calls, when nobody is
		// racing this promise yet. Its next read still observes the failure.
		void this.stopped.catch(() => {})
		parent?.addEventListener('abort', this.cancel, { once: true })
	}

	async run<T>(operation: () => Promise<T>): Promise<T> {
		if (this.failure !== undefined) throw this.failure
		try {
			const value = await Promise.race([operation(), this.stopped])
			if (this.failure !== undefined) throw this.failure
			return value
		} catch (err) {
			// The inner transport may reject with Canceled synchronously when
			// we abort it. The page still needs the disconnect's Unavailable.
			throw this.failure ?? err
		}
	}

	abort(reason: ConnectError): void {
		if (this.failure !== undefined) return
		this.failure = reason
		this.controller.abort(reason)
		this.reject(reason)
		this.finish()
	}

	finish(): void {
		this.parent?.removeEventListener('abort', this.cancel)
		this.release()
	}
}

async function* messages<T>(source: AsyncIterable<T>, call: Flight): AsyncIterable<T> {
	const iterator = source[Symbol.asyncIterator]()
	let complete = false
	try {
		for (;;) {
			const next = await call.run(() => iterator.next())
			if (next.done) { complete = true; return }
			yield next.value
		}
	} finally {
		if (complete) call.finish()
		else call.abort(canceled())
		// An iterator blocked in next() may also block return(). The signal
		// has already canceled its RPC; don't delay reporting Unavailable.
		try { void iterator.return?.().catch(() => {}) } catch { /* cleanup cannot replace the RPC error */ }
	}
}
