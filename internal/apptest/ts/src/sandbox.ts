/**
 * The whole app, in the page.
 *
 * Almost all of this is `@lesomnus/payday/sandbox` now, and what is left is the
 * two things payday cannot know: which services this app has, and where its
 * worker file is. That is the shape every payday app's sandbox has -- see
 * `pd sandbox init`, which writes the other half.
 *
 * The Go half is the same server the process runs: the same generated services,
 * the same stack, the same wall from the same schema. Two things differ and
 * both are one line in `wasm/main.go` -- the database is SQLite in a Worker
 * rather than a file, and calls arrive over a message port rather than HTTP/2.
 *
 * @module
 */

import { start as open, type Sandbox as Instance } from '@lesomnus/payday/sandbox'

import { app, type App } from './client.js'

/** Sandbox is this app, and the instance it is running inside. */
export interface Sandbox {
	readonly app: App

	/**
	 * The transport underneath, for a page that wants to wrap it -- to say who
	 * is calling, say.
	 *
	 * Handed on rather than left to be rebuilt from [Sandbox.sock], and that is
	 * not a convenience. A page that called `createDrpcTransport` itself would
	 * be using **its** copy of `@lesomnus/grpc-dgram` on a socket opened by the
	 * one `@lesomnus/payday` resolved, and in this repository those are two
	 * copies: payday's TypeScript is linked by path and a linked package brings
	 * its own `node_modules`. What that costs is not a type error -- it is a
	 * refusal arriving with no status, so `NotFound` reads as `Unknown`.
	 *
	 * A published payday has one copy, because `@lesomnus/grpc-dgram` is a peer
	 * dependency. This is the seam that makes the linked case behave like the
	 * published one, and it is worth having anyway: dialing again is a second
	 * connection nobody asked for.
	 */
	readonly transport: Instance['transport']

	/** The wasm instance, for a page that wants to take it down. */
	readonly sock: Instance['sock']

	/** close stops the server. A reload does the same thing more thoroughly. */
	close(): void
}

/**
 * start compiles the app into the page and answers with a client for it.
 *
 * `onProgress` is how far the module has got, for a page that would rather show
 * that than nothing: this one is 72MB, and on a cold cache the wait is long
 * enough that silence reads as a hang.
 *
 * The worker URL is resolved against **this** module rather than passed in,
 * because `sandbox-worker.ts` sits beside this file and a bundler rewrites
 * where it lands. It is this file's to know and not the package's: a default
 * inside `@lesomnus/payday` would resolve against `node_modules`.
 */
export async function start(url = '/app.wasm', onProgress?: (loaded: number, total: number) => void): Promise<Sandbox> {
	const box = await open({
		url,
		worker: new URL('./sandbox-worker.ts', import.meta.url),

		// Passed on rather than defaulted to something, because a sandbox with
		// nowhere to draw has no use for the count -- see `Opts.onProgress`,
		// which is also where `total` being 0 is explained.
		...(onProgress === undefined ? {} : { onProgress }),
	})

	// `box.transport` is a Connect `Transport` like any other, which is the
	// whole reason a sandbox is worth having: `app()` is the same call the real
	// server gets, and nothing above it knows which one it got. Code that only
	// ever ran against a fake is code that has never run.
	return {
		app: app(box.transport),
		transport: box.transport,
		sock: box.sock,
		close: box.close,
	}
}
