/**
 * The whole server, in the page.
 *
 * A payday app compiled `GOOS=js GOARCH=wasm` is the same server the process
 * runs -- the same generated services, the same stack, the same wall from the
 * same schema. Two things differ and both are one line in the app's `wasm/`
 * entry point: the database is SQLite in a Worker rather than a file, and calls
 * arrive over a message port rather than HTTP/2.
 *
 * A reload is a fresh server: new instance, new database, nothing left over.
 * Somebody working on the front end starts no backend, migrates nothing, and
 * does not have to remember what state they left it in.
 *
 * # Why this answers a Transport and not a client
 *
 * Because payday cannot name the app's services. Every payday app has a hand-
 * written `client.ts` that binds its own services onto one `Transport`, and
 * that file is what knows them -- so the sandbox and the real server differ by
 * the argument and by nothing above it:
 *
 *     const box = await start({ worker: new URL('./sandbox-worker.ts', import.meta.url) })
 *     const c = app(box.transport)
 *
 * Code that only ever ran against a fake is code that has never run. This is
 * the same code, with a different transport under it.
 *
 * # What has to be true of the page
 *
 * Four things, and none of them is payday's to fix. `pd sandbox init` writes
 * what it can and `pd doctor` checks all four, because each fails in a way
 * that does not name its cause.
 *
 *   - **`Cross-Origin-Opener-Policy: same-origin` and
 *     `Cross-Origin-Embedder-Policy: require-corp`.** SQLite in a Worker
 *     cancels work with a `SharedArrayBuffer`, which does not exist without
 *     cross-origin isolation. The symptom is "it works on the other dev
 *     server".
 *   - **`@lesomnus/grpc-dgram` is not pre-bundled.** The worker URL it builds
 *     resolves relative to the module, and pre-bundling moves the module into
 *     `.vite/deps/` where there is no worker to resolve to. The symptom is
 *     "the worker itself failed", which does not mention bundling:
 *
 *         optimizeDeps: { exclude: ['@lesomnus/grpc-dgram'] },
 *
 *   - **`wasm_exec.js` is the toolchain's.** It is the JS half of the Go
 *     runtime and is version-coupled to the compiler that built the module, so
 *     a vendored copy pins the wrong one:
 *
 *         cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" ./public/
 *
 *   - **The worker is the app's**, and it has to be. See [start].
 *
 * @module
 */

import type { Transport } from '@connectrpc/connect'
import { createDrpcTransport } from '@lesomnus/grpc-dgram/transport/connect'
import { open, type WasmApp, type WasmSock } from '@lesomnus/grpc-dgram/wasm'

// The two globals this module needs from a page, declared rather than by
// putting `DOM` in `lib` -- which would make every browser global compile in a
// package that also runs in a worker and in Node. `store/idb.ts` does the same
// with IndexedDB, and the list is also the documentation of how little is being
// asked for: a way to resolve a relative URL against the document.
interface URL {
	readonly href: string
}

declare const URL: {
	new (url: string | URL, base?: string | URL): URL
}

declare const location: { readonly href: string }

// And the four a progress report needs, for the same reason and written the
// same way. What is being asked for is the module as a stream of bytes, which
// is the only way to know how far along it is.
interface Res {
	readonly ok: boolean
	readonly status: number
	readonly statusText: string
	readonly headers: { get(name: string): string | null }
	readonly body: { getReader(): { read(): Promise<{ done: boolean; value?: Uint8Array }> } } | null
	text(): Promise<string>
}

declare const fetch: (url: string) => Promise<Res>

declare const Response: new (body: unknown, init?: { headers?: Record<string, string> }) => Res

declare const ReadableStream: new (src: {
	start(c: { enqueue(v: Uint8Array): void; close(): void; error(e: unknown): void }): Promise<void>
}) => unknown

// Typed as answering what `open` takes, which is all this asks of it -- and
// which keeps the name `WebAssembly.Module` out of a package compiled without
// a DOM.
declare const WebAssembly: { compileStreaming(src: Res): Promise<WasmApp> }

/** Sandbox is the running instance, and the transport that reaches it. */
export interface Sandbox {
	/**
	 * What an app's `client.ts` takes. It is a Connect `Transport` like any
	 * other -- there is no cast and nothing above it knows which one it got.
	 */
	readonly transport: Transport

	/** The wasm instance, for a page that wants to take it down. */
	readonly sock: WasmSock

	/** close stops the server. A reload does the same thing more thoroughly. */
	close(): void
}

/** Opts is where the build landed and which worker runs it. */
export interface Opts {
	/**
	 * The module, as the page serves it.
	 *
	 *     GOOS=js GOARCH=wasm go build -o ts/public/app.wasm ./wasm
	 */
	url?: string

	/**
	 * The worker the program runs in, which is the app's file and cannot be
	 * this package's.
	 *
	 * Two imports must land in the **same realm** and neither package can put
	 * the other there: `sqlite3-wasm-go` installs the global the Go driver
	 * looks for, and `@lesomnus/grpc-dgram` runs the module that looks for it.
	 * Importing the driver from the page installs it where the program cannot
	 * see it, and what you get is the instance exiting with
	 *
	 *     sqlite3-wasm: globalThis["sqlite3-wasm-go"] is not installed
	 *
	 * which names the problem exactly and does not say that the answer is two
	 * lines in a file of the app's own. `pd sandbox init` writes that file:
	 *
	 *     // ts/src/sandbox-worker.ts
	 *     import 'sqlite3-wasm-go'
	 *     import '@lesomnus/grpc-dgram/wasm/worker'
	 *
	 * It is required rather than defaulted because a default would resolve
	 * against **this** module's URL, which is inside `node_modules` -- so the
	 * app's worker would not be found and the failure would be about a file
	 * nobody wrote. It is also the app's choice: OPFS instead of memory, or no
	 * SQLite at all.
	 */
	worker: URL | string

	/**
	 * Told how much of the module has arrived, so a page can say so.
	 *
	 * A payday app compiled to wasm is tens of megabytes -- the one in this
	 * repository is 72 -- and on a cold cache that is a long time in which a
	 * page that only says "starting" is indistinguishable from a page that has
	 * hung. This is what it takes to say which.
	 *
	 * `total` is 0 when the length is not knowable, and the two cases are worth
	 * telling apart rather than guessing at:
	 *
	 *   - the response has no `content-length`, which is what a chunked
	 *     response is;
	 *   - it has one and a `content-encoding`, in which case the header counts
	 *     the compressed bytes while the stream yields decompressed ones, so a
	 *     ratio of the two would sail past 100% and settle somewhere absurd.
	 *
	 * So a bar drawn from this needs an indeterminate state, and a page that
	 * would rather show bytes than a bar needs nothing.
	 *
	 * The last call is `(total, total)`, and it is not the end of the wait:
	 * compiling what arrived is the rest, and it has no progress to report.
	 * Saying "compiling" there is the honest thing for a page to do.
	 */
	onProgress?: (loaded: number, total: number) => void
}

/**
 * start compiles the app into the page and answers with a transport for it.
 *
 * It resolves once the instance has published its entry point, so a call made
 * on the transport it answers with reaches a server that is up.
 */
export async function start(opts: Opts): Promise<Sandbox> {
	const url = opts.url ?? '/app.wasm'

	// Fetched here only when somebody is watching. Handed a URL, `open` fetches
	// it itself and does it better -- one call, and the browser's own cache
	// revalidation -- so the counting version is what a caller asked for rather
	// than what everyone gets.
	const app = opts.onProgress === undefined ? url : await counted(url, opts.onProgress)

	const sock = await open(app, {
		// Against the document rather than this module: what the app passed is
		// its own file, and a bundler rewrites where that lands.
		workerUrl: new URL(opts.worker, location.href),
	})

	return {
		// No cast. `createDrpcTransport` answers a Connect `Transport` and an
		// app's `client.ts` takes one, which is the whole reason the sandbox and
		// a real server differ by this line and nothing above it.
		//
		// It used to need `as unknown as`, and that was a **linking** artifact
		// rather than a disagreement about types: a linked package brings its
		// own `node_modules`, and two copies of `@connectrpc/connect` are two
		// nominally different `Transport`s. Published, with the peer dependency
		// it declares, there is one copy.
		transport: createDrpcTransport(sock.dial()),
		sock,
		close: () => sock.close(),
	}
}

/**
 * counted fetches the module and says how far along it is.
 *
 * It compiles from the stream rather than from the bytes it collected, which is
 * the point of doing it this way at all: `compileStreaming` compiles what has
 * arrived while the rest is still arriving, so watching the download costs
 * nothing but the counter. Reading it into an array first and compiling that
 * would trade the overlap for a number.
 *
 * What comes back is a compiled module and not the bytes, and that matters on
 * the way to the worker: `open` posts this, and a `WebAssembly.Module` is
 * structured-cloned by sharing the compiled code, where a 72MB `ArrayBuffer`
 * is copied.
 */
async function counted(url: string, onProgress: (loaded: number, total: number) => void): Promise<WasmApp> {
	const res = await fetch(url)
	if (!res.ok) {
		const body = await res.text().catch(() => '')
		throw new Error(`sandbox: GET ${url} failed: ${res.status} ${res.statusText}${body === '' ? '' : `\n${body}`}`)
	}

	const body = res.body
	if (body === null) throw new Error(`sandbox: GET ${url} answered with no body`)

	// Unknown rather than wrong. See `Opts.onProgress`: an encoded response
	// counts its compressed bytes in the header and its decompressed ones in
	// the stream, so the header is only a total when nothing re-encoded it.
	const total = res.headers.get('content-encoding') === null ? Number(res.headers.get('content-length') ?? 0) : 0

	let seen = 0
	const counting = new ReadableStream({
		async start(c) {
			const r = body.getReader()
			try {
				for (;;) {
					const { done, value } = await r.read()
					if (done) break
					if (value === undefined) continue

					seen += value.byteLength
					onProgress(seen, total)
					c.enqueue(value)
				}
				c.close()
			} catch (e) {
				c.error(e)
			}
		},
	})

	// The mime type is restated because `compileStreaming` insists on it and
	// this is a new response over the same bytes: the one that carried it is
	// the one being read from.
	return WebAssembly.compileStreaming(new Response(counting, { headers: { 'content-type': 'application/wasm' } }))
}
