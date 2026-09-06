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

import {
	create,
	ScalarType,
	type DescField,
	type DescMessage,
	type DescMethodUnary,
	type DescService,
} from '@bufbuild/protobuf'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { createClient, type Transport } from '@connectrpc/connect'
import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react'

import * as pdid from '../pdid/index.js'
import { bytes, key, type EntityDesc } from '../store/index.js'

import { build, Form, leafOf, useForm, type Vals } from './form.js'
import { useApp } from './index.js'
import { Json } from './json.js'

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
		boxSizing: 'border-box',
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
	//
	// Its bottom edge sits **on** the sheet's top edge rather than above it, so
	// the two read as one thing that has slid up -- which is why there is no
	// border or radius along the bottom of either: a rounded edge at the bottom
	// of the viewport is a card, and this is not one.
	handle: {
		position: 'fixed',
		left: '50%',
		transform: 'translateX(-50%)',
		zIndex: 2147483001,
		boxSizing: 'border-box',
		background: back,
		color: dim,

		// Brighter than the panel's own lines, because this is the one element
		// that sits on a background it does not control. Against a white page
		// any dark edge reads; against a dark one `line` is a shade off the
		// ground and the handle dissolves into it -- and a handle nobody can
		// find is the whole panel gone.
		border: '1px solid #3f3f3f',
		borderBottom: 'none',
		borderRadius: '8px 8px 0 0',
		padding: '3px 22px 4px',
		cursor: 'pointer',
		font: '12px ui-monospace, SFMono-Regular, Menlo, monospace',
		lineHeight: '14px',
	},
	bar: {
		display: 'flex',
		gap: 3,
		alignItems: 'center',
		padding: '3px 4px',
		borderBottom: `1px solid ${line}`,
		flexWrap: 'wrap',
	},
	body: { flex: 1, minHeight: 0, display: 'flex' },

	// The form on the left and the answer on the right, each scrolling on its
	// own. Stacked, the form pushed the table off the bottom of a sheet that is
	// already short, and a table you have to scroll past a form to reach is a
	// table you stop looking at.
	pane: {
		width: 260,
		flex: 'none',
		overflow: 'auto',
		padding: 4,
		borderRight: `1px solid ${line}`,
		display: 'flex',
		flexDirection: 'column',
		gap: 4,
	},
	seen: { flex: 1, minWidth: 0, overflow: 'auto', padding: 4 },

	// The top edge, which is where a sheet is resized from. It is its own
	// element rather than a CSS `resize`, which cannot grow a thing anchored to
	// the bottom of the viewport: dragging that handle moves the wrong edge.
	grip: {
		position: 'absolute',
		left: 0,
		right: 0,
		top: -3,
		height: 7,
		cursor: 'ns-resize',
		touchAction: 'none',
	},
	table: { borderCollapse: 'collapse', whiteSpace: 'nowrap', width: 'max-content' },
	th: {
		textAlign: 'left',
		padding: '1px 10px 1px 0',
		borderBottom: `1px solid ${line}`,
		color: dim,
		fontWeight: 'normal',
		position: 'sticky',
		top: 0,
		background: back,
	},
	td: { padding: '1px 10px 1px 0', borderBottom: `1px solid ${line}`, verticalAlign: 'top' },
	input: {
		background: '#101010',
		color: ink,
		border: `1px solid ${line}`,
		borderRadius: 3,
		padding: '5px 8px',
		font: 'inherit',
	},

	// The same box, sized to be hit rather than to be read past. What pays for
	// it is the space around things and not the sheet: a bar of taller buttons
	// with less between them is the height it was.
	press: {
		background: '#101010',
		color: ink,
		border: `1px solid ${line}`,
		borderRadius: 3,
		padding: '7px 14px',
		font: 'inherit',
		lineHeight: '16px',
		cursor: 'pointer',
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
	// Over the header rather than in the flow, so that reading a hidden
	// column's name does not move the ones that are not hidden -- and *above*
	// it, because below is where the rows are and covering the first of them
	// to read a column name trades one thing hidden for another.
	over: {
		position: 'absolute',
		bottom: '100%',
		left: 0,
		zIndex: 1,
		background: '#101010',
		border: `1px solid ${line}`,
		borderRadius: 3,
		padding: '1px 5px',
		color: ink,
		whiteSpace: 'nowrap',
	},
	bad: { color: '#ff8b8b', whiteSpace: 'pre-wrap' },

	// Down the right side, and **under** the sheet. The panel is what somebody
	// is working in and the bytes are what they are looking at from it, so the
	// panel stays reachable: a full-screen editor over the top of it means
	// closing the editor to do anything at all.
	//
	// Its width is its content's -- sixteen bytes of hex, the offsets and the
	// text -- because that is a fixed number of columns and stretching it to
	// the viewport puts a screen of nothing between the hex and the text it is
	// read against.
	side: {
		position: 'fixed',
		top: 0,
		right: 0,
		bottom: 0,
		zIndex: 2147482999,
		width: 'max-content',
		maxWidth: '100vw',
		boxSizing: 'border-box',
		background: back,
		color: ink,
		borderLeft: `1px solid ${line}`,
		font: '12px ui-monospace, SFMono-Regular, Menlo, monospace',
		display: 'flex',
		flexDirection: 'column',
	},
	rule: { color: dim, lineHeight: '18px', whiteSpace: 'pre', margin: 0, flex: 'none' },
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

	/** Where a resize started, for as long as one is happening. */
	const grip = useRef<{ y: number; height: number } | undefined>(undefined)

	// Up here rather than in the table that opened it: see [View.raw], and also
	// because a row scrolled out from under an open editor should not take the
	// editor with it.
	const [raw, setRaw] = useState<Raw>()

	// Where following edges has been, so that going back is going back rather
	// than starting over. It is state and not history: the panel is a window on
	// a page that has its own back button, and taking that one over would be
	// answering a question nobody asked it.
	const [trail, setTrail] = useState<{ typeName: string; id: string }[]>([])
	const looking = trail[trail.length - 1]

	const shown = kept.tab === 'get' ? gets : lists
	const entity = shown.find((v) => v.typeName === kept.entity) ?? shown[0]

	const transport = ungated && props.ungated !== undefined ? props.ungated : app.queries.raw

	/** look is what following an edge does: the Get tab, on the row it named. */
	const look = useCallback(
		(typeName: string, id: string) => {
			setTrail((v) => [...v, { typeName, id }])
			keep({ tab: 'get', entity: typeName })
		},
		[keep],
	)

	/** back is one edge the way it was come by. */
	const back = useCallback(() => {
		setTrail((v) => {
			const now = v.slice(0, -1)
			const to = now[now.length - 1]
			keep(to === undefined ? {} : { entity: to.typeName })

			return now
		})
	}, [keep])

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
				{/*
					Drag the top edge. A pointer capture rather than listeners
					on the window: the pointer leaves this seven-pixel strip on
					the first move, and without the capture the drag ends there.
				*/}
				<div
					style={style.grip}
					role="separator"
					aria-label="resize"
					onPointerDown={(e) => {
						// Guarded because it is not everywhere: jsdom has no
						// pointer capture, and a panel that throws on mousedown
						// in a test suite is a panel nobody tests.
						try {
							e.currentTarget.setPointerCapture(e.pointerId)
						} catch {
							// Then the drag ends where the pointer leaves, which
							// is worse and is not broken.
						}

						grip.current = { y: e.clientY, height: kept.height }
					}}
					onPointerMove={(e) => {
						const from = grip.current
						if (from === undefined) return

						// Up is taller, because the sheet grows from the
						// bottom of the viewport. Bounded below by the bar,
						// which is the smallest thing that is still a panel,
						// and above by the viewport, since a sheet taller than
						// the page is a sheet with a handle nobody can reach.
						setKept((old) => ({
							...old,
							height: Math.min(Math.max(from.height + (from.y - e.clientY), 80), window.innerHeight - 40),
						}))
					}}
					onPointerUp={() => {
						grip.current = undefined
						// Written once, at the end: a drag is a hundred moves
						// and storage is not where a hundred of anything goes.
						keep({})
					}}
				/>

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

					{trail.length > 0 && kept.tab === 'get' && (
						<button
							type="button"
							style={{ ...style.input, cursor: 'pointer' }}
							onClick={back}
							aria-label="back"
						>
							←
						</button>
					)}

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
				</div>

				<div style={style.body}>
					{entity === undefined ? (
						<p style={style.bad}>this app declares no entity that answers here.</p>
					) : kept.tab === 'store' ? (
						<Held
							entity={entity}
							hidden={kept.hidden}
							keep={keep}
							look={look}
							raw={setRaw}
							entities={props.entities}
						/>
					) : kept.tab === 'get' ? (
						<Get
							key={`${entity.typeName}:${String(ungated)}`}
							entity={entity}
							entities={props.entities}
							transport={transport}
							hidden={kept.hidden}
							keep={keep}
							look={look}
							raw={setRaw}
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
							raw={setRaw}
						/>
					)}
				</div>
			</section>

			{/* A sibling of the sheet, so that it is above the handle too. */}
			{raw !== undefined && (
				<Hex
					name={raw.name}
					value={raw.value}
					settable={raw.settable}
					onClose={() => setRaw(undefined)}
					onSave={async (v) => {
						await raw.save(v)
						setRaw(undefined)
					}}
				/>
			)}
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

	/**
	 * Open a bytes value over everything, which is the panel's to do and not a
	 * table's.
	 *
	 * A `position: fixed` element inside the sheet is stacked *within* the
	 * sheet, because the sheet has a `z-index` and so is a stacking context of
	 * its own -- so an overlay rendered down there cannot get above the panel's
	 * own handle however large a number it asks for. What it looked like was
	 * the handle floating in the middle of a full-screen editor.
	 */
	raw: (v: Raw) => void
}

/** Raw is a bytes value being looked at, and what saving it would do. */
interface Raw {
	name: string
	value: Uint8Array
	settable: boolean
	save: (v: Uint8Array) => Promise<void>
}

/**
 * List is what the server answers for a whole page of them.
 *
 * It pages as it is scrolled rather than on a button, because what somebody
 * does with a table is scroll it, and a `next` button is a cursor being managed
 * by hand for no reason. The cursor is still on the screen -- the form's own
 * `after` box holds whatever the last page answered with -- so what the panel
 * is doing is visible and can be typed over.
 */
function List(props: View & { transport: Transport }): ReactNode {
	const method = props.entity.service?.method.list as DescMethodUnary<DescMessage, DescMessage>
	const [vals, setVals] = useForm(method.input)

	const [rows, setRows] = useState<Record<string, unknown>[]>([])
	const [next, setNext] = useState('')
	const [err, setErr] = useState<string>()

	// A ref and not state: what it guards is the scroll handler, which fires
	// again before a re-render could have told it anything.
	const busy = useRef(false)

	const ask = useCallback(
		async (after: string, more: boolean) => {
			if (busy.current) return
			busy.current = true
			setErr(undefined)
			try {
				const req = build(method.input, vals)

				// `size` is not on the form -- see the `skip` below -- so it is
				// this that decides it. Big enough that the first answer fills
				// a sheet and asks for the second by being scrolled.
				req.size = 50
				if (after !== '') req.after = after

				const v = (await call(props.entity, props.transport, 'list', req)) as {
					items: Record<string, unknown>[]
					next: string
				}

				setRows((old) => (more ? [...old, ...v.items] : v.items))
				setNext(v.next)

				// The cursor, put where somebody would have typed it -- but
				// only when it was scrolling that asked. A panel that pages
				// invisibly is a panel whose paging cannot be checked against
				// what the server answered; one that writes the cursor back on
				// the *first* page turns the next press of `ask` into a second
				// page nobody asked for.
				if (more) setVals({ ...vals, leaf: { ...vals.leaf, after: v.next } })
			} catch (e) {
				if (!more) setRows([])
				setErr(String(e))
			} finally {
				busy.current = false
			}
		},
		[method, vals, props.entity, props.transport, setVals],
	)

	useEffect(() => {
		void ask('', false)
		// On the entity and the path, not on every keystroke in the form.
	}, [props.entity, props.transport]) // eslint-disable-line react-hooks/exhaustive-deps

	/** more is the next page, if the last answer said there is one. */
	const more = useCallback(
		(e: { currentTarget: HTMLElement }) => {
			if (next === '' || busy.current) return

			const el = e.currentTarget
			if (el.scrollTop + el.clientHeight < el.scrollHeight - 64) return

			void ask(next, true)
		},
		[next, ask],
	)

	return (
		<>
			<Pane desc={method.input} vals={vals} onChange={setVals} onAsk={() => void ask('', false)} what="ask" skip={['size']}>
				{err !== undefined && <p style={style.bad}>{err}</p>}
				<span style={{ color: dim }}>
					{rows.length} row{rows.length === 1 ? '' : 's'}
					{next === '' ? ', all of them' : ', scroll for more'}
				</span>
			</Pane>

			<div style={style.seen} onScroll={more}>
				<Table label="served" rows={rows} onSaved={() => void ask('', false)} {...props} />
			</div>
		</>
	)
}

/**
 * Pane is a request form and the button that sends it, down the left side.
 *
 * The form is the request message, whatever it happens to be -- a `List` takes
 * filters and a cursor, a `Get` takes a reference and a select, and both are
 * read off the descriptor rather than written out here. An RPC that grows a
 * field grows a box.
 */
function Pane(props: {
	desc: DescMessage
	vals: Vals
	onChange: (v: Vals) => void
	onAsk: () => void
	what: string
	skip?: readonly string[]
	children?: ReactNode
}): ReactNode {
	return (
		<aside style={style.pane}>
			<button type="button" style={style.press} onClick={props.onAsk}>
				{props.what}
			</button>

			<Form
				desc={props.desc}
				vals={props.vals}
				onChange={props.onChange}
				{...(props.skip === undefined ? {} : { skip: props.skip })}
			/>

			{props.children}
		</aside>
	)
}

/**
 * Get is one row, asked for however the request allows.
 *
 * A reference is a `oneof` -- an identifier, or a slug where the schema
 * declared one -- and the form is that, so this is not an identifier box that
 * happens to be the case everybody uses.
 *
 * What comes back is shown as JSON rather than as a table of one row. A table
 * is for comparing rows and there is one; what is worth seeing here is the
 * whole document, nesting and all.
 */
function Get(props: View & { transport: Transport; id?: string }): ReactNode {
	const method = props.entity.service?.method.get as DescMethodUnary<DescMessage, DescMessage>
	const [vals, setVals] = useForm(method.input)

	const [row, setRow] = useState<unknown>()
	const [err, setErr] = useState<string>()

	const ask = useCallback(
		async (v: Vals) => {
			setErr(undefined)
			setRow(undefined)
			try {
				const req = build(method.input, v)
				setRow(await call(props.entity, props.transport, 'get', req))
			} catch (e) {
				setErr(String(e))
			}
		},
		[method, props.entity, props.transport],
	)

	// Following an edge is an identifier arriving from above: it fills the form
	// the way somebody would have and then asks, so what is on the screen is
	// what was sent.
	useEffect(() => {
		if (props.id === undefined) return

		const v: Vals = {
			leaf: { 'ref.id': props.id },
			pick: { 'ref.key': 'id' },
			many: {},
		}

		setVals(v)
		void ask(v)
	}, [props.id, ask]) // eslint-disable-line react-hooks/exhaustive-deps

	return (
		<>
			<Pane desc={method.input} vals={vals} onChange={setVals} onAsk={() => void ask(vals)} what="look up">
				{err !== undefined && <p style={style.bad}>{err}</p>}
			</Pane>

			<div style={style.seen}>{row !== undefined && <Json value={row} />}</div>
		</>
	)
}

/** Held is what the store holds, which is not the same question. */
function Held(props: View): ReactNode {
	const app = useApp()
	const rows = app.store.all(props.entity.typeName) as unknown as Record<string, unknown>[]

	return (
		<div style={style.seen}>
			<Table label="held" rows={rows} {...props} />
		</div>
	)
}

/**
 * Table is the rows, as a table.
 *
 * The columns are the message's fields, in the order the schema declares them,
 * and the whole of it scrolls sideways rather than wrapping: a row is wide
 * because the entity is, and a wrapped one cannot be read across.
 *
 * A value is edited by double-clicking it, and only one is being edited at a
 * time: opening a second closes the first without saving it. Two half-typed
 * edits in two rows is a state where the next click is a guess about which one
 * it meant.
 */
function Table(props: View & { label: string; rows: Record<string, unknown>[]; onSaved?: () => void }): ReactNode {
	const app = useApp()
	const fields = props.entity.schema.fields
	const hidden = new Set(props.hidden[props.entity.typeName] ?? [])
	const refs = new Map((props.entity.refs ?? []).map((v) => [v.field, v.to]))
	const ids = new Set(props.entity.ids ?? [])

	const method = props.entity.service?.method.patch as DescMethodUnary<DescMessage, DescMessage> | undefined
	const settable = useMemo(
		() => new Set(method === undefined ? [] : patchable(method, props.entity.version).map((f) => f.localName)),
		[method, props.entity.version],
	)

	const [edit, setEdit] = useState<{ at: string; field: string; value: string; err?: string }>()

	/**
	 * The last column somebody turned, so that shift reaches back to it.
	 *
	 * A ref rather than state: nothing on the screen depends on it, and the
	 * click that reads it is the same one that writes it.
	 */
	const from = useRef<string | undefined>(undefined)

	/**
	 * toggle turns a column off, or a run of them.
	 *
	 * Shift takes everything from the last one turned to this one, which is
	 * what a list of checkboxes means everywhere else -- and a wide entity is
	 * twenty columns of which somebody wants three, so without it the gesture
	 * is seventeen clicks.
	 *
	 * The run is given **this** column's new state rather than each being
	 * flipped: a range that flipped would turn some on and some off, which is
	 * not what anybody dragging a selection means.
	 */
	const toggle = (name: string, span: boolean): void => {
		const names = fields.map((f) => f.localName)
		const show = hidden.has(name)

		let run = [name]
		if (span && from.current !== undefined) {
			const a = names.indexOf(from.current)
			const b = names.indexOf(name)
			if (a >= 0 && b >= 0) run = names.slice(Math.min(a, b), Math.max(a, b) + 1)
		}

		const now = new Set(hidden)
		for (const v of run) {
			if (show) {
				now.delete(v)
			} else {
				now.add(v)
			}
		}

		from.current = name
		props.keep({ hidden: { ...props.hidden, [props.entity.typeName]: [...now] } })
	}

	/**
	 * save writes one field of one row, through `Patch`.
	 *
	 * Through `Queries.call` rather than the raw transport, which is the
	 * opposite of every read here and for the same reason: a write is supposed
	 * to reach the store, so that every screen drawing that row is right
	 * afterwards. Watching the page change is most of what this is for.
	 *
	 * The version travels as the precondition. A row read a minute ago and
	 * patched now is refused rather than applied over somebody else's write,
	 * and seeing that refusal is worth more than a panel that always wins.
	 */
	const save = async (row: Record<string, unknown>, f: DescField, v: unknown): Promise<boolean> => {
		if (method === undefined) return false

		try {
			const req: Record<string, unknown> = {
				ref: { key: { case: 'id', value: idBytes(row.id) } },
				[f.localName]: v,
			}
			if (props.entity.version !== undefined) req[props.entity.version] = row[props.entity.version]

			await app.queries.call(method, create(method.input, req as never))
			props.onSaved?.()

			return true
		} catch (e) {
			// Reported where it was typed, and thrown on so that whoever
			// asked knows the value on the screen is not what is stored.
			setEdit((old) => (old === undefined ? old : { ...old, err: String(e) }))
			throw e
		}
	}

	return (
		<>
			<table style={style.table} aria-label={props.label}>
				<thead>
					<tr>
						{fields.map((f) => (
							<th key={f.localName} style={style.th}>
								<Head name={f.localName} shown={!hidden.has(f.localName)} toggle={toggle} />
							</th>
						))}
					</tr>
				</thead>
				<tbody>
					{props.rows.map((row, i) => {
						const at = rowAt(row, i)

						return (
							<tr key={at}>
								{fields.map((f) => (
									<td
										key={f.localName}
										style={style.td}
										onDoubleClick={
											settable.has(f.localName)
												? () => setEdit({ at, field: f.localName, value: written(row[f.localName]) })
												: undefined
										}
									>
										{hidden.has(f.localName) ? null : edit?.at === at &&
										  edit.field === f.localName ? (
											<Editing
												value={edit.value}
												err={edit.err}
												onChange={(v) =>
													setEdit((old) => (old === undefined ? old : { ...old, value: v }))
												}
												onCancel={() => setEdit(undefined)}
												onSave={() => {
													void save(row, f, leafOf(f, edit.value)).then(
														() => setEdit(undefined),
														() => undefined,
													)
												}}
											/>
										) : (
											<Cell
												value={row[f.localName]}
												field={f}
												id={ids.has(f.localName)}
												to={refs.get(f.localName)}
												look={props.look}
												onRaw={(v) =>
													props.raw({
														name: f.localName,
														value: v,
														settable: settable.has(f.localName),
														save: async (w) => void (await save(row, f, w)),
													})
												}
											/>
										)}
									</td>
								))}
							</tr>
						)
					})}
				</tbody>
			</table>

		</>
	)
}

/** rowAt is what names a row on the screen, which is its identifier. */
function rowAt(row: Record<string, unknown>, i: number): string {
	const v = row.id

	return v instanceof Uint8Array ? key(v) : typeof v === 'string' && v !== '' ? v : `#${String(i)}`
}

/** idBytes is a row's identifier, from either shape it arrives in. */
function idBytes(v: unknown): Uint8Array {
	return v instanceof Uint8Array ? v : bytes(typeof v === 'string' ? v : '')
}

/** written is a value as somebody would have typed it into a box. */
function written(v: unknown): string {
	if (v === undefined || v === null) return ''
	if (v instanceof Uint8Array) return idOf(v) ?? key(v)
	if (typeof v === 'object') return ''

	return String(v)
}

/** Editing is one cell, being typed into. */
function Editing(props: {
	value: string
	err: string | undefined
	onChange: (v: string) => void
	onCancel: () => void
	onSave: () => void
}): ReactNode {
	return (
		<span style={{ display: 'inline-flex', gap: 4, alignItems: 'center' }}>
			<input
				aria-label="editing"
				style={style.input}
				value={props.value}
				autoFocus
				spellCheck={false}
				onChange={(e) => props.onChange(e.target.value)}
				onKeyDown={(e) => {
					// The two keys a box like this already means, so that a
					// value can be corrected without the mouse coming back.
					if (e.key === 'Enter') props.onSave()
					if (e.key === 'Escape') props.onCancel()
				}}
			/>
			<button type="button" style={style.press} onClick={props.onSave}>
				save
			</button>
			<button type="button" style={style.press} onClick={props.onCancel}>
				cancel
			</button>
			{props.err !== undefined && <span style={style.bad}>{props.err}</span>}
		</span>
	)
}

/**
 * Head is one column's name and the box that turns it off.
 *
 * The box is beside the name rather than in a list somewhere else, because the
 * question "is this column worth its width" is asked while looking at the
 * column. Turned off, the name goes and the box stays -- which is the point:
 * the column collapses to the width of a checkbox instead of leaving a gap
 * where something used to be.
 *
 * Hovering it brings the name back, over the rows rather than in the flow, so
 * finding a column that was turned off does not move everything that was not.
 */
function Head(props: { name: string; shown: boolean; toggle: (name: string, span: boolean) => void }): ReactNode {
	const [over, setOver] = useState(false)

	// Whether shift was down, taken from the click rather than from the change
	// it causes: a `change` event is an `Event` and carries no modifiers, and
	// reading `nativeEvent` for one is reading a field that is there in a
	// browser and not in a test.
	const span = useRef(false)

	return (
		<span
			style={{ position: 'relative', display: 'inline-flex', gap: 4, alignItems: 'baseline' }}
			onMouseEnter={() => setOver(true)}
			onMouseLeave={() => setOver(false)}
		>
			<input
				type="checkbox"
				aria-label={props.name}
				checked={props.shown}
				onClick={(e) => (span.current = e.shiftKey)}
				onChange={() => props.toggle(props.name, span.current)}
			/>
			{props.shown ? (
				<span>{props.name}</span>
			) : (
				over && (
					<span role="tooltip" style={style.over}>
						{props.name}
					</span>
				)
			)}
		</span>
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

	/** Whether the schema declared this field a uuid. */
	id: boolean

	/** The entity this field names, for a field that names one. */
	to: string | undefined
	look: (typeName: string, id: string) => void
	onRaw: (v: Uint8Array) => void
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

	// Bytes, which the **descriptor** says and the value does not: a row off
	// the wire carries a `Uint8Array` and the same row out of the store carries
	// the hex the store keys by, and asking the value which it is would answer
	// differently for the same field on two tabs.
	const raw = rawOf(props.field, v)
	if (raw !== undefined) {
		if (raw.byteLength === 0) return <span style={{ color: dim }}>—</span>

		// Whether it is an identifier is the **schema's** to say and not the
		// value's -- `EntityDesc.ids` says why, and it is not only the field
		// called `id`: a trail row names six and none of them is called that.
		//
		// Written as a uuid and not as a `pdid`, which is the same sixteen
		// bytes with two more claims made about them. A row minted somewhere
		// else, or by something that used a plain v7, is still the uuid the
		// column holds -- and `pdid.from` refusing it would put "16 bytes"
		// where a value everybody can read was sitting.
		if (props.id && raw.byteLength === 16) return <span>{uuidOf(raw)}</span>

		// Not spelled out. A marshalled patch is a column as wide as the
		// document it holds and hex is the least readable thing that column
		// could be full of, so it says how big it is -- which is what anybody
		// reads at a glance -- and opens on a click.
		return (
			<button type="button" style={style.link} onClick={() => props.onRaw(raw)}>
				{raw.byteLength} bytes
			</button>
		)
	}

	if (typeof v === 'object' && (v as { $typeName?: string }).$typeName === 'google.protobuf.Timestamp') {
		return <span>{timestampDate(v as never).toISOString()}</span>
	}

	if (typeof v === 'object') return <span>{JSON.stringify(v)}</span>

	return <span>{String(v)}</span>
}

/**
 * Hex is a bytes value, down the right side.
 *
 * Three columns because that is what every tool that has ever shown bytes uses
 * and it is not a style: the offset says where you are, the hex is what is
 * there, and the text is how anybody tells at a glance whether they are looking
 * at a string, a protobuf or noise. They are read across, so they are computed
 * from the **same lines** -- the offsets are the running byte count of what has
 * been typed, so a line somebody shortened moves the ones under it rather than
 * lying about them.
 *
 * It sits under the panel rather than over it; `style.side` says why.
 *
 * Editable where the schema says the field is: `patchable` already knows, and a
 * box that takes typing for a field the server will refuse is a box that
 * teaches the wrong thing. What is typed is hex, because that is what is shown;
 * anything that is not a pair of hex digits is refused before it is sent, with
 * the position that is wrong.
 */
function Hex(props: {
	name: string
	value: Uint8Array
	settable: boolean
	onClose: () => void
	onSave: (v: Uint8Array) => Promise<void>
}): ReactNode {
	const [text, setText] = useState(() => spaced(props.value))
	const [err, setErr] = useState<string>()

	const parsed = useMemo(() => packed(text), [text])
	const changed = text !== spaced(props.value)

	const send = (): void => {
		if (typeof parsed === 'string') {
			setErr(parsed)

			return
		}

		setErr(undefined)
		props.onSave(parsed).catch((e: unknown) => setErr(String(e)))
	}

	return (
		<div
			style={style.side}
			role="dialog"
			aria-label={`${props.name} bytes`}
			// Escape closes it, and the div is focused on mount so that it
			// does without anything being clicked first.
			tabIndex={-1}
			ref={(el) => el?.focus()}
			onKeyDown={(e) => {
				if (e.key === 'Escape') props.onClose()
			}}
		>
			<div style={style.bar}>
				<strong>{props.name}</strong>
				<span style={{ color: dim }}>
					{props.value.byteLength} bytes{props.settable ? '' : ', read only'}
				</span>

				<span style={{ flex: 1 }} />

				{props.settable && (
					<button type="button" style={style.press} onClick={send} disabled={!changed}>
						save
					</button>
				)}
				<button type="button" style={style.press} onClick={props.onClose}>
					close
				</button>
			</div>

			{err !== undefined && <p style={{ ...style.bad, margin: '4px 6px' }}>{err}</p>}

			<div style={{ flex: 1, minHeight: 0, display: 'flex', overflow: 'auto', padding: '6px 10px', gap: 10 }}>
				<pre aria-label="offset" style={style.rule}>
					{offsets(text)}
				</pre>

				<textarea
					aria-label="hex"
					readOnly={!props.settable}
					spellCheck={false}
					value={text}
					onChange={(e) => setText(e.target.value)}
					// The width of a line and no more: sixteen pairs, three
					// gaps between the fours, and the borders.
					style={{
						...style.input,
						flex: 'none',
						width: '51ch',
						resize: 'none',
						lineHeight: '18px',
						whiteSpace: 'pre',
					}}
				/>

				{/*
					Not editable: two editors over one value is two answers
					about what was typed.
				*/}
				<pre aria-label="text" style={style.rule}>
					{printable(text)}
				</pre>
			</div>
		</div>
	)
}

/**
 * spaced is bytes as hex, sixteen to a line and grouped in fours.
 *
 * The gap every four is the whole reason a hex dump is countable: sixteen pairs
 * in a row have to be counted one at a time, and four groups of four are read.
 */
function spaced(v: Uint8Array): string {
	const out: string[] = []
	for (let i = 0; i < v.length; i += 16) {
		const line = Array.from(v.slice(i, i + 16), (b) => b.toString(16).padStart(2, '0'))
		const fours: string[] = []
		for (let j = 0; j < line.length; j += 4) fours.push(line.slice(j, j + 4).join(' '))

		out.push(fours.join('  '))
	}

	return out.join('\n')
}

/** digits is one line's hex, with everything that is not a digit taken out. */
function digits(line: string): string {
	return line.replace(/\s+/g, '')
}

/**
 * offsets is where each line starts, counted from what is on it.
 *
 * From the text rather than from the line number, because a line somebody
 * edited is not sixteen bytes any more and an offset that assumed it was would
 * name the wrong byte for the whole rest of the document.
 */
function offsets(text: string): string {
	let at = 0

	return text
		.split('\n')
		.map((l) => {
			const was = at
			at += digits(l).length >> 1

			return was.toString(16).padStart(8, '0')
		})
		.join('\n')
}

/** packed is [spaced] back, or what is wrong with it. */
function packed(text: string): Uint8Array | string {
	const t = digits(text)
	if (t.length % 2 !== 0) return `hex: ${String(t.length)} digits is half a byte short`

	const out = new Uint8Array(t.length / 2)
	for (let i = 0; i < out.length; i++) {
		const pair = t.slice(i * 2, i * 2 + 2)
		if (!/^[0-9a-fA-F]{2}$/.test(pair)) return `hex: ${JSON.stringify(pair)} at byte ${String(i)} is not two hex digits`

		out[i] = Number.parseInt(pair, 16)
	}

	return out
}

/**
 * printable is the same bytes as text, on the same lines.
 *
 * Line by line rather than sixteen at a time, so that the three columns stay
 * read-across while somebody is in the middle of typing.
 */
function printable(text: string): string {
	return text
		.split('\n')
		.map((l) => {
			const t = digits(l)
			let out = ''
			for (let i = 0; i + 1 < t.length; i += 2) {
				const b = Number.parseInt(t.slice(i, i + 2), 16)
				out += Number.isNaN(b) || b < 0x20 || b >= 0x7f ? '.' : String.fromCharCode(b)
			}

			return out
		})
		.join('\n')
}

/** rawOf is a bytes field's value, whichever of its two shapes it arrived in. */
function rawOf(f: DescField, v: unknown): Uint8Array | undefined {
	if (f.fieldKind !== 'scalar' || f.scalar !== ScalarType.BYTES) return undefined
	if (v instanceof Uint8Array) return v

	// The store's hex. Empty is a field nobody set, and a zero-length editor
	// over it would be a dialog about nothing.
	return typeof v === 'string' && v !== '' ? bytes(v) : undefined
}

/** uuidOf is sixteen bytes written the way a uuid is written. */
function uuidOf(v: Uint8Array): string {
	const h = key(v)

	return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

/**
 * idOf reads an identifier out of whatever shape it arrived in.
 *
 * Which fields are identifiers is `EntityDesc.ids`, from the schema. This is
 * only the reading, and it refuses bytes that are not one of ours -- an
 * identifier minted by another deployment is bytes here, and saying so is
 * better than printing a uuid nothing answers to.
 */
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
