/**
 * A form for any message, built from its descriptor.
 *
 * A request is a message and a message says what it holds, so a panel that
 * wants to ask an arbitrary entity an arbitrary question does not need a form
 * per RPC — it needs one form that reads a descriptor. That is the same rule
 * the store is built on, one layer up: generate the declaration, implement the
 * behaviour once.
 *
 * What it covers is what payday's own requests are made of: scalars, bytes as
 * the identifiers they are, nested messages, the `oneof` a reference is, and a
 * repeated message like a list's filters. What it does not cover — maps, and a
 * repeated scalar — is not in one, and rendering something nobody has is how a
 * generic form becomes wrong without anybody finding out.
 *
 * # Why the state is paths and strings
 *
 * Every input is a string, and a request wants `Uint8Array`, `bigint` and
 * nested objects. Keeping the half-typed shape and converting on the way out
 * means one walk of the descriptor knows how to convert, rather than every
 * input knowing what it is inside — and a value that does not parse yet is a
 * value somebody is still typing rather than an error to throw at them.
 *
 * @module
 */

import { ScalarType, type DescField, type DescMessage, type DescOneof } from '@bufbuild/protobuf'
import { useMemo, useState, type CSSProperties, type ReactNode } from 'react'

import * as pdid from '../pdid/index.js'
import { bytes } from '../store/index.js'

/** Vals is what somebody has typed, keyed by where it sits in the message. */
export interface Vals {
	/** A scalar, an enum, or bytes, as typed. */
	leaf: Record<string, string>

	/** Which member of a `oneof` is being written, by the oneof's path. */
	pick: Record<string, string>

	/** How many of a repeated field there are, by the field's path. */
	many: Record<string, number>
}

/** empty is a form nobody has typed in yet. */
export function empty(): Vals {
	return { leaf: {}, pick: {}, many: {} }
}

/**
 * build turns what was typed into the request, converting as it goes.
 *
 * A leaf nobody filled in is left out rather than sent as a zero: an unset
 * field and a field set to its default are the same on the wire for a scalar,
 * but a `oneof` member and a nested message are not, and a form that sent every
 * box would ask a different question from the one on the screen.
 */
export function build(desc: DescMessage, v: Vals, at = ''): Record<string, unknown> {
	const out: Record<string, unknown> = {}

	for (const o of desc.oneofs) {
		const p = path(at, o.localName)
		const name = v.pick[p]
		if (name === undefined || name === '') continue

		const f = o.fields.find((w) => w.localName === name)
		if (f === undefined) continue

		// Keyed by the **oneof** and not by the member: an init shape says
		// `{ key: { case: 'id', value } }`, where `key` is the group's name and
		// `id` is which of it. Writing the member's name there builds a message
		// with a field nothing reads, and the request goes out without the
		// reference it was supposed to carry.
		const got = one(f, v, path(at, f.localName))
		if (got !== undefined) out[o.localName] = { case: f.localName, value: got }
	}

	for (const f of desc.fields) {
		if (f.oneof !== undefined) continue

		if (f.fieldKind === 'list') {
			const n = v.many[path(at, f.localName)] ?? 0
			if (n === 0) continue

			const vs: unknown[] = []
			for (let i = 0; i < n; i++) {
				const got = one(f, v, `${path(at, f.localName)}.${String(i)}`, true)
				if (got !== undefined) vs.push(got)
			}

			out[f.localName] = vs
			continue
		}

		const got = one(f, v, path(at, f.localName))
		if (got !== undefined) out[f.localName] = got
	}

	return out
}

/** one is a single field's value, or undefined for one nobody filled in. */
function one(f: DescField, v: Vals, at: string, item = false): unknown {
	const kind = item && f.fieldKind === 'list' ? f.listKind : f.fieldKind

	if (kind === 'message') {
		const m = f.fieldKind === 'list' ? f.message : f.fieldKind === 'message' ? f.message : undefined
		if (m === undefined) return undefined

		const got = build(m, v, at)

		// An empty nested message is one nobody typed in. Sending it would ask
		// for a row by a reference that names nothing.
		return Object.keys(got).length === 0 ? undefined : got
	}

	const s = v.leaf[at]
	if (s === undefined || s === '') return undefined

	if (kind === 'enum') return Number(s)
	if (f.fieldKind !== 'scalar' && f.fieldKind !== 'list') return undefined

	const t = f.fieldKind === 'scalar' ? f.scalar : f.listKind === 'scalar' ? f.scalar : undefined
	switch (t) {
		case ScalarType.BOOL:
			return s === 'true'

		case ScalarType.BYTES:
			// An identifier is what bytes are in a payday request, so this
			// takes one the way it is written down -- and falls back to hex
			// for the rest, which is what the store keys rows by.
			try {
				return pdid.parse(s).bytes
			} catch {
				return bytes(s)
			}

		case ScalarType.INT64:
		case ScalarType.UINT64:
		case ScalarType.SINT64:
		case ScalarType.FIXED64:
		case ScalarType.SFIXED64:
			return BigInt(s)

		case ScalarType.STRING:
		case undefined:
			return s

		default:
			return Number(s)
	}
}

function path(at: string, name: string): string {
	return at === '' ? name : `${at}.${name}`
}

const dim = '#8b8b8b'
const line = '#2c2c2c'

const style = {
	group: { border: `1px solid ${line}`, borderRadius: 3, padding: '4px 6px', margin: '2px 0' },
	label: { color: dim, marginRight: 6 },
	row: { display: 'flex', gap: 6, alignItems: 'baseline', margin: '2px 0', flexWrap: 'wrap' },
	input: {
		background: '#101010',
		color: '#e6e6e6',
		border: `1px solid ${line}`,
		borderRadius: 3,
		padding: '1px 4px',
		font: 'inherit',
		minWidth: 0,
	},
} satisfies Record<string, CSSProperties>

/** Form is the inputs for one message. */
export function Form(props: {
	desc: DescMessage
	vals: Vals
	onChange: (v: Vals) => void
	at?: string
}): ReactNode {
	const at = props.at ?? ''

	const set = (part: keyof Vals, k: string, v: string | number): void => {
		props.onChange({ ...props.vals, [part]: { ...props.vals[part], [k]: v } })
	}

	return (
		<>
			{props.desc.oneofs.map((o) => (
				// The spread goes first: it carries this form's own `at`, and
				// what each of these needs is where **it** sits.
				<OneOf key={o.localName} {...props} oneof={o} at={at} set={set} />
			))}

			{props.desc.fields.map((f) =>
				f.oneof !== undefined ? null : (
					<Field key={f.localName} {...props} field={f} at={path(at, f.localName)} set={set} />
				),
			)}
		</>
	)
}

type Set = (part: keyof Vals, k: string, v: string | number) => void

/** OneOf is the choice, and then whichever member was chosen. */
function OneOf(props: {
	oneof: DescOneof
	desc: DescMessage
	vals: Vals
	onChange: (v: Vals) => void
	at: string
	set: Set
}): ReactNode {
	const p = path(props.at, props.oneof.localName)
	const picked = props.vals.pick[p] ?? ''
	const f = props.oneof.fields.find((w) => w.localName === picked)

	return (
		<div style={style.row}>
			<span style={style.label}>{props.oneof.localName}</span>
			<select
				aria-label={p}
				style={style.input}
				value={picked}
				onChange={(e) => props.set('pick', p, e.target.value)}
			>
				<option value="" />
				{props.oneof.fields.map((w) => (
					<option key={w.localName} value={w.localName}>
						{w.localName}
					</option>
				))}
			</select>

			{f !== undefined && (
				<Field
					field={f}
					at={path(props.at, f.localName)}
					desc={props.desc}
					vals={props.vals}
					onChange={props.onChange}
					set={props.set}
					bare
				/>
			)}
		</div>
	)
}

/** Field is one field, whatever it is made of. */
function Field(props: {
	field: DescField
	at: string
	desc: DescMessage
	vals: Vals
	onChange: (v: Vals) => void
	set: Set
	bare?: boolean
}): ReactNode {
	const f = props.field
	const name = props.bare === true ? null : <span style={style.label}>{f.localName}</span>

	if (f.fieldKind === 'list') {
		const n = props.vals.many[props.at] ?? 0

		return (
			<div style={style.group}>
				<div style={style.row}>
					{name ?? <span style={style.label}>{f.localName}</span>}
					<button
						type="button"
						style={{ ...style.input, cursor: 'pointer' }}
						onClick={() => props.set('many', props.at, n + 1)}
					>
						add
					</button>
					{n > 0 && (
						<button
							type="button"
							style={{ ...style.input, cursor: 'pointer' }}
							onClick={() => props.set('many', props.at, n - 1)}
						>
							drop
						</button>
					)}
				</div>

				{Array.from({ length: n }, (_, i) =>
					f.listKind === 'message' ? (
						<div key={i} style={style.group}>
							<Form
								desc={f.message}
								vals={props.vals}
								onChange={props.onChange}
								at={`${props.at}.${String(i)}`}
							/>
						</div>
					) : (
						<Leaf key={i} at={`${props.at}.${String(i)}`} vals={props.vals} set={props.set} />
					),
				)}
			</div>
		)
	}

	if (f.fieldKind === 'message') {
		return (
			<div style={style.group}>
				{name}
				<Form desc={f.message} vals={props.vals} onChange={props.onChange} at={props.at} />
			</div>
		)
	}

	if (f.fieldKind === 'enum') {
		return (
			<span style={style.row}>
				{name}
				<select
					aria-label={props.at}
					style={style.input}
					value={props.vals.leaf[props.at] ?? ''}
					onChange={(e) => props.set('leaf', props.at, e.target.value)}
				>
					<option value="" />
					{f.enum.values.map((v) => (
						<option key={v.name} value={v.number}>
							{v.name}
						</option>
					))}
				</select>
			</span>
		)
	}

	if (f.scalar === ScalarType.BOOL) {
		return (
			<span style={style.row}>
				{name}
				<input
					type="checkbox"
					aria-label={props.at}
					checked={props.vals.leaf[props.at] === 'true'}
					onChange={(e) => props.set('leaf', props.at, e.target.checked ? 'true' : '')}
				/>
			</span>
		)
	}

	return (
		<span style={style.row}>
			{name}
			<Leaf at={props.at} vals={props.vals} set={props.set} hint={hint(f)} />
		</span>
	)
}

function Leaf(props: { at: string; vals: Vals; set: Set; hint?: string }): ReactNode {
	return (
		<input
			aria-label={props.at}
			style={style.input}
			value={props.vals.leaf[props.at] ?? ''}
			placeholder={props.hint ?? ''}
			spellCheck={false}
			onChange={(e) => props.set('leaf', props.at, e.target.value)}
		/>
	)
}

/** hint is what a box takes, for the two that are not obvious. */
function hint(f: DescField): string {
	if (f.fieldKind !== 'scalar') return ''
	if (f.scalar === ScalarType.BYTES) return 'uuid or hex'

	return ''
}

/** useForm is [Vals] as state, reset when the message changes. */
export function useForm(desc: DescMessage): [Vals, (v: Vals) => void] {
	const zero = useMemo(() => empty(), [desc])
	const [vals, setVals] = useState<Vals>(zero)

	// A different message is a different form, and keeping what was typed into
	// the last one would send a path that does not exist here.
	const [seen, setSeen] = useState(desc)
	if (seen !== desc) {
		setSeen(desc)
		setVals(zero)
	}

	return [vals, setVals]
}
