// @vitest-environment jsdom

/**
 * The React binding, in a DOM.
 *
 * Everything under it is checked elsewhere and against the real server: the
 * store's rules in `store.test.ts`, the query layer's in `query.test.ts`. What
 * is left is the thirty lines that turn a subscription into a render, and the
 * only way to know they do is to render something and change a row under it.
 *
 * The transport is a fake here, deliberately. What these tests are about is
 * **who re-rendered**, and a real server would make that a question about
 * timing as well. Whether the server sends what this believes is
 * `query.test.ts`'s question, and it asks it of the real one.
 */

import { create } from '@bufbuild/protobuf'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { pdid } from '@lesomnus/payday'
import { Queries } from '@lesomnus/payday/query'
import { Provider, useQuery, useRow, type App } from '@lesomnus/payday/react'
import { Store } from '@lesomnus/payday/store'

import { entities, Robot } from '../gen/entities.js'
import { RobotSchema, type Robot as RobotMsg } from '../gen/app/robot_pb.js'
import { RobotService, RobotListResponseSchema } from '../gen/app/robot_svc_pb.js'
import { RobotDomain } from '../gen/domains.js'

const id = pdid.newId(RobotDomain).bytes

function robot(alias: string, at: Date): RobotMsg {
	return create(RobotSchema, { id, alias, dateUpdated: timestampFromDate(at) })
}

let app: App
let answer: RobotMsg

beforeEach(() => {
	answer = robot('arm-01', new Date('2026-01-01T00:00:00Z'))

	const store = Store.open(entities, { name: 'react', identity: 'x' })
	const transport = {
		async unary() {
			return {
				stream: false,
				service: RobotService,
				method: RobotService.method.list,
				header: new Headers(),
				message: create(RobotListResponseSchema, { items: [answer] }),
				trailer: new Headers(),
			}
		},
		// No watch: these tests move rows by hand, which is what "somebody else
		// changed it" looks like from in here.
		async *stream() {
			throw new Error('no stream')
		},
	} as never

	app = { store, queries: new Queries(store, transport, entities) }
})

afterEach(cleanup)

/** One component drawing one row, by name so a test can find it. */
function Alias(props: { at: string }): React.ReactNode {
	const { data } = useQuery(RobotService.method.list, { filters: [] })

	return <span data-testid={props.at}>{data?.items[0]?.alias ?? '...'}</span>
}

/** flush lets whatever the first render started finish. */
async function flush(): Promise<void> {
	await act(async () => {
		await new Promise((ok) => setTimeout(ok, 0))
	})
}

describe('a component draws what a query answered', () => {
	it('renders what came back', async () => {
		render(
			<Provider app={app}>
				<Alias at="a" />
			</Provider>,
		)

		// Nothing has answered yet, and the hook says so rather than throwing or
		// suspending -- what to show while waiting is the app's decision.
		expect(screen.getByTestId('a').textContent).toBe('...')

		await flush()
		expect(screen.getByTestId('a').textContent).toBe('arm-01')
	})
})

describe('one row, two places, one change', () => {
	it('re-renders both, with nothing joining them up', async () => {
		render(
			<Provider app={app}>
				<Alias at="a" />
				<Alias at="b" />
			</Provider>,
		)

		await flush()
		expect(screen.getByTestId('a').textContent).toBe('arm-01')
		expect(screen.getByTestId('b').textContent).toBe('arm-01')

		// Somebody else changes it. On a page this is a `Watch` item, another
		// query's answer, or this app's own write somewhere far from here --
		// they all end up in the same place, which is the point.
		await act(async () => {
			app.store.put(
				Robot.typeName,
				create(RobotSchema, {
					id,
					alias: 'renamed',
					dateUpdated: timestampFromDate(new Date('2026-01-02T00:00:00Z')),
				}),
			)
		})

		expect(screen.getByTestId('a').textContent).toBe('renamed')
		expect(screen.getByTestId('b').textContent).toBe('renamed')
	})

	it('does not re-render for an answer it decided to ignore', async () => {
		let renders = 0

		function Counting(): React.ReactNode {
			renders++
			const { data } = useQuery(RobotService.method.list, { filters: [] })

			return <span data-testid="c">{data?.items[0]?.alias ?? '...'}</span>
		}

		render(
			<Provider app={app}>
				<Counting />
			</Provider>,
		)
		await flush()

		const was = renders
		await act(async () => {
			// Older than what is held, so the store keeps what it has -- and a
			// screen that redrew for it would be redrawing for nothing.
			app.store.put(
				Robot.typeName,
				create(RobotSchema, {
					id,
					alias: 'stale',
					dateUpdated: timestampFromDate(new Date('2025-01-01T00:00:00Z')),
				}),
			)
		})

		expect(renders).toBe(was)
		expect(screen.getByTestId('c').textContent).toBe('arm-01')
	})
})

describe('useRow reads a row on its own', () => {
	it('follows the row and stops when the component goes', async () => {
		function Neighbour(): React.ReactNode {
			const v = useRow<RobotMsg>(Robot.typeName, id)

			return <span data-testid="n">{v?.alias ?? 'nothing'}</span>
		}

		const { unmount } = render(
			<Provider app={app}>
				<Neighbour />
			</Provider>,
		)

		// Nothing has it yet, which is the caller's cue to go and ask.
		expect(screen.getByTestId('n').textContent).toBe('nothing')

		await act(async () => {
			app.store.put(Robot.typeName, answer)
		})
		expect(screen.getByTestId('n').textContent).toBe('arm-01')

		// And letting go really lets go: a store that went on calling a
		// component that is not there is a leak and a warning in every console.
		unmount()
		app.store.put(
			Robot.typeName,
			create(RobotSchema, {
				id,
				alias: 'after',
				dateUpdated: timestampFromDate(new Date('2026-02-01T00:00:00Z')),
			}),
		)
	})
})
