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

// And what keeping the module between visits needs, for the same reason and
// written the same way. The list is also the documentation of how little is
// being asked for.
interface Res {
	readonly ok: boolean
	readonly status: number
	readonly statusText: string
	readonly headers: { get(name: string): string | null }
	readonly body: { getReader(): { read(): Promise<{ done: boolean; value?: Uint8Array }> } } | null
	clone(): Res
	text(): Promise<string>
}

declare const fetch: (url: string, init?: { cache?: string; headers?: Record<string, string> }) => Promise<Res>

declare const Response: new (body: unknown, init?: { headers?: Record<string, string> }) => Res

declare const ReadableStream: new (src: {
	start(c: { enqueue(v: Uint8Array): void; close(): void; error(e: unknown): void }): Promise<void>
}) => unknown

// Typed as answering what `open` takes, which is all this asks of it -- and
// which keeps the name `WebAssembly.Module` out of a package compiled without
// a DOM.
declare const WebAssembly: { compileStreaming(src: Res): Promise<WasmApp> }

interface Store {
	match(url: string): Promise<Res | undefined>
	put(url: string, res: Res): Promise<void>
}

// Declared as possibly absent because it is: `caches` exists in a secure
// context only. See [Opts.cache].
declare const caches: { open(name: string): Promise<Store> } | undefined

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
	 * repository is 72 -- and even from the last visit that is a wait in which
	 * a page saying only "starting" is indistinguishable from one that has
	 * hung. This is what it takes to say which.
	 *
	 * [Load.from] is why it is not just a number. A page that draws the same
	 * bar for both tells somebody their module is downloading again when it is
	 * being read off their own disk, which is the question this option
	 * otherwise invites.
	 */
	onProgress?: (v: Load) => void

	/**
	 * Where to keep the module between visits, or `false` not to.
	 *
	 * The browser's own HTTP cache does not do this job, and the reason is the
	 * size: a cache backend drops any single entry over a fraction of the whole
	 * cache, so a module this big is never stored and every reload is the
	 * entire download again. The server is not doing anything wrong when this
	 * happens -- it answers `304` to a conditional request all day, and the
	 * browser never asks one, because it has nothing to ask about.
	 *
	 * The Cache API is subject to the origin's storage quota instead, which is
	 * gigabytes. So the module is kept there and the freshness question is
	 * asked by hand: the stored response's `ETag` goes out as `If-None-Match`
	 * -- or its `Last-Modified` as `If-Modified-Since` -- and a `304` means the
	 * stored one is still the server's answer. Rebuild the module and the
	 * validator changes and it is fetched again. **A stale sandbox is not a
	 * thing this can leave you with.**
	 *
	 * It is a **secure context** feature, so it is there on `https://` and on
	 * `localhost`, and it is not there on `http://192.168.x.x:5173` -- which is
	 * what a container's dev server looks like when it is reached from the host
	 * by address rather than through a forwarded port. Without it this falls
	 * back to fetching every time, which is what it did before, and says so
	 * through [Load.from].
	 */
	cache?: string | false
}

/** Load is how far the module has got, and where it is coming from. */
export interface Load {
	/** Bytes so far. */
	loaded: number

	/**
	 * How many there are, or 0 when that is not knowable -- which is a state a
	 * bar needs rather than a number to substitute for:
	 *
	 *   - a response with no `content-length`, which is what a chunked one is;
	 *   - one with a `content-length` **and** a `content-encoding`, where the
	 *     header counts compressed bytes and the stream yields decompressed
	 *     ones, so a ratio of the two sails past 100%.
	 */
	total: number

	/**
	 * `cache` when these bytes are the ones kept from the last visit, and the
	 * server has said they are still current.
	 *
	 * Worth saying out loud on the page. Reading 72MB off a disk moves a bar
	 * exactly like downloading it does, and somebody watching that a second
	 * time will conclude the caching is broken.
	 */
	from: 'network' | 'cache'

	/**
	 * Bytes per second so far, or 0 before there is anything to divide by.
	 *
	 * Averaged over the whole transfer rather than sampled, because what a page
	 * does with it is print it: an instantaneous rate over a stream that
	 * arrives in chunks jitters by a factor of ten between two frames, and a
	 * number nobody can read is worse than no number.
	 *
	 * It is here rather than left to the page because every page would work it
	 * out the same way and get the jitter wrong the same way -- and because the
	 * clock has to start at the **first byte**, which is the one thing a
	 * caller cannot see from the outside.
	 */
	rate: number

	/**
	 * Whether this module is being kept for the next visit.
	 *
	 * False means the next reload does all of this again, and there is one
	 * ordinary reason for it: [Opts.cache] needs a secure context, so a dev
	 * server reached at `http://192.168.x.x:5173` -- a container's, from the
	 * host, by address rather than through a forwarded port -- has nowhere to
	 * keep anything.
	 *
	 * Which is worth a line on the page. Nothing about a download that repeats
	 * says the address it was asked for is the reason.
	 */
	keeping: boolean
}

/**
 * start compiles the app into the page and answers with a transport for it.
 *
 * It resolves once the instance has published its entry point, so a call made
 * on the transport it answers with reaches a server that is up.
 */
export async function start(opts: Opts): Promise<Sandbox> {
	const app = await load(opts)

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
 * load answers with the module, from wherever it is.
 *
 * Nothing here happens for a caller that asked for neither progress nor a
 * cache: a URL handed to `open` is fetched by `open`, which is one fewer thing
 * between the page and the compiler.
 */
async function load(opts: Opts): Promise<WasmApp | string> {
	const url = opts.url ?? '/app.wasm'
	const store = opts.cache === false ? undefined : await opened(opts.cache ?? 'payday-sandbox')
	if (store === undefined && opts.onProgress === undefined) return url

	const kept = store === undefined ? undefined : await store.match(url).catch(() => undefined)

	const res = await fetch(url, {
		// The browser's own cache is not in play. It will not store an entry
		// this size -- see [Opts.cache] -- so asking it to try buys a copy and
		// no hit, and asking it *not* to keeps the two caches from disagreeing
		// about which answer is current.
		cache: 'no-store',
		headers: asking(kept),
	})

	// The stored one is still the server's answer. Nothing came over the wire
	// but this line of headers.
	if (res.status === 304 && kept !== undefined) return compile(kept, opts.onProgress, 'cache', keeper(store, url))

	if (!res.ok) {
		const body = await res.text().catch(() => '')
		throw new Error(`sandbox: GET ${url} failed: ${res.status} ${res.statusText}${body === '' ? '' : `\n${body}`}`)
	}

	return compile(res, opts.onProgress, 'network', keeper(store, url))
}

/** keeper is how a fetched module is put away, if there is anywhere to put it. */
function keeper(store: Store | undefined, url: string): ((v: Res) => Promise<void>) | undefined {
	return store === undefined ? undefined : (v) => store.put(url, v)
}

/** opened is the cache, or nothing where there is no such thing. */
async function opened(name: string): Promise<Store | undefined> {
	// Absent outside a secure context, and `caches.open` can still refuse --
	// a browser told to allow no site data has the name and not the storage.
	// Neither is an error here: it is the difference between a fast reload and
	// a slow one.
	if (typeof caches === 'undefined') return undefined

	return caches.open(name).catch(() => undefined)
}

/**
 * asking is the conditional request for what is already held, if anything.
 *
 * `ETag` first because it is exact; `Last-Modified` is the fallback for a
 * server that does not send one, and it is only good to the second -- which is
 * a rebuild landing inside the same second reading as unchanged. A dev server
 * sends an `ETag`, so this is the path that is almost never taken.
 */
function asking(kept: Res | undefined): Record<string, string> {
	if (kept === undefined) return {}

	const tag = kept.headers.get('etag')
	if (tag !== null) return { 'if-none-match': tag }

	const at = kept.headers.get('last-modified')

	return at === null ? {} : { 'if-modified-since': at }
}

/**
 * compile turns a response into the module, counting the bytes on the way past
 * and handing a copy to `keep` if there is one.
 *
 * It compiles from the stream rather than from bytes it collected first, which
 * is the point of doing it by hand at all: `compileStreaming` compiles what has
 * arrived while the rest is still arriving, so watching costs only the counter.
 *
 * What comes back is a compiled module rather than the bytes, and that matters
 * on the way to the worker: `open` posts this, and a `WebAssembly.Module` is
 * structured-cloned by sharing the compiled code where a 72MB `ArrayBuffer` is
 * copied.
 */
async function compile(
	res: Res,
	onProgress: ((v: Load) => void) | undefined,
	from: Load['from'],
	keep: ((v: Res) => Promise<void>) | undefined,
): Promise<WasmApp> {
	const body = res.body
	if (body === null) throw new Error('sandbox: the module arrived with no body')

	// See [Load.total]. An encoded response counts compressed bytes in the
	// header and decompressed ones in the stream, so its header is not a total.
	const total = res.headers.get('content-encoding') === null ? Number(res.headers.get('content-length') ?? 0) : 0

	let seen = 0

	// The first byte and not the request: the wait before it is a round trip
	// and a revalidation, and counting that as transfer time reports a rate
	// that starts at nothing and climbs for the whole download.
	let began = 0

	const counting = new ReadableStream({
		async start(c) {
			const r = body.getReader()
			try {
				for (;;) {
					const { done, value } = await r.read()
					if (done) break
					if (value === undefined) continue

					if (began === 0) began = Date.now()

					seen += value.byteLength
					const ms = Date.now() - began
					onProgress?.({
						loaded: seen,
						total,
						from,
						keeping: keep !== undefined,
						rate: ms === 0 ? 0 : (seen * 1000) / ms,
					})
					c.enqueue(value)
				}
				c.close()
			} catch (e) {
				c.error(e)
			}
		},
	})

	// Only the three a later visit needs: what it is, and the two ways to ask
	// whether it still is. Carrying the rest would be carrying a `date` and a
	// `content-encoding` that describe a transfer that already happened.
	const over = new Response(counting, { headers: carried(res) })

	// Cloned **before** either side reads, and not awaited: storing is for the
	// next visit and this one should not wait on a disk write. A failure here
	// is a slow reload rather than a broken page -- the quota is the usual
	// reason, and there is nothing to do about it from here.
	//
	// Not for a `304`, which is already what is in the store: writing it back
	// would be reading a copy out to write the same copy in.
	if (keep !== undefined && from === 'network') void keep(over.clone()).catch(() => undefined)

	return WebAssembly.compileStreaming(over)
}

/** carried is the headers a stored response has to keep. */
function carried(res: Res): Record<string, string> {
	const out: Record<string, string> = {
		// Restated rather than copied: `compileStreaming` insists on it, and a
		// response read back out of the cache has to satisfy it too.
		'content-type': 'application/wasm',
	}

	for (const name of ['etag', 'last-modified', 'content-length']) {
		const v = res.headers.get(name)
		if (v !== null) out[name] = v
	}

	return out
}
