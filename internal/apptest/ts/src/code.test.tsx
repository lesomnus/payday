// @vitest-environment jsdom

/**
 * The editor's two directions, over a monaco that is not there.
 *
 * `MonacoLike` is four signatures on purpose, and this is what that buys: the
 * component can be driven without fifteen megabytes and a real DOM, and what
 * is worth pinning here is not what Monaco draws -- it is which way a document
 * moves. Both of the bugs below were found in a browser by typing, and neither
 * would have been found by looking at the screen for less than a sentence.
 */

import { act, cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { Code, type MonacoLike } from '@lesomnus/payday/react/devtools'

afterEach(cleanup)

/** A model that records what is done to it. */
function model(value: string) {
	let now = value
	const cbs: (() => void)[] = []

	return {
		sets: [] as string[],
		getValue: () => now,
		setValue(v: string) {
			this.sets.push(v)
			now = v
			for (const cb of cbs) cb()
		},
		onDidChangeContent(cb: () => void) {
			cbs.push(cb)

			return { dispose: () => undefined }
		},
		dispose: () => undefined,
		/** What typing into it looks like from outside. */
		type(v: string) {
			now = v
			for (const cb of cbs) cb()
		},
	}
}

function fake() {
	const made: ReturnType<typeof model>[] = []
	const opts: { editor?: Record<string, unknown>; diff?: Record<string, unknown> } = {}

	const monaco = {
		editor: {
			create: (_el: HTMLElement, o: Record<string, unknown>) => {
				opts.editor = o

				return { dispose: () => undefined }
			},
			createDiffEditor: (_el: HTMLElement, o: Record<string, unknown>) => {
				opts.diff = o

				return { setModel: () => undefined, dispose: () => undefined }
			},
			createModel: (v: string) => {
				const m = model(v)
				made.push(m)

				return m
			},
		},
		Uri: { parse: (v: string) => ({ toString: () => v }) },
		json: { setDiagnosticsOptions: () => undefined },
	} satisfies MonacoLike

	return { monaco, made, opts }
}

describe('a document in an editor', () => {
	it('leaves what is being typed alone, whatever the parent is holding', async () => {
		const { monaco, made } = fake()

		const view = render(
			<Code monaco={monaco} uri="a.json" value="one" schema={undefined} readOnly={false} onChange={() => undefined} />,
		)

		const m = made[0]
		expect(m).toBeDefined()

		// Two keystrokes before React has re-rendered from the first, which is
		// what typing is: the parent then re-renders holding the older text.
		await act(async () => {
			m?.type('on')
			m?.type('one!')
		})

		m?.sets.splice(0)
		await act(async () => {
			view.rerender(
				<Code monaco={monaco} uri="a.json" value="on" schema={undefined} readOnly={false} onChange={() => undefined} />,
			)
		})

		// Putting it back would throw away the keystroke that came after, and
		// nothing in here can tell that from an answer.
		expect(m?.sets, 'a later value is this editor a render behind').toEqual([])
		expect(m?.getValue()).toBe('one!')
	})

	it('is a new editor when the answer is a new one', async () => {
		const { monaco, made } = fake()

		const view = render(
			<Code key={1} monaco={monaco} uri="a.json" value="one" schema={undefined} readOnly={false} onChange={() => undefined} />,
		)
		expect(made).toHaveLength(1)

		// Which is how a second `Get` replaces what is on the screen: the key
		// is the count of answers, so looking the same row up again counts.
		await act(async () => {
			view.rerender(
				<Code key={2} monaco={monaco} uri="a.json" value="two" schema={undefined} readOnly={false} onChange={() => undefined} />,
			)
		})

		expect(made).toHaveLength(2)
		expect(made[1]?.getValue()).toBe('two')
	})

	it('is the document on the left and a diff of it on the right', async () => {
		const { monaco, made, opts } = fake()

		render(
			<Code
				monaco={monaco}
				uri="a.json"
				value="two"
				original="one"
				schema={undefined}
				readOnly={false}
				onChange={() => undefined}
			/>,
		)

		// Three models: what is being typed, what was read, and the copy of
		// the first that the diff is computed against -- the copy being what
		// the delay is on, since the live one cannot be delayed.
		expect(made.map((m) => m.getValue())).toEqual(['two', 'one', 'two'])

		// The half that is edited is the plain editor; the diff is read-only
		// and inline, because half the width cannot hold two columns.
		expect(opts.editor?.readOnly).toBe(false)
		expect(opts.diff?.readOnly).toBe(true)
		expect(opts.diff?.renderSideBySide).toBe(false)
	})

	it('waits for typing to stop before it works the diff out', async () => {
		vi.useFakeTimers()
		try {
			const { monaco, made } = fake()

			render(
				<Code
					monaco={monaco}
					uri="a.json"
					value="one"
					original="one"
					schema={undefined}
					readOnly={false}
					onChange={() => undefined}
					debounceMs={300}
				/>,
			)

			const live = made[0]
			const snap = made[2]
			expect(live).toBeDefined()
			expect(snap).toBeDefined()

			live?.type('on')
			live?.type('one!')
			expect(snap?.getValue(), 'nothing while it is still being typed').toBe('one')

			await act(async () => {
				vi.advanceTimersByTime(300)
			})
			expect(snap?.getValue()).toBe('one!')
		} finally {
			vi.useRealTimers()
		}
	})
})
