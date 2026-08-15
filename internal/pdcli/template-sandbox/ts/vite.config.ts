import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

/**
 * `npm run dev` serves this on 5173 and the app answers on 8080, which is two
 * origins -- so the server has to say the page may call it. That is the
 * `origins:` list under `server.http` in the app's configuration, and it is a
 * list rather than a wildcard on purpose.
 *
 * A deployment that serves `dist/` from the app itself is one origin and needs
 * none of it.
 *
 * The rest of this file is what it takes to serve the sandbox, which is two
 * things and neither is payday's to fix. Both fail confusingly, which is why
 * they are written out rather than left to a README -- and `pd doctor` checks
 * that they are still here.
 */
export default defineConfig({
	plugins: [react()],

	// The worker `@lesomnus/grpc-dgram` starts is
	// `new URL("./wasm/worker.mjs", import.meta.url)`, and dependency
	// pre-bundling rewrites the module into `.vite/deps/` -- where that
	// relative URL resolves to nothing. The failure is "the worker itself
	// failed", which does not mention bundling.
	optimizeDeps: { exclude: ['@lesomnus/grpc-dgram'] },

	server: {
		// SQLite in a Worker cancels work with a `SharedArrayBuffer`, which
		// does not exist without cross-origin isolation. The symptom is "it
		// works on the other dev server".
		headers: {
			'Cross-Origin-Opener-Policy': 'same-origin',
			'Cross-Origin-Embedder-Policy': 'require-corp',
		},
	},
})
