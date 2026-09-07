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

import { StrictMode, useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'

import { Queries } from '@lesomnus/payday/query'
import { Provider, type App } from '@lesomnus/payday/react'
import type { Load } from '@lesomnus/payday/sandbox'
import { Devtools, type MonacoLike } from '@lesomnus/payday/react/devtools'

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

/** watching is whoever is drawing the wait, for the same reason `once` exists. */
const watching = new Set<(v: Load) => void>()

/** editing is monaco, fetched once, for as long as this page is open. */
let editing: Promise<MonacoLike> | undefined

/**
 * editor is the four things payday asks of monaco, fetched rather than
 * imported.
 *
 * Monaco is four megabytes and this page is a table. Imported at the top it is
 * in the entry chunk, so nothing renders until all of it has arrived -- and the
 * first thing anybody does here is read a list, which needs none of it. Asked
 * for after the page is up, the table is on the screen and interactive while
 * the editor is still coming, and a `Get` opened before it lands is the
 * read-only document the panel draws without one.
 *
 * `MonacoEnvironment` is set in here for the same reason it exists at all: the
 * URL of a bundled worker is something only the bundler that produced it knows,
 * and `?worker` is vite's way of saying so. It has to be set before monaco asks
 * for a worker, and the only ordering that guarantees is this one -- beside the
 * import that will do the asking. Without the JSON one there is no completion
 * and no validation, and the editor still edits, which is the confusing half of
 * getting it wrong.
 *
 * Module state and not a hook, for the reason `once` is: StrictMode runs an
 * effect twice, and two of these is two copies of four megabytes.
 */
function editor(): Promise<MonacoLike> {
	editing ??= (async () => {
		const [monaco, json, plain] = await Promise.all([
			import('monaco-editor'),
			import('monaco-editor/language/json/json.worker?worker'),
			import('monaco-editor/editor/editor.worker?worker'),
		])

		;(self as unknown as { MonacoEnvironment: unknown }).MonacoEnvironment = {
			getWorker(_id: string, label: string) {
				return label === 'json' ? new json.default() : new plain.default()
			},
		}

		return { editor: monaco.editor, Uri: monaco.Uri, json: monaco.json.jsonDefaults }
	})()

	return editing
}

function Page(): React.ReactNode {
	const [app, setApp] = useState<App>()
	const [err, setErr] = useState<string>()
	const [got, setGot] = useState<Load>()
	const [edit, setEdit] = useState<MonacoLike>()

	// Started here rather than at the top of the module, so that it is fetched
	// beside the page instead of before it. Nothing waits on it: the panel is
	// whole without one -- a `Get` is the same document, read-only -- and it
	// becomes editable when this lands.
	useEffect(() => {
		let alive = true
		editor().then(
			(v) => {
				if (alive) setEdit(v)
			},
			(e: unknown) => console.error('monaco:', e),
		)

		return () => {
			alive = false
		}
	}, [])

	useEffect(() => {
		// Subscribed to rather than passed in, because the boot is module state
		// and this component is not: a second mount -- which StrictMode
		// guarantees -- gets the run already in flight, and a callback captured
		// by the first one would be reporting into a component nobody is
		// looking at.
		watching.add(setGot)
		once ??= boot()
		once.then(setApp, (e: unknown) => setErr(String(e)))

		return () => void watching.delete(setGot)
	}, [])

	if (err !== undefined) return <main><h1>failed</h1><pre>{err}</pre></main>

	return (
		<main>
			<h1>payday devtools</h1>
			<p>
				The whole app is running in this page — the same schema, the same wall, the same server compiled
				to wasm. The handle is at the bottom.
			</p>
			{app === undefined ? <Starting got={got} /> : (
				<Provider app={app}>
					<Devtools entities={entities} {...(edit === undefined ? {} : { monaco: edit })} />
				</Provider>
			)}
		</main>
	)
}

/**
 * Starting is the wait, which is most of a cold load.
 *
 * Four states and not two. A module still arriving has a fraction to show; one
 * that has all arrived is being compiled, which takes seconds more and has
 * nothing to report; and before the first byte there is nothing at all. The
 * fourth is where the bytes are coming from -- reading 72MB off a disk fills a
 * bar exactly like downloading it does, and somebody watching that on a reload
 * concludes the caching is broken. It is not; see `Opts.cache`.
 */
function Starting(props: { got: Load | undefined }): React.ReactNode {
	const got = props.got
	const mb = (n: number): string => `${(n / 1024 / 1024).toFixed(1)} MB`

	// `total` is 0 when the length was not knowable -- a chunked or a
	// re-encoded response; see `Load.total`. Bytes are still worth saying then,
	// so it falls back to those rather than to nothing.
	const part = got === undefined || got.total === 0 ? undefined : got.loaded / got.total
	const done = got !== undefined && part === 1
	const kept = got?.from === 'cache'

	return (
		<p style={{ display: 'flex', gap: 8, alignItems: 'center', color: '#8b8b8b' }}>
			<span
				style={{
					width: 160,
					height: 4,
					borderRadius: 2,
					background: '#2c2c2c',
					overflow: 'hidden',
					flex: 'none',
				}}
			>
				<span
					style={{
						display: 'block',
						height: '100%',
						background: kept ? '#a8e6a1' : '#7db4ff',
						// An unknown length is the whole bar, dimmed: there is
						// no fraction to draw and an empty bar would say the
						// download had not started.
						width: part === undefined ? '100%' : `${String(part * 100)}%`,
						opacity: part === undefined ? 0.3 : 1,
						transition: 'width 120ms linear',
					}}
				/>
			</span>

			<span style={{ whiteSpace: 'nowrap' }}>
				{got === undefined
					? 'fetching…'
					: done
						? 'compiling…'
						: part === undefined
							? `${mb(got.loaded)}…`
							: `${mb(got.loaded)} of ${mb(got.total)}`}
			</span>

			{/*
				How fast, which is the other half of "is this stuck". A bar
				that has not moved in a second and a rate of 40 MB/s are the
				same picture and different situations.
			*/}
			{got !== undefined && !done && got.rate > 0 && (
				<span style={{ whiteSpace: 'nowrap', color: '#5f5f5f' }}>{mb(got.rate)}/s</span>
			)}

			{kept && <span style={{ color: '#5f5f5f' }}>from the last visit</span>}

			{got !== undefined && !got.keeping && (
				// The one thing a repeated download does not say about itself.
				// See `Load.keeping`.
				<span style={{ color: '#ffb86b' }}>
					not kept — this origin is not a secure context, so open it over localhost or https
				</span>
			)}
		</p>
	)
}

/** boot starts the app and seeds it with something to look at. */
async function boot(): Promise<App> {
	const box = await start('/app.wasm', (v) => {
		for (const w of watching) w(v)
	})

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

createRoot(document.getElementById('root') as HTMLElement).render(
	// StrictMode on, because the panel is React and this page is where its
	// effects are actually run twice. The one thing that cannot be run twice is
	// starting the server, and `once` above is what says so.
	<StrictMode>
		<Page />
	</StrictMode>,
)
