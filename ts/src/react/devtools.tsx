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
	fromJson,
	toJson,
	ScalarType,
	type DescField,
	type DescMessage,
	type DescMethodUnary,
	type DescService,
} from '@bufbuild/protobuf'
import { timestampDate } from '@bufbuild/protobuf/wkt'
import { createClient, type Transport } from '@connectrpc/connect'
import {
	useCallback,
	useEffect,
	useMemo,
	useRef,
	useState,
	type CSSProperties,
	type MouseEvent,
	type ReactNode,
} from 'react'

import * as pdid from '../pdid/index.js'
import { bytes, key, type EntityDesc } from '../store/index.js'

import { Code, type MonacoLike } from './code.js'

export { Code, type MonacoLike }
import { build, Form, leafOf, useForm, type Vals } from './form.js'
import { jsonSchemaOf } from './jsonschema.js'
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

	/**
	 * An editor for the document a `Get` answers with, which the app brings.
	 *
	 *     import * as monaco from 'monaco-editor'
	 *     <Devtools entities={entities} monaco={monaco} />
	 *
	 * Absent is the ordinary case and the panel is whole without it: the same
	 * document is shown as coloured text that cannot be edited. What it buys is
	 * what a schema buys -- completion over the fields the message actually
	 * has, a hover saying what each is, and a red line under a typo rather than
	 * a refusal from the server -- because the descriptor is right there and a
	 * JSON Schema is what a descriptor already says. See `./jsonschema.ts`.
	 *
	 * It is handed in rather than imported because payday cannot import it:
	 * fifteen megabytes into every app that mounts the panel, and web worker
	 * URLs only the app's own bundler can write. `./code.tsx` says the rest.
	 */
	monaco?: MonacoLike
}

/** Kept is what the panel remembers between reloads. */
interface Kept {
	open: boolean
	height: number
	entity: string
	tab: Tab
	/** Which columns are hidden, per entity. Absent is "all of them shown". */
	hidden: Record<string, string[]>

	/** Whether a document is shown against what the server answered with. */
	diff: boolean

	/**
	 * Which edge columns are showing the identifier rather than the name, per
	 * entity. Absent is "all of them by name", which is the useful default:
	 * `acme` is what somebody is looking for and the uuid is what they would
	 * have to translate it into first.
	 */
	asId: Record<string, string[]>

	/**
	 * Whether a timestamp is shown as UTC rather than in the viewer's zone.
	 *
	 * One switch and not one per column, which is what the `(id)` toggle is:
	 * that one is per column because only an edge has two readings and which
	 * you want differs by column. A zone is the same question about every
	 * timestamp at once, and the answer belongs to the person rather than to
	 * the field.
	 *
	 * Off by default -- the request log beside this panel is a wall clock, and
	 * reading a row against it is what somebody is doing here. What the column
	 * holds is UTC either way; see `config.OtelConfig` and the driver.
	 */
	utc: boolean
}

type Tab = 'list' | 'get' | 'store'

/** How a query is read. */
type How = 'text' | 'regex' | 'fuzzy'

/** Find is what is being looked for, and how. */
interface Find {
	q: string
	how: How
}

/**
 * useOver is where the thing being hovered is, for [Over].
 *
 * A rectangle rather than a boolean because the label is positioned in the
 * viewport -- see `style.over` -- so it has to be told where to go.
 */
function useOver(): [DOMRect | undefined, { onMouseEnter: (e: MouseEvent<HTMLElement>) => void; onMouseLeave: () => void }] {
	const [at, setAt] = useState<DOMRect>()

	return [
		at,
		{
			onMouseEnter: (e) => setAt(e.currentTarget.getBoundingClientRect()),
			onMouseLeave: () => setAt(undefined),
		},
	]
}

/**
 * Over is a label above whatever [useOver] measured.
 *
 * Anchored by whichever edge it will not run off. A label on the last column
 * of a wide table, or on the rightmost of the mode buttons, is a label that
 * starts near the edge of the window and grows past it -- so past the middle
 * it grows leftwards from the right edge instead, which needs no measurement
 * of the label itself.
 */
function Over(props: { at: DOMRect | undefined; children: ReactNode }): ReactNode {
	const at = props.at
	if (at === undefined) return null

	const side =
		at.left > window.innerWidth / 2
			? { right: Math.max(window.innerWidth - at.right, 0) }
			: { left: at.left }

	return (
		<span role="tooltip" style={{ ...style.over, ...side, bottom: window.innerHeight - at.top + 4 }}>
			{props.children}
		</span>
	)
}

/** The three, in the order they are offered. */
const modes = [
	['fuzzy', '≈', 'the letters in order'],
	['text', 'ab', 'the letters as typed'],
	['regex', '.*', 'a pattern'],
] as const

/**
 * Modes is which of the three a query is read as.
 *
 * One control and not three, because it is one of three and not three
 * switches: separate boxes read as things that can each be turned off, and
 * pressing the one that is already on does nothing -- which is exactly what
 * "wait, is this a toggle?" looks like.
 */
function Modes(props: { find: Find; onChange: (v: Find) => void }): ReactNode {
	return (
		<span style={style.seg} role="group" aria-label="how to match">
			{modes.map(([how, glyph, why]) => (
				<Mode
					key={how}
					how={how}
					glyph={glyph}
					why={why}
					on={props.find.how === how}
					onPick={() => props.onChange({ ...props.find, how })}
				/>
			))}
		</span>
	)
}

function Mode(props: {
	how: How
	glyph: string
	why: string
	on: boolean
	onPick: () => void
}): ReactNode {
	const [over, bind] = useOver()

	return (
		<>
			<button
				type="button"
				// Named for what it does to the search, because a panel showing
				// bytes has a `text` column and a button that edits them as
				// text, and three things called `text` is three things to
				// disambiguate every time one of them is looked for.
				aria-label={`match ${props.how}`}
				aria-pressed={props.on}
				style={{ ...style.chip, ...(props.on ? style.on : {}) }}
				// A click picks it and does not focus it. A ring left on the
				// one that was pressed reads as a state of its own, on a
				// control where the state is already the fill.
				onMouseDown={(e) => e.preventDefault()}
				onClick={props.onPick}
				{...bind}
			>
				{props.glyph}
			</button>

			<Over at={over}>
				{props.how} — {props.why}
			</Over>
		</>
	)
}

/** Hits are the stretches of a value a query matched, in order. */
type Hits = readonly (readonly [number, number])[]

/**
 * matcher answers **where** a query matched, or says what is wrong with it.
 *
 * Where and not whether, because a match nobody can see is a row somebody has
 * to work out the reason for -- and that is most of what makes fuzzy usable at
 * all: the letters it matched are lit up, so a row that looks like a false
 * positive can be read as one at a glance instead of being taken on trust.
 *
 * Three ways because they are three questions. `fuzzy` is the editor gesture --
 * the letters in order and anything between them -- and is the default: it is a
 * superset of `text`, so it never hides what a substring would have found, and
 * four characters somebody remembers reach a uuid. `text` is for narrowing when
 * fuzzy has been too generous. `regex` is for a shape, and its mistakes are
 * typos in the pattern, so the error is shown rather than swallowed into "no
 * rows matched" -- which is what an unreadable pattern otherwise looks like.
 */
function matcher(find: Find): ((s: string) => Hits | undefined) | string {
	const q = find.q
	if (q === '') return () => []

	if (find.how === 'regex') {
		let re: RegExp
		try {
			re = new RegExp(q, 'gi')
		} catch (e) {
			return String(e)
		}

		return (s) => {
			const out: [number, number][] = []
			re.lastIndex = 0

			let m: RegExpExecArray | null
			while ((m = re.exec(s)) !== null) {
				// A pattern that can match nothing -- `a*` -- would otherwise
				// walk this loop forever at the same index.
				if (m[0] === '') {
					re.lastIndex++
					continue
				}

				out.push([m.index, m.index + m[0].length])
			}

			return out.length === 0 ? undefined : out
		}
	}

	const want = q.toLowerCase()
	if (find.how === 'text') {
		return (s) => {
			const v = s.toLowerCase()
			const out: [number, number][] = []
			for (let at = v.indexOf(want); at >= 0; at = v.indexOf(want, at + want.length)) {
				out.push([at, at + want.length])
			}

			return out.length === 0 ? undefined : out
		}
	}

	return (s) => {
		const v = s.toLowerCase()
		const out: [number, number][] = []

		let at = 0
		for (const c of want) {
			at = v.indexOf(c, at)
			if (at < 0) return undefined

			// Adjacent letters are one stretch, so `abc` found in `abc` lights
			// up as a word rather than as three boxes touching.
			const last = out[out.length - 1]
			if (last !== undefined && last[1] === at) {
				last[1] = at + 1
			} else {
				out.push([at, at + 1])
			}

			at++
		}

		return out
	}
}

/**
 * useNames resolves references to what they are called, once each.
 *
 * Through `call` and never through `Queries`, like every other read here: what
 * this is for is looking at what the server says, and a lookup that filled the
 * store would make the served tab and the store tab agree by writing to one of
 * them.
 */
function useNames(entities: readonly EntityDesc[], transport: Transport): Names {
	const app = useApp()
	const [known, setKnown] = useState<Record<string, string>>({})

	// A ref and not state: it guards a fetch that is started from an effect,
	// and the guard has to hold before the state that would report it lands.
	const asked = useRef(new Set<string>())

	const of = useCallback(
		(typeName: string, id: string): string | undefined => {
			const e = entities.find((v) => v.typeName === typeName)
			if (e?.alias === undefined) return undefined

			// The store first, and free: a row it already holds is a row
			// nobody has to be asked about again.
			const row = app.store.row(typeName, bytes(id)) as Record<string, unknown> | undefined
			const held = row?.[e.alias]
			if (typeof held === 'string' && held !== '') return held

			return known[`${typeName}:${id}`]
		},
		[entities, known, app.store],
	)

	const want = useCallback(
		(vs: readonly { typeName: string; id: string }[]) => {
			for (const v of vs) {
				const at = `${v.typeName}:${v.id}`
				if (asked.current.has(at)) continue

				const e = entities.find((w) => w.typeName === v.typeName)
				if (e?.alias === undefined || e.service?.method.get === undefined) continue

				asked.current.add(at)
				void call(e, transport, 'get', { ref: { key: { case: 'id', value: bytes(v.id) } } }).then(
					(row) => {
						const name = (row as Record<string, unknown>)[e.alias as string]
						if (typeof name !== 'string' || name === '') return

						setKnown((old) => ({ ...old, [at]: name }))
					},
					() => {
						// A row this caller may not see is a row with no name
						// to show, which is the wall answering and not a
						// failure. It stays asked, so it is asked once.
					},
				)
			}
		},
		[entities, transport],
	)

	return { of, want }
}

/**
 * Names is what a reference is called, for a table that would rather show that.
 *
 * A `List` answers references and not rows: the neighbour arrives with its
 * identifier set and nothing else, because the request has no `select` to ask
 * for more. So the name has to be fetched, and this is the cache that makes
 * that one call per distinct row rather than one per cell -- fifty robots in
 * one tenant is one `Get`.
 */
interface Names {
	/** The name, or undefined until it is known. */
	of(typeName: string, id: string): string | undefined

	/** Ask for these, if they are not already known. */
	want(vs: readonly { typeName: string; id: string }[]): void
}

/** Step is one row followed into, and the screen it was followed from. *//**
 * Names is what a reference is called, for a table that would rather show that.
 *
 * A `List` answers references and not rows: the neighbour arrives with its
 * identifier set and nothing else, because the request has no `select` to ask
 * for more. So the name has to be fetched, and this is the cache that makes
 * that one call per distinct row rather than one per cell -- fifty robots in
 * one tenant is one `Get`.
 */
interface Names {
	/** The name, or undefined until it is known. */
	of(typeName: string, id: string): string | undefined

	/** Ask for these, if they are not already known. */
	want(vs: readonly { typeName: string; id: string }[]): void
}

/** Step is one row followed into, and the screen it was followed from. */
interface Step {
	from: { tab: Tab; entity: string }
	typeName: string
	id: string
}

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
	const zero: Kept = { open: false, height: 320, entity: '', tab: 'list', hidden: {}, diff: false, asId: {}, utc: false }
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

	// Everything that is not the answer, down the left. It used to be a bar
	// across the top and a pane under it, which is two places to look for one
	// thing: what entity, which of its three questions, what is being asked,
	// and what is being searched for are one column of decisions, and the
	// answer is what the rest of the sheet is for.
	pane: {
		width: 260,
		flex: 'none',
		minHeight: 0,
		borderRight: `1px solid ${line}`,
		display: 'flex',
		flexDirection: 'column',
	},

	// Fixed at the top, and the form between it and the foot is what scrolls:
	// a request with twenty filters should not push the entity picker off the
	// screen.
	top: { flex: 'none', padding: 4, display: 'flex', flexDirection: 'column', gap: 4 },

	// Three of one thing. Even widths rather than each taking what its name
	// needs, because they are not three separate buttons -- which is the same
	// reason `seg` exists for the match modes.
	tabs: { display: 'flex', border: `1px solid ${line}`, borderRadius: 3, overflow: 'hidden' },
	tab: {
		flex: 1,
		background: '#101010',
		color: dim,
		border: 'none',
		borderLeft: `1px solid ${line}`,
		padding: '6px 0',
		font: 'inherit',
		cursor: 'pointer',
	},

	// Pinned to the bottom, where a search box is.
	foot: {
		flex: 'none',
		padding: 4,
		borderTop: `1px solid ${line}`,
		display: 'flex',
		flexDirection: 'column',
		gap: 4,
	},
	// No padding at the top, which is not a nicety: a sticky header sticks to
	// the scrollport's padding edge, so a padded top is a strip above the
	// header that rows scroll through.
	seen: { flex: 1, minWidth: 0, overflow: 'auto', padding: '0 4px 4px' },

	// What the view puts in the middle of the sidebar, between the fixed head
	// and the fixed foot.
	asked: { flex: 1, minHeight: 0, overflow: 'auto', padding: 4, display: 'flex', flexDirection: 'column', gap: 4 },

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
		padding: '1px 8px',

		// The cell centres what is in it, and what is in it is a `flex` label
		// rather than an `inline-flex` one.
		//
		// Both halves matter. A table cell aligns its content by the baseline
		// unless told otherwise, and an inline-flex label's baseline is its
		// first item's -- the checkbox's, which is its bottom edge. So the
		// whole label hung 1.75px above the middle of the row, box and word
		// together, however well the two were centred against each other.
		verticalAlign: 'middle',
		// A rule between columns, because a row of a wide entity is read
		// **across** and nothing else says where one value stops. Without it
		// two empty cells in a row are one wide gap.
		borderRight: `1px solid ${line}`,
		borderBottom: `1px solid ${line}`,
		color: dim,
		fontWeight: 'normal',
		position: 'sticky',
		top: 0,
		background: back,
	},
	td: {
		padding: '1px 8px',
		borderRight: `1px solid ${line}`,
		borderBottom: `1px solid ${line}`,
		verticalAlign: 'top',
	},
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
	// Positioned in the **viewport** and not in the flow, which is not a
	// nicety: the table scrolls inside a container of its own, and an
	// absolutely positioned label is clipped by it -- so the name of a column
	// somebody turned off was drawn outside the pane and could not be read,
	// which is a tooltip that exists and does not work.
	//
	// Above whatever it names, because below is where the rows are and
	// covering the first of them to read a column name trades one thing hidden
	// for another.
	over: {
		position: 'fixed',
		zIndex: 2147483002,
		background: '#101010',
		border: `1px solid ${line}`,
		borderRadius: 3,
		padding: '1px 5px',
		color: ink,
		whiteSpace: 'nowrap',
		pointerEvents: 'none',
	},

	// One of three, drawn as one control. Three separate boxes read as three
	// switches somebody can turn off, and pressing the one that is already on
	// does nothing -- which is what "is this a toggle?" looks like.
	seg: {
		display: 'inline-flex',
		border: `1px solid ${line}`,
		borderRadius: 3,
		overflow: 'hidden',
	},
	bad: { color: '#ff8b8b', whiteSpace: 'pre-wrap' },

	// A word in a heading rather than a button in one: it is the size of the
	// text it sits beside, and what it does is say which of two things the
	// column is showing.
	tag: {
		background: 'none',
		border: 'none',
		padding: 0,
		margin: 0,
		font: 'inherit',
		cursor: 'pointer',
	},

	// The one control the browser draws in its own colours, which is a
	// light-grey box in a dark panel. `accentColor` is the whole of what it
	// takes, and it keeps the box a real checkbox.
	box: { accentColor: '#7db4ff', width: 13, height: 13, margin: 0, cursor: 'pointer' },

	// A checkbox and the word it belongs to, which is four places in this file
	// and was four answers -- two of them wrong the same way.
	//
	// `center` and not the default, which is `baseline`: a checkbox is a
	// replaced element, so its baseline is its bottom edge, and lining that up
	// with the text's lifts the box by however far the descenders hang. The
	// gap is here for the same reason it is one style -- a label written
	// without one puts the word against the box, and the next person writes a
	// third answer.
	//
	// `display` is left to the caller: one of these is a flex item in a row of
	// buttons and wants `flex`, the rest sit in a line of text and want
	// `inline-flex`.
	check: { alignItems: 'center', gap: 4, cursor: 'pointer' },

	// One glyph wide, and square, so three of them read as a set of switches
	// rather than as three more buttons in a row of buttons.
	chip: {
		background: '#101010',
		color: dim,
		border: 'none',
		borderLeft: `1px solid ${line}`,
		padding: 0,
		width: 26,
		height: 26,
		font: 'inherit',
		lineHeight: '24px',
		cursor: 'pointer',
	},
	on: { color: '#101010', background: '#7db4ff' },

	// Loud on purpose. It is answering "why is this row here", and a highlight
	// that has to be looked for does not answer it.
	hit: { background: '#ffb86b', color: '#101010', borderRadius: 2 },

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
	grid: { flex: 'none', lineHeight: '18px', width: '51ch' },

	// A byte is a click target, so it says so -- and it keeps the width it has
	// in the line, because a cell that grew on hover would move every byte
	// after it.
	cell: { cursor: 'text', borderRadius: 2 },
	byte: {
		background: '#101010',
		color: '#ffb86b',
		border: 'none',
		outline: `1px solid #7db4ff`,
		borderRadius: 2,
		padding: 0,
		margin: 0,
		font: 'inherit',
		width: '2ch',
		lineHeight: '18px',
	},
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

	// Not remembered between reloads, unlike the columns: a hidden column is a
	// decision about this entity and a search is a question being asked right
	// now. Coming back to a panel that is still filtered by something typed
	// yesterday is coming back to a table that is missing rows for no visible
	// reason.
	const [find, setFind] = useState<Find>({ q: '', how: 'fuzzy' })

	// Where following edges has been, so that going back is going back rather
	// than starting over. It is state and not history: the panel is a window on
	// a page that has its own back button, and taking that one over would be
	// answering a question nobody asked it.
	const [trail, setTrail] = useState<Step[]>([])
	const looking = trail[trail.length - 1]

	const shown = kept.tab === 'get' ? gets : lists
	const entity = shown.find((v) => v.typeName === kept.entity) ?? shown[0]

	const transport = ungated && props.ungated !== undefined ? props.ungated : app.queries.raw

	const names = useNames(props.entities, transport)

	/**
	 * look is what following an edge does: the Get tab, on the row it named.
	 *
	 * Each step records **where it was left from** as well as where it goes,
	 * because that is the only thing that can be gone back to. Without it the
	 * first press of back had nowhere to return to -- it popped the one step
	 * there was, found no step under it, and left the screen exactly as it
	 * was while the button that did nothing disappeared.
	 */
	const look = useCallback(
		(typeName: string, id: string) => {
			setTrail((v) => [...v, { from: { tab: kept.tab, entity: kept.entity }, typeName, id }])
			keep({ tab: 'get', entity: typeName })
		},
		[keep, kept.tab, kept.entity],
	)

	/**
	 * back is one edge, the way it was come by.
	 *
	 * The step is read out here rather than inside the `setTrail` updater. An
	 * updater has to be a pure function of the state it is given -- React calls
	 * it again when it decides to -- and `keep` is a second `setState`, so
	 * putting it in there is asking for the tab to be set twice or not at all.
	 */
	const back = useCallback(() => {
		const top = trail[trail.length - 1]
		if (top === undefined) return

		setTrail(trail.slice(0, -1))
		keep(top.from)
	}, [trail, keep])

	/**
	 * head and foot are the sidebar around whatever the view puts in it.
	 *
	 * Built here and handed down rather than rendered here, because the
	 * sidebar is one column: the entity, the three tabs, the view's own
	 * request form, and the search at the bottom. Rendering the fixed parts
	 * here and the form in the view would need them to be siblings, and they
	 * are not -- so one place lays the column out and the view fills the
	 * middle of it. See [Pane].
	 */
	const head = (
		<div style={style.top}>
			{/* Its own row: a type name is long and a row it shares is a row that wraps. */}
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

			{/* Three of one thing, so they share the width rather than each taking what its name needs. */}
			<div style={style.tabs}>
				{(['list', 'get', 'store'] as const).map((v) => (
					<button
						key={v}
						type="button"
						style={{ ...style.tab, ...(kept.tab === v ? style.on : {}) }}
						aria-pressed={kept.tab === v}
						onMouseDown={(e) => e.preventDefault()}
						onClick={() => keep({ tab: v })}
					>
						{v}
					</button>
				))}
			</div>

		</div>
	)

	const foot = (
		<div style={style.foot}>
			{props.ungated !== undefined && (
				<label style={{ ...style.check, display: 'inline-flex', color: ungated ? '#ffb86b' : dim }}>
					<input
						type="checkbox"
						style={style.box}
						checked={ungated}
						onChange={(e) => setUngated(e.target.checked)}
					/>
					past the wall
				</label>
			)}

			{/*
				A zone and not a format: what the column holds is UTC either
				way. The log this panel sits beside is a wall clock, which is
				why the default is the viewer's own -- and why the offset is
				always drawn, so that a row read out of here is unambiguous.
			*/}
			<label style={{ ...style.check, display: 'inline-flex', color: kept.utc ? '#7db4ff' : dim }}>
				<input
					type="checkbox"
					style={style.box}
					checked={kept.utc}
					onChange={(e) => keep({ utc: e.target.checked })}
				/>
				utc
			</label>

			{/*
				At the bottom, where a search box is. Over what is **on the
				screen** -- the columns that are not turned off, as they are
				drawn. Searching the values underneath would be a search that
				misses what it is pointed at: a uuid is sixteen bytes down there
				and a patch is a document, and neither is what anybody types.

				On the screen also means the rows that have been fetched, which
				the count says: `3 of 50` and not `3 of everything`. Asking the
				server is what the form's own filters are, and a find that
				quietly paged until it found something would be a second, worse
				version of them -- one that reads every row of a table to answer
				what an index could.
			*/}
			<div style={{ display: 'flex', gap: 3 }}>
				<input
					aria-label="find"
					style={{ ...style.input, flex: 1, minWidth: 0 }}
					placeholder="find"
					spellCheck={false}
					value={find.q}
					onChange={(e) => setFind({ ...find, q: e.target.value })}
				/>
				<Modes find={find} onChange={setFind} />
			</div>
		</div>
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

				<div style={style.body}>
					{entity === undefined ? (
						<p style={style.bad}>this app declares no entity that answers here.</p>
					) : kept.tab === 'store' ? (
						<Held
							head={head}
							foot={foot}
							entity={entity}
							hidden={kept.hidden}
							asId={kept.asId}
							utc={kept.utc}
							keep={keep}
							look={look}
							raw={setRaw}
							find={find}
							names={names}
							back={{ go: back, can: trail.length > 0 }}
							entities={props.entities}
						/>
					) : kept.tab === 'get' ? (
						<Get
							key={`${entity.typeName}:${String(ungated)}`}
							head={head}
							foot={foot}
							entity={entity}
							entities={props.entities}
							transport={transport}
							hidden={kept.hidden}
							asId={kept.asId}
							utc={kept.utc}
							keep={keep}
							look={look}
							raw={setRaw}
							find={find}
							names={names}
							back={{ go: back, can: trail.length > 0 }}
							diff={kept.diff}
							{...(props.monaco === undefined ? {} : { monaco: props.monaco })}
							{...(looking?.typeName === entity.typeName ? { id: looking.id } : {})}
						/>
					) : (
						<List
							key={`${entity.typeName}:${String(ungated)}`}
							head={head}
							foot={foot}
							entity={entity}
							entities={props.entities}
							transport={transport}
							hidden={kept.hidden}
							asId={kept.asId}
							utc={kept.utc}
							keep={keep}
							look={look}
							raw={setRaw}
							find={find}
							names={names}
							back={{ go: back, can: trail.length > 0 }}
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
					under={kept.height}
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
	asId: Record<string, string[]>

	/** Whether a timestamp is shown as UTC; see [Kept.utc]. */
	utc: boolean
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

	/** What is being looked for, which only a table can answer. */
	find: Find

	/** What a reference is called, and how to go and find out. */
	names: Names

	/** The sidebar above and below the view's own form; see [Pane]. */
	head: ReactNode
	foot: ReactNode

	/**
	 * One edge back, and whether there is one.
	 *
	 * It keeps its place when there is nowhere to go rather than appearing and
	 * disappearing: a button that comes and goes moves everything under it, and
	 * the row it shares would be a different row every time an edge is
	 * followed.
	 */
	back: { go: () => void; can: boolean }
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

	// And the cursor beside it, for exactly that reason.
	//
	// `next` is state because the form shows it. State is what a *render* sees,
	// and the scroll handler runs between the answer arriving and the render it
	// causes -- `busy` goes false at the end of `ask`, while `setNext` is still
	// queued. A scroll landing in that window read the cursor of the page
	// before and asked for it again, so a fast scroll appended a page twice.
	//
	// Written where the answer is read, so the two are never a render apart.
	const cursor = useRef('')

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

				cursor.current = v.next

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
				if (!more) {
					setRows([])
					cursor.current = ''
				}
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
			// `cursor.current` and not `next`: see the ref. They hold the same
			// thing everywhere except in the window this handler fires in.
			const at = cursor.current
			if (at === '' || busy.current) return

			const el = e.currentTarget
			if (el.scrollTop + el.clientHeight < el.scrollHeight - 64) return

			void ask(at, true)
		},
		[ask],
	)

	return (
		<>
			<Pane
				head={props.head}
				foot={props.foot}
				back={props.back}
				desc={method.input}
				vals={vals}
				onChange={setVals}
				onAsk={() => void ask('', false)}
				what="ask"
				skip={['size']}
			>
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
 * Pane is the sidebar: everything that is not the answer, in one column.
 *
 * The entity and the three tabs at the top, the request being built in the
 * middle, and the search at the bottom -- and only the middle scrolls, so a
 * request with twenty filters does not push the picker off the screen.
 *
 * The head and the foot are handed in rather than rendered here because they
 * are the panel's and not a view's: which entity and which tab are the same
 * question on all three tabs. What varies is the middle, which is the request
 * message read off its descriptor -- a `List` takes filters and a cursor, a
 * `Get` takes a reference and a select, and the store tab takes nothing at all.
 * An RPC that grows a field grows a box.
 */
function Pane(props: {
	head: ReactNode
	foot: ReactNode
	back: { go: () => void; can: boolean }

	/** The request being built, for a tab that asks one. */
	desc?: DescMessage
	vals?: Vals
	onChange?: (v: Vals) => void
	onAsk?: () => void
	what?: string
	skip?: readonly string[]

	/** Beside the ask button, for whatever else this tab can do to an answer. */
	act?: ReactNode
	children?: ReactNode
}): ReactNode {
	return (
		<aside style={style.pane}>
			{props.head}

			<div style={style.asked}>
				<div style={{ display: 'flex', gap: 3 }}>
					<button
						type="button"
						style={{ ...style.press, opacity: props.back.can ? 1 : 0.4 }}
						onClick={props.back.go}
						disabled={!props.back.can}
						aria-label="back"
					>
						←
					</button>

					{props.onAsk !== undefined && (
						<button type="button" style={{ ...style.press, flex: 1 }} onClick={props.onAsk}>
							{props.what}
						</button>
					)}
				</div>

				{props.act}

				{props.desc !== undefined && props.vals !== undefined && props.onChange !== undefined && (
					<Form
						desc={props.desc}
						vals={props.vals}
						onChange={props.onChange}
						{...(props.skip === undefined ? {} : { skip: props.skip })}
					/>
				)}

				{props.children}
			</div>

			{props.foot}
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
 * What comes back is shown as **protobuf JSON** rather than as a table of one
 * row. A table is for comparing rows and there is one; what is worth seeing
 * here is the whole document, nesting and all -- and JSON is what it goes back
 * as, so what is on the screen is what would be sent.
 *
 * # Editing it
 *
 * With an editor -- see [Props.monaco] -- the document is typed into and saved,
 * and what is sent is a `Patch` of the fields that **changed**: the whole
 * document would rewrite every column with what it already held, which is a
 * trail entry per field per save saying nothing happened.
 */
function Get(props: View & { transport: Transport; id?: string; monaco?: MonacoLike; diff: boolean }): ReactNode {
	const app = useApp()
	const method = props.entity.service?.method.get as DescMethodUnary<DescMessage, DescMessage>
	const patch = props.entity.service?.method.patch as DescMethodUnary<DescMessage, DescMessage> | undefined
	const [vals, setVals] = useForm(method.input)

	/** What the server answered, as JSON, and what is in the editor now. */
	const [was, setWas] = useState<Record<string, unknown>>()

	/**
	 * How many answers there have been, which is the editor's `key`.
	 *
	 * A new answer is a new editor. `Code` reads its document once and cannot
	 * be told a later one -- see its `value` -- because what would arrive
	 * while somebody types is that editor's own text a render behind. Counting
	 * is what makes "a different document" a thing React can see, and it is
	 * not the identifier: looking the **same** row up again is also a new
	 * document, and is how somebody throws an edit away.
	 */
	const [got, setGot] = useState(0)
	const [now, setNow] = useState<string>()
	const [err, setErr] = useState<string>()

	const schema = useMemo(() => jsonSchemaOf(method.output), [method.output])
	const settable = useMemo(
		() => (patch === undefined ? [] : patchable(patch, props.entity.version)),
		[patch, props.entity.version],
	)

	const ask = useCallback(
		async (v: Vals) => {
			setErr(undefined)
			setWas(undefined)
			setNow(undefined)
			try {
				const req = build(method.input, v)
				const row = await call(props.entity, props.transport, 'get', req)
				const json = toJson(method.output, row as never, { alwaysEmitImplicit: true }) as Record<string, unknown>

				setWas(json)
				setNow(JSON.stringify(json, null, 2))
				setGot((n) => n + 1)
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

	/**
	 * save sends what changed, and nothing else.
	 *
	 * Compared as JSON on both sides rather than as messages: that is the shape
	 * the editor holds, and re-decoding it to compare would be deciding twice
	 * what "different" means for a `bytes` or a `Timestamp`.
	 */
	const save = async (): Promise<void> => {
		if (patch === undefined || was === undefined || now === undefined) return

		setErr(undefined)
		try {
			const edited = JSON.parse(now) as Record<string, unknown>

			const req: Record<string, unknown> = { ref: { id: was.id } }
			let some = false
			for (const f of settable) {
				// A field the document does not have is a field this is not
				// about. Deleting a line is not how a value is cleared -- the
				// `_null` companions are, and they are on the request rather
				// than on the row for exactly this reason: absent and empty
				// are different things and a document cannot say which it
				// meant.
				if (!(f.jsonName in edited)) continue
				if (JSON.stringify(edited[f.jsonName]) === JSON.stringify(was[f.jsonName])) continue

				req[f.jsonName] = edited[f.jsonName]
				some = true
			}

			if (!some) {
				setErr('nothing changed')

				return
			}

			// The precondition, from the answer rather than from the editor: a
			// version somebody typed over is a write that overwrites whatever
			// happened in between, which is the one thing this is here to
			// refuse.
			if (props.entity.version !== undefined) {
				const v = method.output.fields.find((f) => f.localName === props.entity.version)
				if (v !== undefined) req[v.jsonName] = was[v.jsonName]
			}

			await app.queries.call(patch, fromJson(patch.input, req as never))
			await ask(vals)
		} catch (e) {
			setErr(String(e))
		}
	}

	const editing = props.monaco !== undefined && settable.length > 0
	const clean = was === undefined ? '' : JSON.stringify(was, null, 2)
	const dirty = now !== undefined && now !== clean

	// On the toggle and on nothing else. It was also asking whether anything
	// had changed yet -- a diff of a document against itself being half the
	// width spent saying so -- and that flipped **while somebody was typing**,
	// which rebuilds the editor and throws away the keystroke that flipped it.
	// An empty diff is honest; an editor that eats the first character is not.
	const split = props.diff && was !== undefined

	return (
		<>
			<Pane
				head={props.head}
				foot={props.foot}
				back={props.back}
				desc={method.input}
				vals={vals}
				onChange={setVals}
				onAsk={() => void ask(vals)}
				what="look up"
				act={
					<>
						{editing && was !== undefined && (
							<div style={{ display: 'flex', gap: 3, alignItems: 'stretch' }}>
								<button
									type="button"
									style={{ ...style.press, flex: 1, opacity: dirty ? 1 : 0.4 }}
									disabled={!dirty}
									onClick={() => {
										// A new editor over the answer, which
										// is what throwing an edit away is:
										// `Code` reads its document once.
										setNow(clean)
										setGot((n) => n + 1)
									}}
								>
									cancel
								</button>

								<label
									style={{
										...style.press,
										...style.check,
										display: 'flex',
										color: props.diff ? '#ffb86b' : dim,
									}}
								>
									<input
										type="checkbox"
										style={style.box}
										checked={props.diff}
										onChange={(e) => props.keep({ diff: e.target.checked })}
									/>
									diff
								</label>

								<button
									type="button"
									style={{ ...style.press, flex: 1, opacity: dirty ? 1 : 0.4 }}
									disabled={!dirty}
									onClick={() => void save()}
								>
									save
								</button>
							</div>
						)}
						{err !== undefined && <p style={style.bad}>{err}</p>}
					</>
				}
			/>

			<div style={{ ...style.seen, padding: 0, display: 'flex' }}>
				{now === undefined ? null : props.monaco === undefined ? (
					<div style={{ overflow: 'auto', padding: 4 }}>
						<Json value={was} />
					</div>
				) : (
					<Code
						key={got}
						monaco={props.monaco}
						uri={`payday/${props.entity.typeName}.json`}
						value={now}
						{...(split ? { original: JSON.stringify(was, null, 2) } : {})}
						schema={schema}
						readOnly={!editing}
						onChange={setNow}
					/>
				)}
			</div>
		</>
	)
}

/** Held is what the store holds, which is not the same question. */
function Held(props: View): ReactNode {
	const app = useApp()
	const rows = app.store.all(props.entity.typeName) as unknown as Record<string, unknown>[]

	return (
		<>
			<Pane head={props.head} foot={props.foot} back={props.back}>
				<p style={{ color: dim, margin: 0 }}>
					what this browser holds, which the server was not asked about
				</p>
			</Pane>

			<div style={style.seen}>
				<Table label="held" rows={rows} {...props} />
			</div>
		</>
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
	const asId = new Set(props.asId[props.entity.typeName] ?? [])

	/** to is what a column points at -- an edge, or this entity for its key. */
	const to = (name: string): string | undefined =>
		name === props.entity.key ? props.entity.typeName : refs.get(name)

	/**
	 * named is a column that can show a name instead of an identifier: an edge
	 * into an entity that has one, and not turned off.
	 */
	const named = (name: string): boolean => {
		if (asId.has(name)) return false

		// Edges only. The key column names **this** row, and showing its alias
		// there would put the same word in two columns of every line.
		const at = refs.get(name)

		return at !== undefined && props.entities.find((v) => v.typeName === at)?.alias !== undefined
	}

	// What is on the screen and not yet known. Asked for in an effect because
	// asking is a fetch, and one row's name is one `Get` however many cells
	// point at it.
	const { want } = props.names
	useEffect(() => {
		const vs: { typeName: string; id: string }[] = []
		for (const row of props.rows) {
			for (const f of fields) {
				if (hidden.has(f.localName) || !named(f.localName)) continue

				const at = refs.get(f.localName)
				const id = idOf(row[f.localName])
				if (at !== undefined && id !== undefined) vs.push({ typeName: at, id: key(bytesOfId(id)) })
			}
		}

		want(vs)
	}) // eslint-disable-line react-hooks/exhaustive-deps

	const method = props.entity.service?.method.patch as DescMethodUnary<DescMessage, DescMessage> | undefined
	const settable = useMemo(
		() => new Set(method === undefined ? [] : patchable(method, props.entity.version).map((f) => f.localName)),
		[method, props.entity.version],
	)

	const [edit, setEdit] = useState<{ at: string; field: string; value: string; err?: string }>()

	/**
	 * The rows that match, over the columns that are not turned off.
	 *
	 * A column somebody turned off is a column they said they are not reading,
	 * and a row kept because of what is in one would be a row that matches
	 * nothing on the screen.
	 */
	const wants = matcher(props.find)
	const rows = useMemo(() => {
		if (typeof wants === 'string' || props.find.q === '') return props.rows

		return props.rows.filter((row) =>
			fields.some((f) => {
				if (hidden.has(f.localName)) return false

				const text = shown({
					value: row[f.localName],
					field: f,
					id: ids.has(f.localName),
					to: to(f.localName),
					names: named(f.localName) ? props.names : undefined,
					utc: props.utc,
				})

				return wants(text) !== undefined
			}),
		)
		// `wants` is rebuilt every render; what it stands for is the query.
	}, [props.rows, props.find.q, props.find.how, props.hidden, props.entity]) // eslint-disable-line react-hooks/exhaustive-deps

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
			{typeof wants === 'string' ? (
				<p style={style.bad}>{wants}</p>
			) : (
				props.find.q !== '' && (
					<p style={{ color: dim, margin: '2px 0' }} aria-label="matched">
						{rows.length} of {props.rows.length}
					</p>
				)
			)}

			<table style={style.table} aria-label={props.label}>
				<thead>
					<tr>
						{fields.map((f) => (
							<th key={f.localName} style={style.th}>
								<Head
									name={f.localName}
									shown={!hidden.has(f.localName)}
									toggle={toggle}
									{...(refs.get(f.localName) !== undefined &&
									props.entities.find((v) => v.typeName === refs.get(f.localName))?.alias !== undefined
										? {
												alias: {
													on: !asId.has(f.localName),
													toggle: () => {
														const now = new Set(asId)
														if (now.has(f.localName)) {
															now.delete(f.localName)
														} else {
															now.add(f.localName)
														}

														props.keep({
															asId: { ...props.asId, [props.entity.typeName]: [...now] },
														})
													},
												},
											}
										: {})}
								/>
							</th>
						))}
					</tr>
				</thead>
				<tbody>
					{rows.map((row, i) => {
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
												// The row's own key names this row,
												// which is the one edge a table has
												// that is not in `refs`.
												to={to(f.localName)}
												names={named(f.localName) ? props.names : undefined}
												utc={props.utc}
												hit={typeof wants === 'string' ? undefined : wants}
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
function Head(props: {
	name: string
	shown: boolean
	toggle: (name: string, span: boolean) => void

	/**
	 * Whether this column is showing names, for one that can.
	 *
	 * Written as `(id)` struck through, which says what is being left out
	 * rather than what is being shown: the column is called `tenant` either
	 * way, and what the switch decides is whether the identifier is what you
	 * are looking at.
	 */
	alias?: { on: boolean; toggle: () => void }
}): ReactNode {
	const [over, bind] = useOver()

	// Whether shift was down, taken from the click rather than from the change
	// it causes: a `change` event is an `Event` and carries no modifiers, and
	// reading `nativeEvent` for one is reading a field that is there in a
	// browser and not in a test.
	const span = useRef(false)

	// A `label` and not a `span`, which is the whole of what makes the name
	// turn the box -- the browser has done this since forever and writing it as
	// a span is opting out of it for nothing.
	return (
		<>
			<label style={{ ...style.check, display: 'flex' }} {...bind}>
				<input
					type="checkbox"
					style={style.box}
					aria-label={props.name}
					checked={props.shown}
					onClick={(e) => (span.current = e.shiftKey)}
					onChange={() => props.toggle(props.name, span.current)}
				/>
				{props.shown && <span>{props.name}</span>}

				{props.shown && props.alias !== undefined && (
					<button
						type="button"
						style={{
							...style.tag,
							textDecoration: props.alias.on ? 'line-through' : 'none',
							color: props.alias.on ? '#5f5f5f' : '#7db4ff',
						}}
						aria-label={`${props.name} as id`}
						aria-pressed={!props.alias.on}
						onMouseDown={(e) => e.preventDefault()}
						onClick={(e) => {
							// The name beside it is inside a `label`, so
							// without this the click would also turn the
							// column off.
							e.preventDefault()
							props.alias?.toggle()
						}}
					>
						(id)
					</button>
				)}
			</label>

			{!props.shown && <Over at={over}>{props.name}</Over>}
		</>
	)
}

interface Value {
	value: unknown
	field: DescField

	/** Whether the schema declared this field a uuid. */
	id: boolean

	/** The entity this field names, for a field that names one. */
	to: string | undefined

	/** Where to look up what it is called, for a column showing names. */
	names: Names | undefined

	/** Whether a timestamp is shown as UTC; see [Kept.utc]. */
	utc: boolean
}

/**
 * shown is the text a cell puts on the screen, whatever it renders it as.
 *
 * Separate from [Cell] because find searches what is **on the screen** and not
 * what is under it: somebody looking for a row types the uuid they can see and
 * not the sixteen bytes it stands for, and `100 bytes` is what a patch column
 * says. Two functions deciding that would be a search that misses what it is
 * pointed at.
 */
function shown(props: Value): string {
	const v = props.value
	if (v === undefined || v === null) return '—'
	if (props.to !== undefined) {
		const id = idOf(v)
		if (id === undefined) return '—'

		// What the row is called if that is known and asked for, and the
		// identifier until it is: a column that went blank while it waited
		// would be a column that flickers.
		return props.names?.of(props.to, key(bytesOfId(id))) ?? id
	}

	const raw = rawOf(props.field, v)
	if (raw !== undefined) {
		if (raw.byteLength === 0) return '—'
		if (props.id && raw.byteLength === 16) return uuidOf(raw)

		return `${String(raw.byteLength)} bytes`
	}

	if (typeof v === 'object' && (v as { $typeName?: string }).$typeName === 'google.protobuf.Timestamp') {
		return when(timestampDate(v as never), props.utc)
	}

	if (typeof v === 'object') return JSON.stringify(v)

	return String(v)
}

/**
 * when is a moment on the screen.
 *
 * Written out rather than left to `toLocaleString`, which is a different shape
 * per locale: a column of them is read down, and two rows only compare at a
 * glance when every row is the same width in the same order. This is the ISO
 * spelling with the date and the time parted by a space -- a `T` is for a wire
 * and this is for a person -- and the offset always written, because a wall
 * clock with no zone on it is the thing this switch exists to stop being.
 *
 * Milliseconds and no further: the column holds nanoseconds and a JavaScript
 * `Date` does not, so the last six digits are gone before this is reached. The
 * bytes are still there under `raw`.
 */
function when(at: Date, utc: boolean): string {
	const p = (v: number, n = 2): string => String(v).padStart(n, '0')

	if (utc) {
		const d = `${String(at.getUTCFullYear())}-${p(at.getUTCMonth() + 1)}-${p(at.getUTCDate())}`
		const t = `${p(at.getUTCHours())}:${p(at.getUTCMinutes())}:${p(at.getUTCSeconds())}.${p(at.getUTCMilliseconds(), 3)}`

		return `${d} ${t} Z`
	}

	const d = `${String(at.getFullYear())}-${p(at.getMonth() + 1)}-${p(at.getDate())}`
	const t = `${p(at.getHours())}:${p(at.getMinutes())}:${p(at.getSeconds())}.${p(at.getMilliseconds(), 3)}`

	// `getTimezoneOffset` counts the other way round: minutes to add to local
	// to reach UTC, so a zone ahead of UTC answers with a negative number.
	const off = -at.getTimezoneOffset()
	const z = `${off < 0 ? '-' : '+'}${p(Math.floor(Math.abs(off) / 60))}:${p(Math.abs(off) % 60)}`

	return `${d} ${t} ${z}`
}

function Cell(
	props: Value & {
		/** Where the current query matched, for a panel that is searching. */
		hit: ((s: string) => Hits | undefined) | undefined
		look: (typeName: string, id: string) => void
		onRaw: (v: Uint8Array) => void
	},
): ReactNode {
	// What it says is [shown]'s; what is left here is what it is rendered as,
	// which is the only part that is not text.
	const text = shown(props)
	if (text === '—') return <span style={{ color: dim }}>—</span>

	const lit = <Mark text={text} hits={props.hit?.(text)} />

	// An edge is not expanded. What the server answered with is a reference, so
	// what there is to show is the row it names -- and following it is a `Get`,
	// which is a click rather than a join nobody asked for.
	if (props.to !== undefined) {
		// Followed by identifier and not by what is on the screen: a column
		// showing names shows `acme`, and `acme` is not what a `Get` takes.
		const id = idOf(props.value)

		return (
			<button
				type="button"
				style={style.link}
				disabled={id === undefined}
				onClick={() => id !== undefined && props.look(props.to as string, id)}
			>
				{lit}
			</button>
		)
	}

	// Bytes that are not an identifier. A marshalled patch is a column as wide
	// as the document it holds and hex is the least readable thing that column
	// could be full of, so it says how big it is -- which is what anybody reads
	// at a glance -- and opens on a click.
	const raw = rawOf(props.field, props.value)
	if (raw !== undefined && !(props.id && raw.byteLength === 16)) {
		return (
			<button type="button" style={style.link} onClick={() => props.onRaw(raw)}>
				{lit}
			</button>
		)
	}

	return <span>{lit}</span>
}

/**
 * Mark is a value with the part a query matched lit up.
 *
 * A row kept by a search and not saying why is a row somebody has to work the
 * reason out for, over twenty columns -- and for fuzzy that is most of the
 * work, because the letters it matched are scattered by definition.
 */
function Mark(props: { text: string; hits: Hits | undefined }): ReactNode {
	const hits = props.hits
	if (hits === undefined || hits.length === 0) return props.text

	const out: ReactNode[] = []
	let at = 0
	let n = 0

	for (const [a, b] of hits) {
		if (a > at) out.push(<span key={n++}>{props.text.slice(at, a)}</span>)

		out.push(
			<mark key={n++} style={style.hit}>
				{props.text.slice(a, b)}
			</mark>,
		)
		at = b
	}

	if (at < props.text.length) out.push(<span key={n++}>{props.text.slice(at)}</span>)

	return out
}

/**
 * Hex is a bytes value, down the right side.
 *
 * Three columns because that is what every tool that has ever shown bytes uses
 * and it is not a style: the offset says where you are, the hex is what is
 * there, and the text is how anybody tells at a glance whether they are looking
 * at a string, a protobuf or noise.
 *
 * It sits under the panel and stops where the panel starts; `style.side` says
 * why it is not over the top of it.
 *
 * # Two ways to edit, and why both
 *
 * A byte at a time is what somebody wants nine times out of ten: click the
 * byte, type two digits, and it is on the next one -- no selecting the right
 * two characters out of a wall of them and no chance of leaving one digit
 * behind. What that cannot do is take a run of bytes out or paste a new value
 * over the whole thing, and a text box is exactly right for that. So the byte
 * grid is what opens and `text` is a button away, and only one of them is on
 * the screen at a time -- two editors over one value is two answers about what
 * was typed.
 *
 * Editable where the schema says the field is: `patchable` already knows, and a
 * box that takes typing for a field the server will refuse is a box that
 * teaches the wrong thing.
 */
function Hex(props: {
	name: string
	value: Uint8Array
	settable: boolean
	/** Where the panel starts, which is where this stops. */
	under: number
	onClose: () => void
	onSave: (v: Uint8Array) => Promise<void>
}): ReactNode {
	const [text, setText] = useState(() => spaced(props.value))
	const [raw, setRaw] = useState(false)
	const [err, setErr] = useState<string>()

	const box = useRef<HTMLDivElement>(null)
	useEffect(() => box.current?.focus(), [])

	/** Which byte is being typed into, and what has been typed of it. */
	const [at, setAt] = useState<number>()
	const [typed, setTyped] = useState('')

	const parsed = useMemo(() => packed(text), [text])
	const changed = text !== spaced(props.value)

	// A document that does not parse cannot be shown as bytes, and the box it
	// was typed in is the only place the mistake can be fixed.
	const bad = typeof parsed === 'string'
	const grid = !raw && !bad

	const put = (i: number, b: number): void => {
		if (typeof parsed === 'string') return

		const next = new Uint8Array(parsed)
		next[i] = b
		setText(spaced(next))
	}

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
			style={{ ...style.side, bottom: props.under }}
			role="dialog"
			aria-label={`${props.name} bytes`}
			// Escape closes it, and it is focused **once** so that it does
			// without anything being clicked first.
			//
			// Once, and by an effect, because this was an inline `ref` -- a new
			// function every render, so React detached and reattached it every
			// render and focused the dialog again each time. What that cost was
			// the byte editor: opening one put the caret here instead of in the
			// box, and typing a digit into it took the caret straight back out.
			tabIndex={-1}
			ref={box}
			onKeyDown={(e) => {
				if (e.key === 'Escape') props.onClose()
			}}
			// Anywhere that is not a byte ends the edit, which is what clicking
			// off something means. The bytes stop this from reaching here.
			onMouseDown={() => setAt(undefined)}
		>
			<div style={style.bar}>
				<strong>{props.name}</strong>
				<span style={{ color: dim }}>
					{props.value.byteLength} bytes{props.settable ? '' : ', read only'}
				</span>

				<span style={{ flex: 1 }} />

				{props.settable && (
					<button
						type="button"
						style={{ ...style.press, color: raw || bad ? '#ffb86b' : dim }}
						aria-label="edit as text"
						aria-pressed={raw || bad}
						onClick={() => setRaw(!raw)}
						disabled={bad}
					>
						text
					</button>
				)}
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
			{bad && <p style={{ ...style.bad, margin: '4px 6px' }}>{parsed}</p>}

			<div style={{ flex: 1, minHeight: 0, display: 'flex', overflow: 'auto', padding: '6px 10px', gap: 10 }}>
				<pre aria-label="offset" style={style.rule}>
					{offsets(text)}
				</pre>

				{grid ? (
					<Bytes
						value={parsed as Uint8Array}
						settable={props.settable}
						at={at}
						typed={typed}
						onOpen={(i) => {
							setAt(i)
							setTyped('')
						}}
						onTyped={setTyped}
						onPut={put}
					/>
				) : (
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
				)}

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
 * Bytes is the hex, one clickable cell per byte.
 *
 * Two digits and it moves on, which is the whole gesture: a byte is two
 * characters and there is no third thing it could be waiting for, so asking
 * for a Tab as well would be asking for a keystroke that carries no
 * information. One digit and then a click away is left alone rather than
 * written as `0x0d` -- half a byte is not a value anybody meant.
 */
function Bytes(props: {
	value: Uint8Array
	settable: boolean
	at: number | undefined
	typed: string
	onOpen: (i: number) => void
	onTyped: (v: string) => void
	onPut: (i: number, b: number) => void
}): ReactNode {
	// Focused by hand rather than by `autoFocus`, which is an attribute the
	// browser honours when it parses a document and not when React puts an
	// element into one: the box appeared and the caret stayed wherever it was.
	// On `at` and not on mount, because moving to the next byte is a different
	// box, and that is the same gesture.
	const box = useRef<HTMLInputElement>(null)
	useEffect(() => {
		box.current?.focus()
	}, [props.at])

	const rows: ReactNode[] = []

	for (let i = 0; i < props.value.length; i += 16) {
		const cells: ReactNode[] = []
		for (let j = 0; j < 16 && i + j < props.value.length; j++) {
			const k = i + j

			// The wider gap every four, which is what makes a line countable:
			// sixteen pairs in a row are counted one at a time and four groups
			// of four are read.
			if (j > 0) cells.push(<span key={`g${String(j)}`}>{j % 4 === 0 ? '\u00a0\u00a0' : '\u00a0'}</span>)

			cells.push(
				props.at === k ? (
					<input
						key={k}
						aria-label={`byte ${String(k)}`}
						style={style.byte}
						value={props.typed}
						ref={box}
						spellCheck={false}
						onMouseDown={(e) => e.stopPropagation()}
						onChange={(e) => {
							const v = e.target.value.replace(/[^0-9a-fA-F]/g, '').slice(0, 2)
							if (v.length < 2) {
								props.onTyped(v)

								return
							}

							props.onPut(k, Number.parseInt(v, 16))
							if (k + 1 < props.value.length) {
								props.onOpen(k + 1)
							} else {
								props.onTyped(v)
							}
						}}
					/>
				) : (
					<span
						key={k}
						style={props.settable ? style.cell : undefined}
						// `preventDefault` as well as `stopPropagation`, and it
						// is the one that matters: a mousedown's default action
						// is to move focus, it runs **after** the handlers, and
						// it was putting the caret back on the dialog the
						// instant the box below had taken it.
						onMouseDown={
							props.settable
								? (e) => {
										e.preventDefault()
										e.stopPropagation()
										props.onOpen(k)
									}
								: undefined
						}
					>
						{props.value[k]?.toString(16).padStart(2, '0')}
					</span>
				),
			)
		}

		rows.push(
			<div key={i} style={{ whiteSpace: 'pre' }}>
				{cells}
			</div>,
		)
	}

	return (
		<div aria-label="hex" style={style.grid}>
			{rows}
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
 *
 * Written in two halves, for the same reason the bytes are in fours: eight
 * digits in a row is a number to be counted rather than read.
 */
function offsets(text: string): string {
	let at = 0

	return text
		.split('\n')
		.map((l) => {
			const was = at.toString(16).padStart(8, '0')
			at += digits(l).length >> 1

			return `${was.slice(0, 4)} ${was.slice(4)}`
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

/** bytesOfId is a written identifier back as the bytes a store keys by. */
function bytesOfId(v: string): Uint8Array {
	try {
		return pdid.parse(v).bytes
	} catch {
		return bytes(v)
	}
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
