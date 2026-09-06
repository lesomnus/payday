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
import { afterEach, describe, expect, it } from 'vitest'

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
	const diffs: { readOnly: unknown }[] = []

	const monaco = {
		editor: {
			create: () => ({ dispose: () => undefined }),
			createDiffEditor: () => ({
				setModel: () => undefined,
				getModifiedEditor: () => ({
					updateOptions: (o: Record<string, unknown>) => diffs.push({ readOnly: o.readOnly }),
				}),
				dispose: () => undefined,
			}),
			createModel: (v: string) => {
				const m = model(v)
				made.push(m)

				return m
			},
		},
		Uri: { parse: (v: string) => ({ toString: () => v }) },
		json: { setDiagnosticsOptions: () => undefined },
	} satisfies MonacoLike

	return { monaco, made, diffs }
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

	it('says read-only to the modified side again when it is a diff', async () => {
		const { monaco, made, diffs } = fake()

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

		// Two models -- the diff is against a document of its own -- and the
		// modified side told again, because the diff editor does not pass the
		// option down and builds one that refuses every keystroke.
		expect(made).toHaveLength(2)
		expect(diffs).toEqual([{ readOnly: false }])
	})
})
