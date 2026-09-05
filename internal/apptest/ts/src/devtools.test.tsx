// @vitest-environment jsdom

/**
 * The devtools panel, over this app's own generated declarations.
 *
 * It is here rather than in payday's package for the same reason `store.test.ts`
 * is: what is worth testing is the pair. The panel is written for *every*
 * entity, so a fixture chosen to suit it would prove nothing about whether a
 * real schema's declarations carry what it reads.
 *
 * The transport is a fake, because what these tests are about is which path was
 * taken and what was sent. Whether the server answers that way is
 * `query.test.ts`'s question, and it asks it of the real one.
 */

import { create, toJson } from '@bufbuild/protobuf'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { pdid } from '@lesomnus/payday'
import { Queries } from '@lesomnus/payday/query'
import { Provider, type App } from '@lesomnus/payday/react'
import { Devtools, Edit, patchable } from '@lesomnus/payday/react/devtools'
import { Store } from '@lesomnus/payday/store'

import type { EntityDesc } from '@lesomnus/payday/store'

import { entities, Cell, Robot, Tenant } from '../gen/entities.js'
import { RobotSchema, type Robot as RobotMsg } from '../gen/app/robot_pb.js'
import { TenantSchema } from '../gen/app/payday/tenant_pb.js'
import { RobotService, RobotListResponseSchema } from '../gen/app/robot_svc_pb.js'
import { RobotDomain, TenantDomain } from '../gen/domains.js'

const id = pdid.newId(RobotDomain).bytes

/** listable is the picker's rule, read the way anything generic reads it. */
function listable(vs: readonly EntityDesc[]): EntityDesc[] {
	return vs.filter((v) => v.service?.method.list !== undefined)
}

let app: App
let store: Store
let answer: RobotMsg

/** What the fake was asked, so a test can say what did *not* happen. */
let asked: { method: string; req: unknown }[]

function fake() {
	return {
		async unary(method: { name: string }, _s: unknown, _t: unknown, _h: unknown, req: unknown) {
			asked.push({ method: method.name, req })

			return {
				stream: false,
				service: RobotService,
				method,
				header: new Headers(),
				message:
					method.name === 'List'
						? create(RobotListResponseSchema, { items: [answer], next: 'more' })
						: answer,
				trailer: new Headers(),
			}
		},
		async *stream() {
			throw new Error('no streams here')
		},
	} as never
}

beforeEach(() => {
	answer = create(RobotSchema, {
		id,
		alias: 'arm-01',
		dateUpdated: timestampFromDate(new Date('2026-01-01T00:00:00Z')),
	})
	asked = []

	// The panel remembers what it was showing, which is the point and is also
	// what makes one test start where the last one left off.
	localStorage.clear()

	store = Store.open(entities, { name: 'devtools', identity: 'x' })
	app = { store, queries: new Queries(store, fake(), entities) }
})

afterEach(() => {
	cleanup()
	store.close()
})

async function mount(ungated?: unknown): Promise<void> {
	await act(async () =>
		void render(
			<Provider app={app}>
				<Devtools entities={entities} {...(ungated === undefined ? {} : { ungated: ungated as never })} />
			</Provider>,
		),
	)

	// It opens as a handle and nothing else, which is what a page that never
	// uses it sees. Everything below is about what is behind that.
	await act(async () => void fireEvent.click(screen.getByText('payday')))
}

/**
 * pick chooses an entity by name, which every test that asks the fake for rows
 * has to do: the panel opens on the first one it can list -- `app.Audit` here --
 * and the fake answers Robots.
 */
async function pick(typeName: string): Promise<void> {
	const select = screen.getByLabelText('entity')
	await act(async () => void fireEvent.change(select, { target: { value: typeName } }))
}

/** tab switches to one of the three. */
async function tab(name: string): Promise<void> {
	await act(async () => void fireEvent.click(screen.getByText(name)))
}

describe('the panel over a whole schema', () => {
	it('offers the entities that answer a List, and no others', async () => {
		await mount()

		const names = Array.from(screen.getByLabelText('entity').querySelectorAll('option'), (o) => o.textContent)

		expect(names).toContain(Robot.typeName)
		expect(names, 'Cell declares no `list:`').not.toContain(Cell.typeName)
		expect(names.length).toBe(listable(entities).length)
	})

	it('asks the server, and shows what it answered', async () => {
		await mount()
		await pick(Robot.typeName)

		expect(asked.map((v) => v.method)).toEqual(['List', 'List'])
		expect(screen.getByLabelText('served').textContent).toContain('arm-01')
	})

	// The whole reason the read goes past `Queries`: this tab and the store tab
	// are two answers to one question, and asking must not be what makes them
	// agree.
	it('does not put what it read into the store', async () => {
		await mount()
		await pick(Robot.typeName)

		expect(screen.getByLabelText('served').textContent).toContain('arm-01')
		expect(store.all(Robot.typeName)).toHaveLength(0)
	})

	it('shows the store beside it, which is a different answer', async () => {
		store.put(Robot.typeName, create(RobotSchema, { id, alias: 'held-by-the-page' }))

		await mount()
		await pick(Robot.typeName)
		await tab('store')

		const held = screen.getByLabelText('held').textContent
		expect(held).toContain('held-by-the-page')
		expect(held, 'the server said arm-01 and the store did not hear it').not.toContain('arm-01')
	})
})

describe('the way past the wall', () => {
	it('is not offered when the app handed over no such transport', async () => {
		await mount()

		expect(screen.queryByText('past the wall')).toBeNull()
	})

	// Structural rather than a flag: a served deployment has no ungated port,
	// so an app has nothing to pass and the switch cannot appear.
	it('is offered when it did, and asks the other one', async () => {
		let other = 0
		const ungated = {
			async unary(method: { name: string }) {
				other++

				return {
					stream: false,
					service: RobotService,
					method,
					header: new Headers(),
					message: create(RobotListResponseSchema, { items: [], next: '' }),
					trailer: new Headers(),
				}
			},
			async *stream() {
				throw new Error('no')
			},
		} as never

		await mount(ungated)
		await pick(Robot.typeName)
		expect(other).toBe(0)

		await act(async () => void fireEvent.click(screen.getByText('past the wall')))
		expect(other).toBe(1)
	})
})

describe('what a Patch may set', () => {
	it('is the request the schema generated, and not a list written here', () => {
		const method = Robot.service?.method.patch
		expect(method).toBeDefined()

		const got = patchable(method as never, Robot.version).map((f) => f.localName)

		// The mutable scalars, and nothing that is not one.
		expect(got).toContain('alias')
		expect(got, 'the row it is about is not a field to type').not.toContain('ref')
		expect(got, 'the version is a precondition, filled from the row').not.toContain('dateUpdated')
		expect(got, 'clearing is a companion flag, not a value').not.toContain('cellNull')
		expect(got, 'an edge is a reference rather than a box').not.toContain('cell')
	})

	it('sends the version it read, so a stale edit is refused rather than applied', async () => {
		store.put(Robot.typeName, answer)
		const row = store.all(Robot.typeName)[0]
		expect(row).toBeDefined()

		render(
			<Provider app={app}>
				<Edit entity={Robot} row={row as never} />
			</Provider>,
		)

		fireEvent.change(screen.getByLabelText('value'), { target: { value: 'arm-02' } })
		await act(async () => void fireEvent.click(screen.getByText('patch')))

		const sent = asked.find((v) => v.method === 'Patch')
		expect(sent).toBeDefined()

		const req = toJson(RobotService.method.patch.input, sent?.req as never) as Record<string, unknown>
		expect(req.alias).toBe('arm-02')
		expect(req.dateUpdated, 'the precondition travels with the write').toBeDefined()
	})
})

describe('the sheet', () => {
	it('is a handle and nothing else until somebody opens it', async () => {
		await act(async () =>
			void render(
				<Provider app={app}>
					<Devtools entities={entities} />
				</Provider>,
			),
		)

		// The whole of what a page that never uses this sees.
		expect(screen.getByText('payday')).toBeDefined()
		expect(screen.queryByLabelText('entity')).toBeNull()

		await act(async () => void fireEvent.click(screen.getByText('payday')))
		expect(screen.getByLabelText('entity')).toBeDefined()
	})

	// What it remembers is what makes it worth opening twice.
	it('opens where it was left, in the next page as well', async () => {
		await mount()
		await pick(Robot.typeName)
		await tab('store')

		cleanup()
		await act(async () =>
			void render(
				<Provider app={app}>
					<Devtools entities={entities} />
				</Provider>,
			),
		)

		expect(screen.queryByText('payday'), 'it was open when it was left').not.toBeNull()
		expect((screen.getByLabelText('entity') as HTMLSelectElement).value).toBe(Robot.typeName)
		expect(screen.getByLabelText('held')).toBeDefined()
	})
})

describe('the table', () => {
	it('is the message’s fields, in the order the schema declares them', async () => {
		await mount()
		await pick(Robot.typeName)

		const head = Array.from(screen.getByLabelText('served').querySelectorAll('th'), (v) => v.textContent)
		expect(head).toEqual(Robot.schema.fields.map((f) => f.localName))
	})

	it('hides a column, and remembers which', async () => {
		await mount()
		await pick(Robot.typeName)

		await act(async () => void fireEvent.click(screen.getByLabelText('alias')))

		const head = () => Array.from(screen.getByLabelText('served').querySelectorAll('th'), (v) => v.textContent)
		expect(head()).not.toContain('alias')
		expect(head(), 'only the one').toContain('secret')

		// And it is the entity's own setting rather than the panel's: another
		// entity is untouched by it.
		await pick(Tenant.typeName)
		expect(head()).toContain('alias')

		await pick(Robot.typeName)
		expect(head()).not.toContain('alias')
	})

	// protojson would render sixteen bytes as base64, which is not what anybody
	// has written down: an identifier is a uuid and carries its entity in the
	// ninth byte.
	it('shows an identifier the way it is typed', async () => {
		await mount()
		await pick(Robot.typeName)

		expect(screen.getByLabelText('served').textContent).toContain(pdid.from(id).toString())
	})
})

describe('an edge', () => {
	it('shows the row it names, and looking it up is a click', async () => {
		const tenant = pdid.newId(TenantDomain).bytes
		answer = create(RobotSchema, {
			id,
			alias: 'arm-01',
			tenant: create(TenantSchema, { id: tenant }),
		})

		await mount()
		await pick(Robot.typeName)

		// The identifier, not the row: what the server answered with is a
		// reference, so there is nothing else to show.
		const link = screen.getByText(pdid.from(tenant).toString())
		expect(link.tagName).toBe('BUTTON')

		asked = []
		await act(async () => void fireEvent.click(link))

		// It followed the edge into the entity it names, by identifier -- and
		// the form says so, because what was sent is what is on the screen.
		expect(asked.map((v) => v.method)).toEqual(['Get'])
		expect((screen.getByLabelText('entity') as HTMLSelectElement).value).toBe(Tenant.typeName)
		expect((screen.getByLabelText('ref.key') as HTMLSelectElement).value).toBe('id')
		expect((screen.getByLabelText('ref.id') as HTMLInputElement).value).toBe(pdid.from(tenant).toString())
	})
})

describe('the get form', () => {
	it('asks for one row by identifier', async () => {
		await mount()
		await tab('get')
		await pick(Robot.typeName)

		asked = []
		await act(async () => {
			fireEvent.change(screen.getByLabelText('ref.key'), { target: { value: 'id' } })
		})
		await act(async () => {
			fireEvent.change(screen.getByLabelText('ref.id'), { target: { value: pdid.from(id).toString() } })
		})
		await act(async () => void fireEvent.click(screen.getByText('look up')))

		expect(asked.map((v) => v.method)).toEqual(['Get'])

		// One row is a document rather than a row of a table, so it is shown
		// as one.
		expect(screen.queryByLabelText('served'), 'a Get is not a table').toBeNull()
		expect(document.body.textContent).toContain('arm-01')
	})

	// A `Get` exists for every entity and a `List` does not, so the two tabs
	// offer different entities -- which is what makes following an edge into
	// something no page lists work at all.
	it('offers entities the list tab cannot', async () => {
		await mount()
		await tab('get')

		const names = Array.from(screen.getByLabelText('entity').querySelectorAll('option'), (o) => o.textContent)
		expect(names).toContain(Cell.typeName)
		expect(names.length).toBeGreaterThan(listable(entities).length)
	})
})

describe('following edges', () => {
	it('comes back the way it went', async () => {
		const tenant = pdid.newId(TenantDomain).bytes
		answer = create(RobotSchema, { id, alias: 'arm-01', tenant: create(TenantSchema, { id: tenant }) })

		await mount()
		await pick(Robot.typeName)

		// Nothing to go back to before anything was followed.
		expect(screen.queryByLabelText('back')).toBeNull()

		await act(async () => void fireEvent.click(screen.getByText(pdid.from(tenant).toString())))
		expect((screen.getByLabelText('entity') as HTMLSelectElement).value).toBe(Tenant.typeName)

		await act(async () => void fireEvent.click(screen.getByLabelText('back')))
		expect(screen.queryByLabelText('back'), 'the trail is empty again').toBeNull()
	})
})

describe('a column that is turned off', () => {
	it('collapses to its checkbox, and says its name on hover', async () => {
		await mount()
		await pick(Robot.typeName)

		// The box is in the header, beside the name.
		const head = () => Array.from(screen.getByLabelText('served').querySelectorAll('th'), (v) => v.textContent)
		expect(head()).toContain('alias')

		await act(async () => void fireEvent.click(screen.getByLabelText('alias')))
		expect(head(), 'the name goes and the box stays').not.toContain('alias')
		expect(screen.getByLabelText('alias'), 'the box is still there to turn back on').toBeDefined()

		// Hovering it brings the name back, over the rows rather than in them.
		expect(screen.queryByRole('tooltip')).toBeNull()
		await act(async () => void fireEvent.mouseEnter(screen.getByLabelText('alias').parentElement as Element))
		expect(screen.getByRole('tooltip').textContent).toBe('alias')
	})
})

describe('a request form', () => {
	// The filters of a `List` are a repeated message, which is the shape a form
	// read off a descriptor has to handle to be worth having.
	it('is the request message, repeated fields and all', async () => {
		await mount()
		await pick(Robot.typeName)

		// `RobotListRequest` carries `filters`, `size` and `after`.
		expect(screen.getByLabelText('size')).toBeDefined()
		expect(screen.getByLabelText('after')).toBeDefined()

		asked = []
		await act(async () => void fireEvent.click(screen.getByText('add')))
		await act(async () => {
			fireEvent.change(screen.getByLabelText('filters.0.ref.key'), { target: { value: 'id' } })
		})
		await act(async () => {
			fireEvent.change(screen.getByLabelText('filters.0.ref.id'), { target: { value: pdid.from(id).toString() } })
		})
		await act(async () => void fireEvent.click(screen.getByText('ask')))

		const sent = asked.find((v) => v.method === 'List')
		expect(sent).toBeDefined()

		const req = sent?.req as { filters: { ref: { key: { case: string; value: Uint8Array } } }[] }
		expect(req.filters).toHaveLength(1)
		expect(req.filters[0]?.ref.key.case).toBe('id')
		expect(Array.from(req.filters[0]?.ref.key.value ?? [])).toEqual(Array.from(id))
	})

	// A box nobody typed in is a field nobody asked about, which is not the
	// same as one asked about with its default.
	it('sends nothing for what was left blank', async () => {
		await mount()
		await pick(Robot.typeName)

		asked = []
		await act(async () => void fireEvent.click(screen.getByText('ask')))

		const sent = asked.find((v) => v.method === 'List')
		expect(sent?.req).toEqual({})
	})
})
