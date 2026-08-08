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
 * @module
 */

import { createDrpcTransport } from '@lesomnus/grpc-dgram/transport/connect'
import { open, type WasmSock } from '@lesomnus/grpc-dgram/wasm'

import type { Transport } from '@connectrpc/connect'

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
 */
export async function start(url = '/app.wasm'): Promise<Sandbox> {
	const sock = await open(url)

	// The cast is a **linking** artifact and not a disagreement about types.
	// `@lesomnus/grpc-dgram` is not published yet, so it is here as a directory
	// link, and a linked package brings its own `node_modules` -- which means
	// two copies of `@connectrpc/connect`, whose `Transport` types are
	// structurally the same and nominally different. Published, with the peer
	// dependency it declares, there is one copy and this is not needed.
	const transport = createDrpcTransport(sock.dial()) as unknown as Transport

	return {
		app: app(transport),
		sock,
		close: () => sock.close(),
	}
}
