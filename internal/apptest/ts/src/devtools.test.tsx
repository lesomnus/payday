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
import { Devtools, patchable } from '@lesomnus/payday/react/devtools'
import { Store } from '@lesomnus/payday/store'

import type { EntityDesc } from '@lesomnus/payday/store'

import { entities, Audit, Cell, Robot, Tenant } from '../gen/entities.js'
import { RobotSchema, type Robot as RobotMsg } from '../gen/app/robot_pb.js'
import { AuditSchema } from '../gen/app/payday/audit_pb.js'
import { TenantSchema } from '../gen/app/payday/tenant_pb.js'
import { RobotService, RobotListResponseSchema } from '../gen/app/robot_svc_pb.js'
import { AuditDomain, RobotDomain, TenantDomain } from '../gen/domains.js'

const id = pdid.newId(RobotDomain).bytes

/** listable is the picker's rule, read the way anything generic reads it. */
function listable(vs: readonly EntityDesc[]): EntityDesc[] {
	return vs.filter((v) => v.service?.method.list !== undefined)
}

let app: App
let store: Store
let answer: RobotMsg

/**
 * A trail row, which is the one with bytes that are not identifiers.
 *
 * The trace is deliberately sixteen bytes with the version nibble of a v4: it
 * is the shape a panel could mistake for a row, and the domain byte is what
 * says it is not.
 */
const trace = new Uint8Array(16).fill(0x4a)

/** What the fake was asked, so a test can say what did *not* happen. */
let asked: { method: string; req: unknown }[]
let trail: ReturnType<typeof create<typeof AuditSchema>>

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
	trail = create(AuditSchema, {
		id: pdid.newId(AuditDomain).bytes,
		traceId: trace,
		patch: new Uint8Array([0xde, 0xad, 0xbe, 0xef]),
		dateCreated: timestampFromDate(new Date('2026-01-01T00:00:00Z')),
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

/**
 * hex is what the dump says, whichever way it is being shown.
 *
 * The grid separates its bytes with non-breaking spaces so that a line does not
 * reflow while it is being typed in; this reads them as the spaces they stand
 * for, so one assertion covers both.
 */
function hex(): string {
	const el = screen.getByLabelText('hex')
	if (el instanceof HTMLTextAreaElement) return el.value

	// A row is a div and a byte under edit is an input, so neither the row
	// breaks nor what is being typed is in `textContent`.
	return Array.from(el.children)
		.map((row) =>
			Array.from(row.childNodes)
				.map((n) => (n instanceof HTMLInputElement ? n.value : (n.textContent ?? '')))
				.join(''),
		)
		.join('\n')
		.replace(/\u00a0/g, ' ')
}

/**
 * settle lets whatever the panel started off finish.
 *
 * An edge column resolves its name with a call of its own, and a fetch that
 * was started inside an `act` is not one that has answered by the time it
 * returns.
 */
async function settle(): Promise<void> {
	await act(async () => {
		await new Promise((go) => setTimeout(go, 0))
	})
}

/**
 * edge is the link in one column of the first served row.
 *
 * By column and not by the text in it, because an edge column shows what the
 * row is **called** -- which is the point of it, and is not a uuid to search
 * the screen for.
 */
function edge(of: EntityDesc, name: string, label = 'served'): HTMLElement {
	const at = of.schema.fields.findIndex((f) => f.localName === name)
	const cell = screen.getByLabelText(label).querySelectorAll('tbody td')[at]

	return cell?.querySelector('button') as HTMLElement
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

		// Two `List`s: the entity it opened on and the one picked. Nothing
		// else -- this answer carries no edge, and an edge is the only thing
		// that is looked up to be named.
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
		await mount()
		await pick(Robot.typeName)

		// The value in the table, edited where it is: two clicks on the cell,
		// which is the whole gesture.
		asked = []
		await act(async () => void fireEvent.doubleClick(screen.getByText('arm-01')))
		fireEvent.change(screen.getByLabelText('editing'), { target: { value: 'arm-02' } })
		await act(async () => void fireEvent.click(screen.getByText('save')))

		const sent = asked.find((v) => v.method === 'Patch')
		expect(sent).toBeDefined()

		const req = toJson(RobotService.method.patch.input, sent?.req as never) as Record<string, unknown>
		expect(req.alias).toBe('arm-02')
		expect(req.dateUpdated, 'the precondition travels with the write').toBeDefined()
	})

	it('edits one value at a time, so a half-typed one cannot be left behind', async () => {
		// Two rows, so that opening the second has a first to close.
		const other = create(RobotSchema, { id: pdid.newId(RobotDomain).bytes, alias: 'arm-09' })
		await act(async () => {
			answer = other
		})

		await mount()
		await pick(Robot.typeName)

		await act(async () => void fireEvent.doubleClick(screen.getByText('arm-09')))
		fireEvent.change(screen.getByLabelText('editing'), { target: { value: 'typed and forgotten' } })

		// The identifier's cell is not editable -- `patchable` says so -- so
		// the second edit is opened on the alias of the same row, which is
		// enough to prove the first one closed.
		await act(async () => void fireEvent.click(screen.getByText('cancel')))
		expect(screen.queryByLabelText('editing')).toBeNull()
		expect(screen.getByText('arm-09'), 'cancelling leaves the value alone').toBeDefined()
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

		// The name, with the `(id)` switch an edge column carries taken off:
		// what is being checked is which columns there are and in what order.
		const head = Array.from(screen.getByLabelText('served').querySelectorAll('th'), (v) =>
			(v.textContent ?? '').replace('(id)', ''),
		)
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

		// A reference is what the server answered with, so the cell is a link
		// to the row rather than the row itself. What it says is `an edge
		// column`'s subject; what this is about is that clicking it looks the
		// row up -- by identifier, whatever is written on it.
		const link = edge(Robot, 'tenant')
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

		// Nothing to go back to yet -- but the button keeps its place, because
		// one that appears and disappears moves the row it shares.
		const back = (): HTMLButtonElement => screen.getByLabelText('back') as HTMLButtonElement
		expect(back().disabled).toBe(true)

		await act(async () => void fireEvent.click(edge(Robot, 'tenant')))
		expect((screen.getByLabelText('entity') as HTMLSelectElement).value).toBe(Tenant.typeName)
		expect(back().disabled).toBe(false)

		await act(async () => void fireEvent.click(back()))
		expect((screen.getByLabelText('entity') as HTMLSelectElement).value).toBe(Robot.typeName)
		expect(back().disabled, 'the trail is empty again').toBe(true)
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

		const tip = screen.getByRole('tooltip')
		expect(tip.textContent).toBe('alias')

		// In the viewport and not in the table, which is what makes it
		// readable: the table scrolls inside a pane of its own, and a label
		// positioned in there is clipped by it.
		expect(tip.style.position).toBe('fixed')
	})
})

describe('how a query is read', () => {
	it('is one of three rather than three switches', async () => {
		await mount()

		const modes = screen.getByRole('group', { name: 'how to match' })
		expect(Array.from(modes.querySelectorAll('button'), (v) => v.getAttribute('aria-label'))).toEqual([
			'match fuzzy',
			'match text',
			'match regex',
		])

		// Pressing the one that is already on leaves it on, which is what a
		// group of three means and is why it is drawn as one control.
		await act(async () => void fireEvent.click(screen.getByLabelText('match fuzzy')))
		expect(screen.getByLabelText('match fuzzy').getAttribute('aria-pressed')).toBe('true')
	})

	// A glyph nobody has met before needs to say what it is, and the browser's
	// own `title` says it after a wait somebody has to know to sit through.
	it('says what a glyph means when it is hovered', async () => {
		await mount()

		expect(screen.queryByRole('tooltip')).toBeNull()
		await act(async () => void fireEvent.mouseEnter(screen.getByLabelText('match regex')))
		expect(screen.getByRole('tooltip').textContent).toBe('regex — a pattern')
	})
})

describe('the table as it is scrolled', () => {
	// The fake always answers `next: 'more'`, so what is being checked is that
	// scrolling asks again and that the answer is added to what is there --
	// not that paging ever ends.
	it('asks for the next page and keeps what it had', async () => {
		await mount()
		await pick(Robot.typeName)

		expect(screen.getAllByText('arm-01')).toHaveLength(1)

		asked = []
		await act(async () => void fireEvent.scroll(screen.getByLabelText('served').parentElement as Element))

		const sent = asked.filter((v) => v.method === 'List')
		expect(sent, 'scrolling to the end asks for the page after the cursor').toHaveLength(1)
		expect((sent[0]?.req as { after?: string }).after).toBe('more')
		expect(screen.getAllByText('arm-01'), 'the page is added, not swapped in').toHaveLength(2)
	})

	it('puts the cursor in the form, where it can be read and typed over', async () => {
		await mount()
		await pick(Robot.typeName)

		// Blank until something has paged: writing it on the first answer
		// makes the next press of `ask` a second page nobody asked for.
		expect((screen.getByLabelText('after') as HTMLInputElement).value).toBe('')

		await act(async () => void fireEvent.scroll(screen.getByLabelText('served').parentElement as Element))
		expect((screen.getByLabelText('after') as HTMLInputElement).value).toBe('more')
	})
})

describe('bytes that are not an identifier', () => {
	// A trail row carries a 16-byte OpenTelemetry trace and a marshalled patch
	// beside four identifiers, which is the whole of this: hex in a cell is a
	// column as wide as the document it holds, and one of those sixteen bytes
	// is not a row anybody can look up.
	it('say how big they are, and open beside the panel', async () => {
		store.put(Audit.typeName, trail)

		await mount()
		await pick(Audit.typeName)
		await tab('store')

		const open = screen.getByText('4 bytes')
		await act(async () => void fireEvent.click(open))

		const dialog = screen.getByRole('dialog')
		expect(dialog.getAttribute('aria-label')).toBe('patch bytes')
		expect(hex()).toBe('de ad be ef')
		expect(screen.getByLabelText('text').textContent).toBe('....')
		expect(screen.getByLabelText('offset').textContent).toBe('0000 0000')

		// Under the panel, which is what somebody is working in: an editor over
		// the top of it means closing the editor to do anything at all.
		expect(Number(dialog.style.zIndex)).toBeLessThan(2147483000)
	})

	it('are not read as a row when nothing here answers to their domain', async () => {
		store.put(Audit.typeName, trail)

		await mount()
		await pick(Audit.typeName)
		await tab('store')

		// A trace is sixteen bytes and every sixteen bytes can be read as a
		// UUID. What keeps it from being printed as somebody's row is the
		// domain byte, which nothing registered.
		expect(screen.getByText('16 bytes'), 'a trace is bytes, not an identifier').toBeDefined()
	})
})

describe('a hex dump', () => {
	/** long is a value with more than one line in it. */
	function long(): void {
		store.put(
			Audit.typeName,
			create(AuditSchema, {
				id: pdid.newId(AuditDomain).bytes,
				patch: new Uint8Array(20).map((_, i) => i),
			}),
		)
	}

	async function open(what: string): Promise<void> {
		await mount()
		await pick(Audit.typeName)
		await tab('store')
		await act(async () => void fireEvent.click(screen.getByText(what)))
	}

	// Four groups of four are read; sixteen pairs in a row are counted.
	it('groups the bytes in fours and says where each line starts', async () => {
		long()
		await open('20 bytes')

		expect(hex()).toBe('00 01 02 03  04 05 06 07  08 09 0a 0b  0c 0d 0e 0f\n10 11 12 13')

		// Counted from what is on each line, so that a line somebody shortens
		// moves the ones under it rather than lying about them -- and split in
		// half, because eight digits in a row is a number to be counted.
		expect(screen.getByLabelText('offset').textContent).toBe('0000 0000\n0000 0010')
	})

	// The gesture this exists for: click the byte, type two digits, and be on
	// the next one. No selecting the right two characters out of a wall.
	it('edits one byte and moves to the next', async () => {
		long()
		await open('20 bytes')

		await act(async () => void fireEvent.mouseDown(screen.getByText('02')))
		await act(async () => {
			fireEvent.change(screen.getByLabelText('byte 2'), { target: { value: 'ff' } })
		})

		// Two digits is the whole of a byte and there is no third thing it
		// could be waiting for, so it is already on the next one.
		expect(screen.getByLabelText('byte 3')).toBeDefined()
		expect(screen.queryByLabelText('byte 2')).toBeNull()

		// And the byte it moved off is what was typed. Read after clicking
		// away, since the one under edit is a box and not a pair of digits.
		await act(async () => void fireEvent.mouseDown(screen.getByRole('dialog')))
		expect(hex().startsWith('00 01 ff 03')).toBe(true)
	})

	it('stops editing when something that is not a byte is clicked', async () => {
		long()
		await open('20 bytes')

		await act(async () => void fireEvent.mouseDown(screen.getByText('02')))
		expect(screen.getByLabelText('byte 2')).toBeDefined()

		await act(async () => void fireEvent.mouseDown(screen.getByRole('dialog')))
		expect(screen.queryByLabelText('byte 2')).toBeNull()

		// And half a byte typed into it is left alone rather than written as
		// one: half a byte is not a value anybody meant.
		expect(hex().startsWith('00 01 02 03')).toBe(true)
	})

	// What a grid of cells cannot do is take a run out or paste a new value
	// over the whole thing.
	it('is a text box when a whole run has to be edited', async () => {
		long()
		await open('20 bytes')

		expect(screen.queryByRole('textbox', { name: 'hex' })).toBeNull()

		await act(async () => void fireEvent.click(screen.getByLabelText('edit as text')))

		const box = screen.getByLabelText('hex') as HTMLTextAreaElement
		expect(box.tagName).toBe('TEXTAREA')
		expect(box.value.startsWith('00 01 02 03')).toBe(true)
	})
})

describe('find', () => {
	/** three holders, so there is something to narrow. */
	function some(): void {
		for (const alias of ['arm-01', 'arm-02', 'crane-07']) {
			store.put(Robot.typeName, create(RobotSchema, { id: pdid.newId(RobotDomain).bytes, alias }))
		}
	}

	async function look(q: string, how?: string): Promise<string[]> {
		if (how !== undefined) {
			await act(async () => void fireEvent.click(screen.getByLabelText(`match ${how}`)))
		}
		await act(async () => {
			fireEvent.change(screen.getByLabelText('find'), { target: { value: q } })
		})

		const table = screen.getByLabelText('held')
		const at = Array.from(table.querySelectorAll('thead th')).findIndex((th) => th.textContent === 'alias')

		return Array.from(table.querySelectorAll('tbody tr')).map(
			(tr) => tr.querySelectorAll('td')[at]?.textContent ?? '',
		)
	}

	beforeEach(async () => {
		some()
		await mount()
		await pick(Robot.typeName)
		await tab('store')
	})

	it('keeps the rows something on the screen matches', async () => {
		expect(await look('arm', 'text')).toEqual(['arm-01', 'arm-02'])
		expect(screen.getByLabelText('matched').textContent).toBe('2 of 3')
	})

	// The letters in order and anything between them, which is the gesture
	// every editor has: four characters somebody remembers out of a uuid.
	//
	// `n` on purpose. The identifier column is on the screen too and it is
	// hex, so a query of `c7` matches whichever rows happen to have a `c`
	// before a `7` in a uuid drawn at random -- which is a test that passes
	// most of the time. A letter hex cannot hold asks the question that was
	// meant.
	it('takes the letters in order when it is asked to be fuzzy', async () => {
		expect(await look('cn7', 'fuzzy')).toEqual(['crane-07'])
	})

	it('takes a pattern when it is asked for a regex', async () => {
		expect(await look('-0[12]$', 'regex')).toEqual(['arm-01', 'arm-02'])
	})

	// A pattern half typed is not "no rows matched", which is what swallowing
	// the error would show and is indistinguishable from a wrong pattern.
	it('says what is wrong with a pattern rather than matching nothing', async () => {
		await look('arm-(', 'regex')
		expect(screen.getByText(/SyntaxError|Invalid regular expression/)).toBeDefined()
	})

	// Which is what makes fuzzy usable as the default: the letters it matched
	// are scattered by definition, so a row that looks like a false positive
	// has to be readable as one at a glance.
	it('lights up the part that matched', async () => {
		await look('cn7', 'fuzzy')

		// By its content and not by `getByText`, which cannot find a value the
		// highlight has broken into pieces -- which is the thing being tested.
		const row = Array.from(screen.getByLabelText('held').querySelectorAll('tbody tr')).find((tr) =>
			Array.from(tr.querySelectorAll('td')).some((td) => td.textContent === 'crane-07'),
		)
		expect(row).toBeDefined()

		const lit = Array.from(row?.querySelectorAll('mark') ?? []).map((v) => v.textContent)
		expect(lit, 'the letters it matched, in order').toEqual(['c', 'n', '7'])
	})

	it('is fuzzy until it is told otherwise, because that hides nothing', async () => {
		// A substring is also a subsequence, so the default never keeps back
		// what `text` would have found.
		expect(await look('crane')).toEqual(['crane-07'])
		expect(screen.getByLabelText('match fuzzy').getAttribute('aria-pressed')).toBe('true')
	})

	// A column somebody turned off is a column they said they are not reading.
	it('does not keep a row for what is in a column that is turned off', async () => {
		expect(await look('crane', 'text')).toEqual(['crane-07'])

		await act(async () => void fireEvent.click(screen.getByLabelText('alias')))
		expect(
			screen.getByLabelText('held').querySelectorAll('tbody tr'),
			'the only column that said crane is gone',
		).toHaveLength(0)
	})
})

describe('an edge column', () => {
	// `acme` is what somebody is looking for; the uuid is what they would have
	// to translate it into first.
	it('shows what the row is called, and says so is what `(id)` is struck for', async () => {
		const tenant = pdid.newId(TenantDomain).bytes
		answer = create(RobotSchema, { id, alias: 'arm-01', tenant: create(TenantSchema, { id: tenant }) })

		// The name is fetched, and the fake answers Robots -- so what comes
		// back for the tenant is the Robot, whose alias is `arm-01`. What is
		// being checked is which way the column resolves, not what it finds.
		await mount()
		await pick(Robot.typeName)
		await settle()

		const cell = (): string =>
			(screen.getByLabelText('served').querySelectorAll('tbody td')[
				Robot.schema.fields.findIndex((f) => f.localName === 'tenant')
			]?.textContent ?? '')

		expect(cell(), 'the name, not the identifier').toBe('arm-01')

		// Turned off, it is the identifier again -- and the switch is
		// remembered, like a hidden column is.
		await act(async () => void fireEvent.click(screen.getByLabelText('tenant as id')))
		expect(cell()).toBe(pdid.from(tenant).toString())
		expect(screen.getByLabelText('tenant as id').getAttribute('aria-pressed')).toBe('true')
	})

	it('does not turn the column off when the switch is pressed', async () => {
		answer = create(RobotSchema, {
			id,
			alias: 'arm-01',
			tenant: create(TenantSchema, { id: pdid.newId(TenantDomain).bytes }),
		})

		await mount()
		await pick(Robot.typeName)
		await settle()

		// The name beside it is a `label`, so a click that reached it would
		// turn the whole column off instead.
		await act(async () => void fireEvent.click(screen.getByLabelText('tenant as id')))
		expect((screen.getByLabelText('tenant') as HTMLInputElement).checked).toBe(true)
	})
})

describe('a column checkbox', () => {
	// A wide entity is twenty columns of which somebody wants three, so
	// without this the gesture is seventeen clicks.
	it('takes everything back to the last one when shift is held', async () => {
		await mount()
		await pick(Robot.typeName)

		const names = Robot.schema.fields.map((f) => f.localName)
		expect(names.length, 'this only says something over a run of columns').toBeGreaterThan(2)

		const first = names[0] as string
		const last = names[names.length - 1] as string

		await act(async () => void fireEvent.click(screen.getByLabelText(first)))
		await act(async () => {
			fireEvent.click(screen.getByLabelText(last), { shiftKey: true })
		})

		// Every one of them, and given the state of the one that was shifted
		// onto rather than each flipped -- a range that flipped would turn some
		// on and some off.
		for (const n of names) {
			expect((screen.getByLabelText(n) as HTMLInputElement).checked, n).toBe(false)
		}
	})
})

describe('a request form', () => {
	// The filters of a `List` are a repeated message, which is the shape a form
	// read off a descriptor has to handle to be worth having.
	it('is the request message, repeated fields and all', async () => {
		await mount()
		await pick(Robot.typeName)

		// `RobotListRequest` carries `filters`, `size` and `after`, and two of
		// them are boxes: `size` is the panel's own, because the table pages as
		// it is scrolled and a number somebody could type over would disagree
		// with the scrolling.
		expect(screen.getByLabelText('after')).toBeDefined()
		expect(screen.queryByLabelText('size'), 'the panel drives the page size').toBeNull()

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

		// `size` is the panel's own and not a box: the table pages as it is
		// scrolled, and a number somebody could type a different value into
		// would be a number that disagrees with the scrolling.
		expect(sent?.req).toEqual({ size: 50 })
	})
})
