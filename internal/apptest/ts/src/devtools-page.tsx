/**
 * The panel, over the whole app running in this page.
 *
 * It is not a test and it is not shipped -- it is the thing to open when the
 * question is what the panel looks like, which is a question no assertion
 * answers. `npm run dev` serves it; see the header of `vite.config.ts` for the
 * two things a sandbox needs from a dev server.
 *
 * The app is the real one: the same schema, the same wall, the same server
 * compiled to wasm and answering over a message port. So what the panel shows
 * here is what it would show anywhere.
 */

import { useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'

import { Queries } from '@lesomnus/payday/query'
import { Provider, type App } from '@lesomnus/payday/react'
import { Devtools } from '@lesomnus/payday/react/devtools'
import { Store } from '@lesomnus/payday/store'

import { entities } from '../gen/entities.js'
import { app as client } from './client.js'
import { start } from './sandbox.js'

/**
 * once is the app, started one time.
 *
 * StrictMode runs an effect twice on purpose, to find the ones that cannot be.
 * Starting a wasm instance is one of those: twice is two workers, two
 * databases, and a cleanup that closes the one still being used. So the start
 * is module state rather than effect state, which is what "once per page" is.
 */
let once: Promise<App> | undefined

function Page(): React.ReactNode {
	const [app, setApp] = useState<App>()
	const [err, setErr] = useState<string>()

	useEffect(() => {
		once ??= boot()
		once.then(setApp, (e: unknown) => setErr(String(e)))
	}, [])

	if (err !== undefined) return <main><h1>failed</h1><pre>{err}</pre></main>

	return (
		<main>
			<h1>payday devtools</h1>
			<p>
				The whole app is running in this page — the same schema, the same wall, the same server compiled
				to wasm. The handle is at the bottom.
			</p>
			{app === undefined ? <p>starting…</p> : (
				<Provider app={app}>
					<Devtools entities={entities} />
				</Provider>
			)}
		</main>
	)
}

/** boot starts the app and seeds it with something to look at. */
async function boot(): Promise<App> {
	const box = await start()

				// `Plain` believes what the caller writes, which is what a
	// `Plain` believes what the caller writes, which is what a sandbox is:
	// there is nobody else in the page to lie to.
	const raw = box.transport as unknown as {
		unary: (...v: unknown[]) => unknown
		stream: (...v: unknown[]) => unknown
	}

	const who = (h: unknown): Headers => {
		const out = new Headers(h as HeadersInit)
		out.set('authorization', 'Plain @acme/admin')

		return out
	}

	const transport = {
		unary: (m: unknown, s: unknown, t: unknown, h: unknown, i: unknown, o: unknown) =>
			raw.unary(m, s, t, who(h), i, o),
		stream: (m: unknown, s: unknown, t: unknown, h: unknown, i: unknown, o: unknown) =>
			raw.stream(m, s, t, who(h), i, o),
	}

	// Something for the panel to look at, since a fresh sandbox has only what
	// `wasm/main.go` seeds. Through the **wrapped** transport: `box.app` says
	// who it is to nobody, and the wall answers "who is asking?" to that.
	const c = client(transport as never)
	const t = await c.tenant.get({ ref: { key: { case: 'alias', value: 'acme' } } })
	for (const alias of ['arm-01', 'arm-02', 'arm-03']) {
		await c.robot.add({ tenant: { key: { case: 'id', value: t.id } }, alias })
	}

	const store = Store.open(entities, { name: 'apptest', identity: 'admin' })

	return { store, queries: new Queries(store, transport as never, entities) }
}

createRoot(document.getElementById('root') as HTMLElement).render(<Page />)
