/**
 * JSON, coloured.
 *
 * One row is a document rather than a table row, so it is shown as one — and a
 * wall of one colour is a wall nobody reads. What this does is the smallest
 * thing that helps: keys, strings, numbers and the three literals get a colour
 * each, and everything else is punctuation.
 *
 * It renders from a **stringified** document rather than walking the value,
 * because that is what makes the indentation and the ordering somebody else's
 * problem: `JSON.stringify` already decided both, and re-deciding them here
 * would be a second opinion about the same document.
 *
 * @module
 */

import type { CSSProperties, ReactNode } from 'react'

const hue = {
	key: '#7db4ff',
	str: '#a8e6a1',
	num: '#ffb86b',
	lit: '#c99bff',
	rest: '#8b8b8b',
} as const

const style = {
	pre: {
		margin: 0,
		font: 'inherit',
		whiteSpace: 'pre',
		overflowX: 'auto',
	},
} satisfies Record<string, CSSProperties>

/**
 * Json shows one value.
 *
 * `bigint` is what a 64-bit field arrives as and `JSON.stringify` throws on it,
 * so it is rendered as the digits it stands for rather than taking the panel
 * down over a column somebody happened to select.
 */
export function Json(props: { value: unknown }): ReactNode {
	let text: string
	try {
		text = JSON.stringify(props.value, replacer, 2) ?? 'undefined'
	} catch (e) {
		text = String(e)
	}

	return <pre style={style.pre}>{paint(text)}</pre>
}

function replacer(_k: string, v: unknown): unknown {
	if (typeof v === 'bigint') return v.toString()
	if (v instanceof Uint8Array) return Array.from(v, (b) => b.toString(16).padStart(2, '0')).join('')

	return v
}

/**
 * paint splits the document into the five things worth telling apart.
 *
 * One expression over the whole text rather than a parser: what is being
 * coloured is already known to be JSON -- `JSON.stringify` wrote it -- so
 * there is nothing to validate and nothing to recover from, and a tokenizer
 * would be a second implementation of a grammar nobody is going to change.
 */
function paint(text: string): ReactNode[] {
	const token = /("(?:\\.|[^"\\])*"\s*:)|("(?:\\.|[^"\\])*")|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|(true|false|null)/g

	const out: ReactNode[] = []
	let at = 0
	let m: RegExpExecArray | null
	let n = 0

	while ((m = token.exec(text)) !== null) {
		if (m.index > at) out.push(<span key={n++}>{text.slice(at, m.index)}</span>)

		const colour = m[1] !== undefined ? hue.key : m[2] !== undefined ? hue.str : m[3] !== undefined ? hue.num : hue.lit

		out.push(
			<span key={n++} style={{ color: colour }}>
				{m[0]}
			</span>,
		)
		at = m.index + m[0].length
	}

	if (at < text.length) out.push(<span key={n++}>{text.slice(at)}</span>)

	return out
}

/** dim is what punctuation is, for a caller that wants the same grey. */
export const punctuation = hue.rest
