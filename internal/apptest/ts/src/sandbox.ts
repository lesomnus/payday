/**
 * The whole app, in the page.
 *
 * A reload is a fresh server: new instance, new database, nothing left over.
 * Somebody working on the front end starts no backend, migrates nothing, and
 * does not have to remember what state they left it in.
 *
 * # What makes this two lines
 *
 * The Go half is the same server the process runs -- the same generated
 * services, the same stack, the same wall from the same schema. Two things
 * differ and both are one line there: the database is SQLite in a Worker rather
 * than a file, and calls arrive over a message port rather than HTTP/2.
 *
 * And this side is two lines because everything above it is transport-blind.
 * `app()` takes a Connect `Transport` and knows nothing else, so the sandbox
 * and the real server are the same code with a different argument -- which is
 * the whole reason a sandbox is worth having. Code that only ever ran against a
 * fake is code that has never run.
 *
 * # Two things will bite whoever serves this
 *
 * Neither is payday's to fix and both fail confusingly.
 *
 *   - **`Cross-Origin-Opener-Policy: same-origin` and
 *     `Cross-Origin-Embedder-Policy: require-corp`.** SQLite in a Worker
 *     cancels work with a `SharedArrayBuffer`, which does not exist without
 *     cross-origin isolation. The symptom is "it works on the other dev
 *     server".
 *   - **`wasm_exec.js` is the toolchain's.** It is the JS half of the Go
 *     runtime and is version-coupled to the compiler that built the module, so
 *     a vendored copy pins the wrong one:
 *
 *         cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" ./public/
 *
 * And one that is not optional at all: **the worker is yours to supply.** See
 * [start].
 *
 * @module
 */

import { createDrpcTransport } from '@lesomnus/grpc-dgram/transport/connect'
import { open, type WasmSock } from '@lesomnus/grpc-dgram/wasm'


import { app, type App } from './client.js'

/** Sandbox is the app, and the instance it is running inside. */
export interface Sandbox {
	readonly app: App

	/** The wasm instance, for a page that wants to take it down. */
	readonly sock: WasmSock

	/** close stops the server. A reload does the same thing more thoroughly. */
	close(): void
}

/**
 * start compiles the app into the page and answers with a client for it.
 *
 * `url` is where the build landed:
 *
 *     GOOS=js GOARCH=wasm go build -o public/app.wasm ./wasm
 *
 * # The worker is yours, and it has to be
 *
 * Two things must be in the **same realm** and neither package can put them
 * there for the other. `sqlite3-wasm-go` installs the global the Go driver
 * looks for; `@lesomnus/grpc-dgram` runs the module that looks for it.
 * Importing the driver on the main thread installs it in the wrong realm, and
 * what you get is the instance exiting with
 *
 *     sqlite3-wasm: globalThis["sqlite3-wasm-go"] is not installed
 *
 * which names the problem exactly and does not say that the answer is two lines
 * in a file of your own:
 *
 *     // sandbox-worker.ts
 *     import 'sqlite3-wasm-go'
 *     import '@lesomnus/grpc-dgram/wasm/worker'
 *
 * It defaults to the one beside this file, so an app that took the template as
 * it comes says nothing. It is a parameter because the driver is the app's
 * choice -- OPFS instead of memory, or no SQLite at all.
 *
 * This did not exist until the page was first loaded in a browser: there was
 * nowhere to pass a worker, and the sandbox could not run without one.
 */
export async function start(
	url = '/app.wasm',
	workerUrl: URL | string = new URL('./sandbox-worker.ts', import.meta.url),
): Promise<Sandbox> {
	const sock = await open(url, { workerUrl: new URL(workerUrl, location.href) })

	// No cast. `createDrpcTransport` answers a Connect `Transport` and `app`
	// takes one, which is the whole reason the sandbox and a real server differ
	// by this line and nothing above it. It used to need `as unknown as`, and
	// that was a **linking** artifact rather than a disagreement about types:
	// `@lesomnus/grpc-dgram` was a directory link, a linked package brings its
	// own `node_modules`, and two copies of `@connectrpc/connect` are two
	// nominally different `Transport`s. Published, with the peer dependency it
	// declares, there is one copy.
	const transport = createDrpcTransport(sock.dial())

	return {
		app: app(transport),
		sock,
		close: () => sock.close(),
	}
}
