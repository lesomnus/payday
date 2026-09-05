/**
 * A window on what the server answers and what this side believes.
 *
 * Every payday app has the same three questions when a screen is wrong, and
 * none of them is about the app's own code: what does the server say this
 * caller may see, what does the store hold, and which of the two is stale. An
 * app cannot answer them from outside — the store is payday's state — and
 * answering them per entity would be one page per table.
 *
 * So it is here, and it is generic because a declaration says enough to be:
 * `service` names the call, the message descriptor names the fields, and the
 * store is asked for a type name. Nothing below knows what a Robot is.
 *
 *     import { Devtools } from '@lesomnus/payday/react/devtools'
 *
 *     {import.meta.env.DEV && <Devtools entities={entities} />}
 *
 * # What it does not do
 *
 * It does not read the database. The unit is the entity and the `List` the
 * wire already answers, so what it shows has been through the wall, the layers
 * and whatever the caller's frame says — which is the question worth asking
 * about a screen. A row that is in the table and not here is the wall doing its
 * job, and telling those two apart is what [Props.ungated] is for.
 *
 * @module
 */

import { create, toJson, type DescField, type DescMessage, type DescMethodUnary, type DescService } from '@bufbuild/protobuf'
import { createClient, type Transport } from '@connectrpc/connect'
import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'

import { bytes, type EntityDesc, type Row } from '../store/index.js'

import { useApp } from './index.js'

/** Props is what an app hands the panel. */
export interface Props {
	/**
	 * Every entity of this app, which is the generated `entities` array.
	 *
	 * Taken rather than read off the store because the store keeps them by
	 * name for its own lookups, and what a picker wants is the list as the app
	 * declared it — one import, and the same one `Store.open` was given.
	 */
	entities: readonly EntityDesc[]

	/**
	 * A second transport reaching the **ungated** stack, for a deployment that
	 * serves one — which is a sandbox, where the server is in the page and the
	 * wall protects the page from itself.
	 *
	 * Absent is the ordinary case and the safe one: with nothing to switch to
	 * there is no switch, so a page that was never given one cannot offer it.
	 * That is the whole of the guard, and it is structural rather than a flag —
	 * a served deployment has no ungated port to reach, so an app has nothing
	 * to pass.
	 *
	 * What it buys is the one question the walled path cannot answer: a row
	 * that is not there and a row that is not visible look the same through the
	 * wall, and different through this.
	 */
	ungated?: Transport
}

/** Devtools is the panel. */
export function Devtools(props: Props): ReactNode {
	const app = useApp()

	const listable = useMemo(
		() => props.entities.filter((v) => v.service?.method.list !== undefined),
		[props.entities],
	)

	const [at, setAt] = useState(0)
	const [tab, setTab] = useState<'served' | 'store'>('served')
	const [ungated, setUngated] = useState(false)

	const entity = listable[at]
	if (entity === undefined) {
		return <p>this app declares no entity with a `list:`, so there is nothing to page through.</p>
	}

	const transport = ungated && props.ungated !== undefined ? props.ungated : app.queries.raw

	return (
		<section>
			<nav>
				<select value={at} onChange={(e) => setAt(Number(e.target.value))} aria-label="entity">
					{listable.map((v, i) => (
						<option key={v.typeName} value={i}>
							{v.typeName}
						</option>
					))}
				</select>

				<button type="button" onClick={() => setTab('served')} aria-pressed={tab === 'served'}>
					served
				</button>
				<button type="button" onClick={() => setTab('store')} aria-pressed={tab === 'store'}>
					store
				</button>

				{props.ungated !== undefined && (
					<label>
						<input type="checkbox" checked={ungated} onChange={(e) => setUngated(e.target.checked)} />
						past the wall
					</label>
				)}
			</nav>

			{tab === 'served' ? (
				<Served key={`${entity.typeName}:${String(ungated)}`} entity={entity} transport={transport} />
			) : (
				<Held entity={entity} />
			)}
		</section>
	)
}

/** Served is what the server answers, asked for directly. */
function Served(props: { entity: EntityDesc; transport: Transport }): ReactNode {
	const [filters, setFilters] = useState('[]')
	const [rows, setRows] = useState<unknown[]>([])
	const [next, setNext] = useState('')
	const [err, setErr] = useState<string>()

	const ask = useCallback(
		async (after: string) => {
			setErr(undefined)
			try {
				const req: Record<string, unknown> = { filters: JSON.parse(filters) as unknown }
				if (after !== '') req.after = after

				// Directly, and never through `Queries`: everything there ends
				// in `store.put`, so reading here would be what makes the two
				// tabs agree. See `Queries.raw`.
				const svc = props.entity.service as DescService
				const c = createClient(svc, props.transport) as unknown as Record<
					string,
					(v: unknown) => Promise<{ items: unknown[]; next: string }>
				>

				const v = await c.list({ ...req })
				const out = props.entity.schema
				setRows(v.items.map((w) => toJson(out, w as never)))
				setNext(v.next)
			} catch (e) {
				setRows([])
				setErr(String(e))
			}
		},
		[filters, props.entity, props.transport],
	)

	useEffect(() => {
		void ask('')
		// The filters are the app's to change and are read when it asks, so
		// this runs on the entity and the path rather than on every keystroke.
	}, [props.entity, props.transport]) // eslint-disable-line react-hooks/exhaustive-deps

	return (
		<div>
			<textarea
				aria-label="filters"
				value={filters}
				onChange={(e) => setFilters(e.target.value)}
				spellCheck={false}
			/>
			<button type="button" onClick={() => void ask('')}>
				ask
			</button>
			{next !== '' && (
				<button type="button" onClick={() => void ask(next)}>
					next
				</button>
			)}

			{err !== undefined && <p role="alert">{err}</p>}

			<ol aria-label="served">
				{rows.map((v, i) => (
					<li key={i}>
						<pre>{JSON.stringify(v, null, 2)}</pre>
					</li>
				))}
			</ol>
		</div>
	)
}

/** Held is what the store holds, which is not the same question. */
function Held(props: { entity: EntityDesc }): ReactNode {
	const app = useApp()
	const rows: Row[] = app.store.all(props.entity.typeName)

	return (
		<ol aria-label="held">
			{rows.map((v) => (
				<li key={v.id}>
					<pre>{JSON.stringify(v, null, 2)}</pre>
				</li>
			))}
		</ol>
	)
}

/**
 * patchable is the fields a `Patch` may set, which the request already says: it
 * carries one per mutable field and none for `id`, the tenant, or anything else
 * a schema marked immutable.
 *
 * The `<field>_null` companions and the version are left out. The first is a
 * naming convention rather than something a descriptor marks, and reading a
 * suffix is how a panel starts being wrong quietly; the second is a
 * precondition and is filled from the row rather than typed.
 */
export function patchable(method: DescMethodUnary<DescMessage, DescMessage>, version?: string): DescField[] {
	const skip = new Set<string>(['ref'])
	if (version !== undefined) {
		skip.add(version)
		skip.add(`${version}Force`)
	}

	const named = new Set(method.input.fields.map((f) => f.localName))

	return method.input.fields.filter((f) => {
		if (skip.has(f.localName)) return false
		if (f.localName.endsWith('Null') && named.has(f.localName.slice(0, -4))) return false

		// A message-valued field is a reference or a document, and neither is
		// something to type into a box. Scalars are what this edits.
		return f.fieldKind === 'scalar'
	})
}

/**
 * Edit sets one field of one row, through `Patch`.
 *
 * It goes through `Queries.call` rather than the raw transport, which is the
 * opposite of the read above and for the same reason: a write is supposed to
 * reach the store, so that every screen drawing that row is right afterwards.
 * Watching the page change is most of what this is for.
 *
 * The version travels as the precondition. A row read a minute ago and patched
 * now is refused rather than applied over somebody else's write, and seeing
 * that refusal is worth more than a panel that always wins.
 */
export function Edit(props: { entity: EntityDesc; row: Row; onDone?: () => void }): ReactNode {
	const app = useApp()
	const method = props.entity.service?.method.patch as DescMethodUnary<DescMessage, DescMessage> | undefined

	const fields = useMemo(
		() => (method === undefined ? [] : patchable(method, props.entity.version)),
		[method, props.entity.version],
	)

	const [field, setField] = useState(0)
	const [value, setValue] = useState('')
	const [err, setErr] = useState<string>()

	if (method === undefined || fields.length === 0) {
		return <p>nothing here takes a `Patch`.</p>
	}

	const f = fields[field]
	if (f === undefined) return null

	const send = async (): Promise<void> => {
		setErr(undefined)
		try {
			const req: Record<string, unknown> = {
				// The store keys rows by hex -- `desc.key` says why -- and a
				// ref is the identifier itself, so this reads it back rather
				// than sending the key.
				ref: { key: { case: 'id', value: bytes(props.row.id) } },
				[f.localName]: value,
			}
			if (props.entity.version !== undefined) {
				req[props.entity.version] = props.row[props.entity.version]
			}

			await app.queries.call(method, create(method.input, req as never))
			props.onDone?.()
		} catch (e) {
			setErr(String(e))
		}
	}

	return (
		<div>
			<select value={field} onChange={(e) => setField(Number(e.target.value))} aria-label="field">
				{fields.map((v, i) => (
					<option key={v.localName} value={i}>
						{v.localName}
					</option>
				))}
			</select>
			<input aria-label="value" value={value} onChange={(e) => setValue(e.target.value)} />
			<button type="button" onClick={() => void send()}>
				patch
			</button>
			{err !== undefined && <p role="alert">{err}</p>}
		</div>
	)
}
