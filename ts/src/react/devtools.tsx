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
 * `service` names the call, the message descriptor names the columns, and the
 * store is asked for a type name. Nothing below knows what a Robot is.
 *
 *     import { Devtools } from '@lesomnus/payday/react/devtools'
 *
 *     {import.meta.env.DEV && <Devtools entities={entities} />}
 *
 * # It brings its own look
 *
 * A panel an app has to style is a panel an app does not mount. The styles are
 * inline objects rather than a stylesheet: a library that ships CSS asks every
 * consumer's bundler about it, and this one is meant to be one import and no
 * configuration. They are also **scoped by being inline** — nothing here can
 * be reached by the app's own selectors, and nothing here reaches them.
 *
 * # What it does not do
 *
 * It does not read the database. The unit is the entity and the calls the wire
 * already answers, so what it shows has been through the wall, the layers and
 * whatever the caller's frame says — which is the question worth asking about a
 * screen. A row that is in the table and not here is the wall doing its job,
 * and telling those two apart is what [Props.ungated] is for.
 *
 * @module
 */

import { create, type DescField, type DescMessage, type DescMethodUnary, type DescService } from '@bufbuild/protobuf'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { createClient, type Transport } from '@connectrpc/connect'
import { useCallback, useEffect, useMemo, useState, type CSSProperties, type ReactNode } from 'react'

import * as pdid from '../pdid/index.js'
import { bytes, key, type EntityDesc, type Row } from '../store/index.js'

import { useApp } from './index.js'

/** Props is what an app hands the panel. */
export interface Props {
	/**
	 * Every entity of this app, which is the generated `entities` array.
	 *
	 * Taken rather than read off the store because the store keeps them by name
	 * for its own lookups, and what a picker wants is the list as the app
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
	 * What it buys is the one question the walled path cannot answer: a row that
	 * is not there and a row that is not visible look the same through the wall,
	 * and different through this.
	 */
	ungated?: Transport
}

/** Kept is what the panel remembers between reloads. */
interface Kept {
	open: boolean
	height: number
	entity: string
	tab: Tab
	/** Which columns are hidden, per entity. Absent is "all of them shown". */
	hidden: Record<string, string[]>
}

type Tab = 'list' | 'get' | 'store'

const at = 'payday.devtools'

/**
 * kept reads what was remembered, and answers the defaults for anything that
 * was not.
 *
 * Storage throws rather than answering in a private window and in a page whose
 * site data is blocked, and a panel that took the page down with it would be
 * worse than one that forgets. So every touch is guarded and forgetting is the
 * failure mode.
 */
function read(): Kept {
	const zero: Kept = { open: false, height: 320, entity: '', tab: 'list', hidden: {} }
	try {
		const v = localStorage.getItem(at)
		if (v === null) return zero

		return { ...zero, ...(JSON.parse(v) as Partial<Kept>) }
	} catch {
		return zero
	}
}

function write(v: Kept): void {
	try {
		localStorage.setItem(at, JSON.stringify(v))
	} catch {
		// A panel that cannot remember is still a panel.
	}
}

const ink = '#e6e6e6'
const dim = '#8b8b8b'
const line = '#2c2c2c'
const back = '#161616'

const style = {
	sheet: {
		position: 'fixed',
		left: 0,
		right: 0,
		bottom: 0,
		zIndex: 2147483000,
		background: back,
		color: ink,
		borderTop: `1px solid ${line}`,
		font: '12px ui-monospace, SFMono-Regular, Menlo, monospace',
		display: 'flex',
		flexDirection: 'column',
	},
	// The handle is what is there when nothing else is, so it is its own
	// element outside the sheet's flow: a page that never opens this sees a
	// tab at the bottom and nothing over its own content.
	handle: {
		position: 'fixed',
		left: '50%',
		transform: 'translateX(-50%)',
		zIndex: 2147483001,
		background: back,
		color: dim,
		border: `1px solid ${line}`,
		borderBottom: 'none',
		borderRadius: '6px 6px 0 0',
		padding: '2px 18px',
		cursor: 'ns-resize',
		font: '12px ui-monospace, SFMono-Regular, Menlo, monospace',
		lineHeight: '16px',
	},
	bar: {
		display: 'flex',
		gap: 8,
		alignItems: 'center',
		padding: '6px 8px',
		borderBottom: `1px solid ${line}`,
		flexWrap: 'wrap',
	},
	body: { overflow: 'auto', flex: 1, padding: 8 },
	table: { borderCollapse: 'collapse', whiteSpace: 'nowrap', width: 'max-content' },
	th: {
		textAlign: 'left',
		padding: '2px 10px 2px 0',
		borderBottom: `1px solid ${line}`,
		color: dim,
		fontWeight: 'normal',
		position: 'sticky',
		top: 0,
		background: back,
	},
	td: { padding: '2px 10px 2px 0', borderBottom: `1px solid ${line}`, verticalAlign: 'top' },
	input: {
		background: '#101010',
		color: ink,
		border: `1px solid ${line}`,
		borderRadius: 3,
		padding: '2px 4px',
		font: 'inherit',
	},
	link: {
		background: 'none',
		border: 'none',
		color: '#7db4ff',
		font: 'inherit',
		padding: 0,
		cursor: 'pointer',
		textDecoration: 'underline',
	},
	bad: { color: '#ff8b8b', whiteSpace: 'pre-wrap' },
} satisfies Record<string, CSSProperties>

/** Devtools is the panel. */
export function Devtools(props: Props): ReactNode {
	const app = useApp()
	const [kept, setKept] = useState<Kept>(read)

	const keep = useCallback((v: Partial<Kept>) => {
		setKept((old) => {
			const now = { ...old, ...v }
			write(now)

			return now
		})
	}, [])

	// Two lists, because they are two questions. A `List` exists where the
	// schema declared one; a `Get` exists for everything, which is what makes
	// following an edge work even into an entity no page lists.
	const lists = useMemo(() => byCall(props.entities, 'list'), [props.entities])
	const gets = useMemo(() => byCall(props.entities, 'get'), [props.entities])

	const [ungated, setUngated] = useState(false)
	const [looking, setLooking] = useState<{ typeName: string; id: string }>()

	const shown = kept.tab === 'get' ? gets : lists
	const entity = shown.find((v) => v.typeName === kept.entity) ?? shown[0]

	const transport = ungated && props.ungated !== undefined ? props.ungated : app.queries.raw

	/** look is what following an edge does: the Get tab, on the row it named. */
	const look = useCallback(
		(typeName: string, id: string) => {
			setLooking({ typeName, id })
			keep({ tab: 'get', entity: typeName })
		},
		[keep],
	)

	if (!kept.open) {
		return (
			<button type="button" style={{ ...style.handle, bottom: 0 }} onClick={() => keep({ open: true })}>
				payday
			</button>
		)
	}

	return (
		<>
			<button
				type="button"
				style={{ ...style.handle, bottom: kept.height }}
				onClick={() => keep({ open: false })}
				aria-label="devtools"
			>
				payday
			</button>

			<section style={{ ...style.sheet, height: kept.height }}>
				<div style={style.bar}>
					<select
						aria-label="entity"
						style={style.input}
						value={entity?.typeName ?? ''}
						onChange={(e) => keep({ entity: e.target.value })}
					>
						{shown.map((v) => (
							<option key={v.typeName} value={v.typeName}>
								{v.typeName}
							</option>
						))}
					</select>

					{(['list', 'get', 'store'] as const).map((v) => (
						<button
							key={v}
							type="button"
							style={{ ...style.input, cursor: 'pointer', color: kept.tab === v ? ink : dim }}
							aria-pressed={kept.tab === v}
							onClick={() => keep({ tab: v })}
						>
							{v}
						</button>
					))}

					{props.ungated !== undefined && (
						<label style={{ color: ungated ? '#ffb86b' : dim }}>
							<input
								type="checkbox"
								checked={ungated}
								onChange={(e) => setUngated(e.target.checked)}
							/>
							past the wall
						</label>
					)}

					<span style={{ flex: 1 }} />

					<button
						type="button"
						style={{ ...style.input, cursor: 'pointer' }}
						onClick={() => keep({ height: kept.height === 320 ? 640 : 320 })}
					>
						{kept.height === 320 ? 'taller' : 'shorter'}
					</button>
				</div>

				<div style={style.body}>
					{entity === undefined ? (
						<p style={style.bad}>this app declares no entity that answers here.</p>
					) : kept.tab === 'store' ? (
						<Held entity={entity} hidden={kept.hidden} keep={keep} look={look} entities={props.entities} />
					) : kept.tab === 'get' ? (
						<Get
							key={`${entity.typeName}:${String(ungated)}`}
							entity={entity}
							entities={props.entities}
							transport={transport}
							hidden={kept.hidden}
							keep={keep}
							look={look}
							{...(looking?.typeName === entity.typeName ? { id: looking.id } : {})}
						/>
					) : (
						<List
							key={`${entity.typeName}:${String(ungated)}`}
							entity={entity}
							entities={props.entities}
							transport={transport}
							hidden={kept.hidden}
							keep={keep}
							look={look}
						/>
					)}
				</div>
			</section>
		</>
	)
}

/** byCall is the entities whose service answers one, sorted as declared. */
function byCall(vs: readonly EntityDesc[], rpc: string): EntityDesc[] {
	return vs.filter((v) => v.service?.method[rpc] !== undefined)
}

interface View {
	entity: EntityDesc
	entities: readonly EntityDesc[]
	hidden: Record<string, string[]>
	keep: (v: Partial<Kept>) => void
	look: (typeName: string, id: string) => void
}

/** List is what the server answers for a whole page of them. */
function List(props: View & { transport: Transport }): ReactNode {
	const [filters, setFilters] = useState('[]')
	const [rows, setRows] = useState<Record<string, unknown>[]>([])
	const [next, setNext] = useState('')
	const [err, setErr] = useState<string>()

	const ask = useCallback(
		async (after: string) => {
			setErr(undefined)
			try {
				const req: Record<string, unknown> = { filters: JSON.parse(filters) as unknown }
				if (after !== '') req.after = after

				const v = (await call(props.entity, props.transport, 'list', req)) as {
					items: Record<string, unknown>[]
					next: string
				}

				setRows(v.items)
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
		// On the entity and the path, not on every keystroke in the filters.
	}, [props.entity, props.transport]) // eslint-disable-line react-hooks/exhaustive-deps

	return (
		<div>
			<div style={{ display: 'flex', gap: 6, marginBottom: 6 }}>
				<input
					aria-label="filters"
					style={{ ...style.input, flex: 1 }}
					value={filters}
					onChange={(e) => setFilters(e.target.value)}
					spellCheck={false}
				/>
				<button type="button" style={{ ...style.input, cursor: 'pointer' }} onClick={() => void ask('')}>
					ask
				</button>
				{next !== '' && (
					<button type="button" style={{ ...style.input, cursor: 'pointer' }} onClick={() => void ask(next)}>
						next
					</button>
				)}
			</div>

			{err !== undefined && <p style={style.bad}>{err}</p>}
			<Table label="served" rows={rows} {...props} />
		</div>
	)
}

/** Get is one row, by the identifier somebody typed or followed an edge to. */
function Get(props: View & { transport: Transport; id?: string }): ReactNode {
	const [id, setId] = useState(props.id ?? '')
	const [rows, setRows] = useState<Record<string, unknown>[]>([])
	const [err, setErr] = useState<string>()

	const ask = useCallback(
		async (v: string) => {
			setErr(undefined)
			setRows([])
			if (v === '') return

			try {
				const ref = { key: { case: 'id', value: pdid.parse(v).bytes } }
				const got = (await call(props.entity, props.transport, 'get', { ref })) as Record<string, unknown>

				setRows([got])
			} catch (e) {
				setErr(String(e))
			}
		},
		[props.entity, props.transport],
	)

	// Following an edge is a new `id` arriving from above, which asks for it
	// rather than waiting for somebody to press the button again.
	useEffect(() => {
		if (props.id === undefined) return

		setId(props.id)
		void ask(props.id)
	}, [props.id, ask])

	return (
		<div>
			<div style={{ display: 'flex', gap: 6, marginBottom: 6 }}>
				<input
					aria-label="identifier"
					style={{ ...style.input, minWidth: 320 }}
					value={id}
					placeholder="019ff7c9-8a1e-8c3d-9f07-2b6c1f0a4d51"
					onChange={(e) => setId(e.target.value)}
					spellCheck={false}
				/>
				<button type="button" style={{ ...style.input, cursor: 'pointer' }} onClick={() => void ask(id)}>
					look up
				</button>
			</div>

			{err !== undefined && <p style={style.bad}>{err}</p>}
			<Table label="served" rows={rows} {...props} />
		</div>
	)
}

/** Held is what the store holds, which is not the same question. */
function Held(props: View): ReactNode {
	const app = useApp()
	const rows = app.store.all(props.entity.typeName) as unknown as Record<string, unknown>[]

	return <Table label="held" rows={rows} {...props} />
}

/**
 * Table is the rows, as a table.
 *
 * The columns are the message's fields, in the order the schema declares them,
 * and the whole of it scrolls sideways rather than wrapping: a row is wide
 * because the entity is, and a wrapped one cannot be read across.
 */
function Table(props: View & { label: string; rows: Record<string, unknown>[] }): ReactNode {
	const fields = props.entity.schema.fields
	const hidden = new Set(props.hidden[props.entity.typeName] ?? [])
	const refs = new Map((props.entity.refs ?? []).map((v) => [v.field, v.to]))

	const toggle = (name: string): void => {
		const now = new Set(hidden)
		if (now.has(name)) {
			now.delete(name)
		} else {
			now.add(name)
		}

		props.keep({ hidden: { ...props.hidden, [props.entity.typeName]: [...now] } })
	}

	return (
		<div>
			<div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 6, color: dim }}>
				{fields.map((f) => (
					<label key={f.localName}>
						<input type="checkbox" checked={!hidden.has(f.localName)} onChange={() => toggle(f.localName)} />
						{f.localName}
					</label>
				))}
			</div>

			<div style={{ overflowX: 'auto' }}>
				<table style={style.table} aria-label={props.label}>
					<thead>
						<tr>
							{fields.map((f) =>
								hidden.has(f.localName) ? null : (
									<th key={f.localName} style={style.th}>
										{f.localName}
									</th>
								),
							)}
						</tr>
					</thead>
					<tbody>
						{props.rows.map((row, i) => (
							<tr key={String(row.id ?? i)}>
								{fields.map((f) =>
									hidden.has(f.localName) ? null : (
										<td key={f.localName} style={style.td}>
											<Cell
												value={row[f.localName]}
												field={f}
												to={refs.get(f.localName)}
												look={props.look}
											/>
										</td>
									),
								)}
							</tr>
						))}
					</tbody>
				</table>
			</div>
		</div>
	)
}

/**
 * Cell is one value, rendered as the thing it is rather than as JSON.
 *
 * An identifier is sixteen bytes and protojson would show them base64, which is
 * not what anybody has written down anywhere: a payday identifier is a uuid and
 * carries the entity in its ninth byte, so it is shown the way it is typed.
 *
 * An edge is not expanded. What the server answered with is a reference, so
 * what there is to show is the row it names — and following it is a `Get`,
 * which is a click rather than a join nobody asked for.
 */
function Cell(props: {
	value: unknown
	field: DescField
	/** The entity this field names, for a field that names one. */
	to: string | undefined
	look: (typeName: string, id: string) => void
}): ReactNode {
	const v = props.value
	if (v === undefined || v === null) return <span style={{ color: dim }}>—</span>

	// The store's own rows key by hex and the wire's carry bytes; both are an
	// identifier and are shown as one.
	if (props.to !== undefined) {
		const id = idOf(v)
		if (id === undefined) return <span style={{ color: dim }}>—</span>

		return (
			<button type="button" style={style.link} onClick={() => props.look(props.to as string, id)}>
				{id}
			</button>
		)
	}

	const id = props.field.localName === 'id' ? idOf(v) : undefined
	if (id !== undefined) return <span>{id}</span>

	if (v instanceof Uint8Array) return <span>{key(v)}</span>

	if (typeof v === 'object' && (v as { $typeName?: string }).$typeName === 'google.protobuf.Timestamp') {
		return <span>{timestampDate(v as never).toISOString()}</span>
	}

	if (typeof v === 'object') return <span>{JSON.stringify(v)}</span>

	return <span>{String(v)}</span>
}

/** idOf reads an identifier out of whatever shape it arrived in. */
function idOf(v: unknown): string | undefined {
	const b = v instanceof Uint8Array ? v : ((v as { id?: unknown } | null)?.id ?? v)

	try {
		if (b instanceof Uint8Array) return pdid.from(b).toString()
		if (typeof b === 'string' && b !== '') return pdid.from(bytes(b)).toString()
	} catch {
		// Not one of ours, which a panel says by saying nothing about it.
	}

	return undefined
}

/**
 * call is one RPC on an entity's service, over the transport it was handed.
 *
 * Directly, and never through `Queries`: everything there ends in `store.put`,
 * so reading here would be what makes the served tab and the store tab agree.
 * See `Queries.raw`.
 */
async function call(e: EntityDesc, t: Transport, rpc: string, req: unknown): Promise<unknown> {
	const c = createClient(e.service as DescService, t) as unknown as Record<
		string,
		(v: unknown) => Promise<unknown>
	>

	const f = c[rpc]
	if (f === undefined) throw new Error(`${e.typeName} answers no ${rpc}`)

	return f(req)
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
 * opposite of the reads above and for the same reason: a write is supposed to
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
		return <p style={style.bad}>nothing here takes a `Patch`.</p>
	}

	const f = fields[field]
	if (f === undefined) return null

	const send = async (): Promise<void> => {
		setErr(undefined)
		try {
			const req: Record<string, unknown> = {
				// The store keys rows by hex — `desc.key` says why — and a ref
				// is the identifier itself, so this reads it back rather than
				// sending the key.
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
		<div style={{ display: 'flex', gap: 6 }}>
			<select
				value={field}
				style={style.input}
				onChange={(e) => setField(Number(e.target.value))}
				aria-label="field"
			>
				{fields.map((v, i) => (
					<option key={v.localName} value={i}>
						{v.localName}
					</option>
				))}
			</select>
			<input aria-label="value" style={style.input} value={value} onChange={(e) => setValue(e.target.value)} />
			<button type="button" style={{ ...style.input, cursor: 'pointer' }} onClick={() => void send()}>
				patch
			</button>
			{err !== undefined && <p style={style.bad}>{err}</p>}
		</div>
	)
}
