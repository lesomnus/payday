/**
 * Reads that keep themselves up to date.
 *
 * # What this is for
 *
 * A screen calls an RPC to show something. Because the call goes through here,
 * this knows **which rows were drawn** -- so when one of them changes, whatever
 * drew it is told, and every place it appears changes at once. Nothing is
 * declared: the dependency is what rendering asked for.
 *
 * That is the whole of it, and everything else follows:
 *
 *   - two components asking the same thing share one call and one entry;
 *   - a row written by any answer -- another query, a `Watch`, a write -- shows
 *     up wherever it is on screen, because they are all reading one copy;
 *   - and what a `Watch` is *for* becomes clear. Without it "stale" is a guess
 *     with a timer on it. With it the server says which row changed and what it
 *     is now, so a row on screen and watched is not stale, and cache
 *     invalidation shrinks to the rows on screen that nobody is watching.
 *
 * # What it does not do
 *
 * Decide anything about the wire. A query is a method descriptor and a request;
 * what carries it is the `Transport` handed in, which is a real server in
 * production and a Go server compiled to wasm in the sandbox, and neither is
 * known here.
 *
 * @module
 */

import {
	create,
	fromBinary,
	toBinary,
	type DescField,
	type DescMessage,
	type DescMethod,
	type DescMethodUnary,
	type DescService,
	type Message,
	type MessageInitShape,
	type MessageShape,
} from '@bufbuild/protobuf'
import { createClient, type Transport } from '@connectrpc/connect'

import { key, type EntityDesc, type Key, type Store } from '../store/index.js'

// Declared rather than putting `DOM` in `lib`: these two are in a page, a
// worker and Node, and they are all this module needs from outside the
// language. See `store/identity.ts` for the same reasoning.
type AbortSignal = { readonly aborted: boolean }
type Aborter = { readonly signal: AbortSignal; abort(): void }
declare const AbortController: { new (): Aborter }

/** State is where a query has got to. */
export type State = 'pending' | 'ok' | 'error'

/** Entry is one query: the same object for as long as anybody is asking. */
export interface Entry<O extends Message = Message> {
	readonly key: string
	readonly state: State

	/**
	 * The answer, with every row in it read from the store rather than from
	 * what arrived -- which is what makes one row shown in six places six
	 * views of one thing.
	 */
	readonly data: O | undefined

	readonly error: unknown

	/** How many times this answer has changed, for whoever caches on it. */
	readonly rev: number
}

interface Live<O extends Message = Message> {
	key: string
	method: DescMethod
	input: Message
	entry: Entry<O>

	/** What this query was asked with, kept so a re-read is the same query. */
	opts: QueryOpts

	/** The rows this answer named, in the order they were found. */
	refs: { typeName: string; id: string }[]

	/** What arrived, kept as the shape to rebuild from the store. */
	res: Message | undefined

	listeners: Set<() => void>
	off: (() => void) | undefined
	/** Nobody is drawing this; see [Queries.rest]. */
	idle: boolean

	/** Its watch has been reopened once already; see [Queries.watch]. */
	retried: boolean

	/** Which read is the current one; see [Queries.run]. */
	gen: number

	stop: Aborter | undefined
	cache: { rev: number; data: Message | undefined } | undefined
}

/** CallOpts is what a write may be told beyond its request. */
export interface CallOpts {
	/**
	 * Re-read the lists whose membership this write may have changed.
	 *
	 * On by default. Off for a caller making a run of writes who reads once
	 * when they are done -- and who then owns that read, since nothing else
	 * will make it.
	 */
	readonly revalidate?: boolean
}

/** Opts is what a query may be told beyond its request. */
export interface QueryOpts {
	/**
	 * Open the sibling `Watch` for as long as anybody is asking this, so the
	 * rows it answered with stay current without anybody polling.
	 *
	 * On by default when the service has one and this query names at least
	 * one filter, because the alternative is a screen that is quietly wrong
	 * and a timer somebody has to tune. A filterless list is left unwatched:
	 * the server refuses a watch over the whole table, so a page that wants
	 * liveness says which rows it is about. `true` insists either way.
	 */
	readonly watch?: boolean
}

/** Queries is every read a page has asked for, and what each of them named. */
export class Queries {
	private readonly store: Store
	private readonly transport: Transport
	private readonly live = new Map<string, Live>()
	private readonly clients = new Map<DescService, Record<string, unknown>>()

	/** Which entities each method answers with a set of; see [Queries.setsOf]. */
	private readonly sets = new Map<DescMethod, ReadonlySet<string>>()

	/** Which message types are entities, by full name. */
	private readonly entities: Map<string, EntityDesc>

	constructor(store: Store, transport: Transport, entities: readonly EntityDesc[]) {
		this.store = store
		this.transport = transport
		this.entities = new Map(entities.map((v) => [v.typeName, v]))
	}

	/**
	 * get answers with the entry for one query, starting it if nobody has.
	 *
	 * The same request twice is the same entry: `keyOf` is the method and the
	 * request, so two components showing the same thing make one call.
	 */
	get<I extends DescMessage, O extends DescMessage>(
		method: DescMethodUnary<I, O>,
		input: MessageInitShape<I>,
		opts: QueryOpts = {},
	): Entry<MessageShape<O>> {
		// Created before it is keyed, so that two callers who wrote the same
		// request differently -- one leaving a default out -- ask one question.
		const req = create(method.input, input) as Message
		const k = keyOf(method, req)

		let v = this.live.get(k)
		if (v === undefined) {
			v = {
				key: k,
				method: method as DescMethod,
				input: req,
				entry: { key: k, state: 'pending', data: undefined, error: undefined, rev: 0 },
				opts,
				refs: [],
				res: undefined,
				listeners: new Set(),
				off: undefined,
				idle: false,
				retried: false,
				gen: 0,
				stop: undefined,
				cache: undefined,
			}
			this.live.set(k, v)

			// What this query answered last time the page was open, if the
			// store was given a mirror and has been hydrated. It is settled
			// **before** the read behind it starts, so a component that just
			// mounted draws its list on its first frame rather than a spinner
			// for something the tab had a second ago.
			this.restore(v)
			void this.run(v)
		}

		return v.entry as Entry<MessageShape<O>>
	}

	/**
	 * call makes a write and puts what it answered with into the store.
	 *
	 * This is the read path run backwards, and for the same reason. A write
	 * answers with the row it wrote, so absorbing it means **every place
	 * showing that row is right immediately** -- the form that submitted it,
	 * the list it is in, the header counting them -- without any of them being
	 * told, and without waiting for a `Watch` to come back around.
	 *
	 * What the answer cannot say is that a set changed. A row that was just
	 * created belongs in lists whose answers were settled before it existed,
	 * and no amount of reading the response reveals which. So after a write the
	 * lists over the entities it touched are read again -- only the ones
	 * somebody is drawing, since a query at rest re-reads when it is drawn
	 * again anyway.
	 *
	 * Deciding that locally instead -- inserting the row into the list it looks
	 * like it belongs in -- would mean evaluating the server's filter and the
	 * server's order against a partial copy, and being confidently wrong about
	 * a page boundary. The re-read is a round trip and it is the true answer.
	 *
	 * A removal is the one write whose answer names no row -- `Erase` answers
	 * with whether this call erased, not with what it erased -- so the subject
	 * is read from the request: `Erase` names a row, and a row erased is gone
	 * here at once. Anything else that removes -- an app's own `Deactivate` --
	 * says so with `store.apply`.
	 */
	async call<I extends DescMessage, O extends DescMessage>(
		method: DescMethodUnary<I, O>,
		input: MessageInitShape<I>,
		opts: CallOpts = {},
	): Promise<MessageShape<O>> {
		const req = create(method.input, input) as Message
		const res = (await this.invoke(method as DescMethod, req)) as Message

		const touched = new Set<string>()
		for (const r of this.absorb(method.output, res)) touched.add(r.typeName)

		const gone = this.erased(method as DescMethod, req)
		if (gone !== undefined) {
			// The identifier is there only when the row was named by one. A ref
			// by slug says which row to the server and not to this, and the
			// re-read below is what takes it off the screen.
			if (gone.id !== undefined) this.store.apply(gone.typeName, [{ id: gone.id }])
			touched.add(gone.typeName)
		}

		if (opts.revalidate !== false) this.revalidate(touched)

		return res as MessageShape<O>
	}

	/**
	 * subscribe tells `cb` when this query's answer changes -- because it
	 * arrived, or because one of the rows it named did.
	 */
	subscribe(key: string, cb: () => void): () => void {
		const v = this.live.get(key)
		if (v === undefined) return () => {}

		const first = v.listeners.size === 0
		v.listeners.add(cb)
		if (first) this.wake(v)

		return () => {
			v.listeners.delete(cb)
			if (v.listeners.size === 0) this.rest(v)
		}
	}

	/**
	 * forget drops a query's answer so the next ask goes to the server.
	 *
	 * Rarely what is wanted. A row on screen and watched is not stale -- the
	 * server says when it changes -- so this is for the ones nothing is
	 * watching, and for "I have a reason to believe the world moved".
	 */
	forget(method?: DescMethod, input?: Message): void {
		for (const w of [...this.live.values()]) {
			if (method !== undefined && w.method !== method) continue
			if (input !== undefined && method !== undefined && w.key !== keyOf(method, input)) continue

			w.cache = undefined
			this.store.setBlob(w.key, undefined)
			void this.run(w)
		}
	}

	/**
	 * run makes the call and writes what comes back.
	 *
	 * Numbered, because a query is read again for reasons that overlap -- a
	 * write landed, a stream reopened, a rested page came back -- and two reads
	 * in flight answer in whatever order the network chose. The rows are safe
	 * either way, since the store orders them by version; the **membership** is
	 * not, so the older read stands down rather than settling an answer that
	 * has already been superseded.
	 */
	private async run(v: Live): Promise<void> {
		const gen = ++v.gen
		try {
			const res = (await this.invoke(v.method, v.input)) as Message
			if (gen !== v.gen) return

			// Into the store first, so the rows exist before anything reads
			// them, and the entry names them afterwards.
			v.refs = this.absorb(v.method.output, res)
			v.res = res

			this.settle(v, 'ok', undefined)
			this.watch(v)
		} catch (err) {
			if (gen !== v.gen) return

			this.settle(v, 'error', err)
		}
	}

	private async invoke(method: DescMethod, input: Message): Promise<unknown> {
		let client = this.clients.get(method.parent)
		if (client === undefined) {
			client = createClient(method.parent, this.transport) as unknown as Record<string, unknown>
			this.clients.set(method.parent, client)
		}

		const f = client[method.localName] as (v: Message) => Promise<unknown>

		return f(input)
	}

	/**
	 * absorb writes every entity a response carried into the store, and answers
	 * with what it named.
	 *
	 * The order is the response's, because that is the order the server chose
	 * and it is the only one that means anything -- a page shows a list in the
	 * order a `List` answered, sorted by a key the server has an index for.
	 */
	private absorb(desc: DescMessage, v: Message): { typeName: string; id: string }[] {
		const refs: { typeName: string; id: string }[] = []

		const walk = (d: DescMessage, m: Message): void => {
			const entity = this.entities.get(d.typeName)
			if (entity !== undefined) {
				const id = key((m as unknown as { id?: Uint8Array }).id)
				if (id !== '') {
					this.store.put(d.typeName, m)
					refs.push({ typeName: d.typeName, id })
				}

				return
			}

			for (const f of d.fields) {
				const at = messageOf(f)
				if (at === undefined) continue

				const got = (m as unknown as Record<string, unknown>)[f.localName]
				if (got === undefined || got === null) continue

				if (f.fieldKind === 'list') {
					for (const w of got as Message[]) walk(at, w)
				} else {
					walk(at, got as Message)
				}
			}
		}

		walk(desc, v)

		return refs
	}

	/**
	 * data is the answer with its rows read from the store.
	 *
	 * Cached on `rev`, which is what `useSyncExternalStore` needs: a snapshot
	 * that is the same object until something actually changed, or React
	 * renders forever.
	 */
	private materialize(v: Live): Message | undefined {
		if (v.res === undefined) return undefined
		if (v.cache !== undefined && v.cache.rev === v.entry.rev) return v.cache.data

		const rebuild = (d: DescMessage, m: Message): Message | undefined => {
			if (this.entities.has(d.typeName)) {
				const id = key((m as unknown as { id?: Uint8Array }).id)

				// The store's copy when there is one, and what arrived when
				// there is not: a neighbour that came back as a reference was
				// never written, and dropping it would lose which row it names.
				// Undefined only when a `Watch` said the row is gone.
				const row = this.store.row(d.typeName, id)
				if (row === undefined) return this.store.desc(d.typeName).version === undefined ? m : undefined

				return this.store.message(d.typeName, row)
			}

			const init: Record<string, unknown> = { ...(m as unknown as Record<string, unknown>) }
			for (const f of d.fields) {
				const at = messageOf(f)
				if (at === undefined) continue

				const got = init[f.localName]
				if (got === undefined || got === null) continue

				if (f.fieldKind === 'list') {
					init[f.localName] = (got as Message[])
						.map((w) => rebuild(at, w))
						.filter((w): w is Message => w !== undefined)
				} else {
					init[f.localName] = rebuild(at, got as Message)
				}
			}

			return create(d, init as never)
		}

		const data = rebuild(v.method.output, v.res)
		v.cache = { rev: v.entry.rev, data }

		return data
	}

	/**
	 * restore is what this query answered the last time the page was open.
	 *
	 * The answer is kept as the bytes of the response it was, beside the rows
	 * and under the same credential -- see [Store.blob]. Which is the whole
	 * trick: the rows came back with [Store.hydrate], so restoring the
	 * **order and the membership** is what is left, and that is exactly a
	 * response.
	 *
	 * It is as old as the tab that wrote it, and it is drawn as though it were
	 * true, because [Queries.run] is already on its way behind it. A row it
	 * names that has since been erased is on screen for one round trip; that is
	 * what "cached" costs, and it is the same bargain [Queries.rest] makes.
	 */
	private restore(v: Live): void {
		const held = this.store.blob(v.key)
		if (held === undefined) return

		try {
			const res = fromBinary(v.method.output, held) as Message
			v.refs = this.absorb(v.method.output, res)
			v.res = res
		} catch {
			// Bytes that no longer decode, which is a response message whose
			// shape moved under a mirror the entity stamp did not cover. Drop
			// it and wait for the read, which is where this would have ended up
			// anyway.
			this.store.setBlob(v.key, undefined)

			return
		}

		this.settle(v, 'ok', undefined)
	}

	/** settle moves an entry on and tells whoever is drawing it. */
	private settle(v: Live, state: State, error: unknown): void {
		this.resubscribe(v)

		// Kept for the next time this page is opened, and kept only when there
		// is something to keep: an error is not an answer, and a query that
		// failed should ask again rather than draw what it managed last week.
		if (state === 'ok' && v.res !== undefined) {
			this.store.setBlob(v.key, toBinary(v.method.output, v.res))
		}

		v.entry = {
			key: v.key,
			state,
			error,
			rev: v.entry.rev + 1,
			get data() {
				return undefined
			},
		}

		// Defined as a getter over the live entry so that reading it re-reads
		// the store, and cached on `rev` inside.
		Object.defineProperty(v.entry, 'data', { get: () => this.materialize(v), enumerable: true })

		for (const cb of [...v.listeners]) cb()
	}

	/**
	 * resubscribe points this entry at the rows it now names.
	 *
	 * A row leaving the answer stops mattering to it, which is why the whole
	 * subscription is replaced rather than added to: an entry that kept every
	 * row it ever saw would redraw for rows it no longer shows.
	 */
	private resubscribe(v: Live): void {
		v.off?.()

		const keys: Key[] = v.refs.map((r) => this.store.rowKey(r.typeName, r.id))
		v.off = this.store.subscribe(keys, () => {
			v.entry = { ...v.entry, rev: v.entry.rev + 1 }
			Object.defineProperty(v.entry, 'data', { get: () => this.materialize(v), enumerable: true })

			for (const cb of [...v.listeners]) cb()
		})
	}

	/**
	 * watch opens the sibling `Watch` for as long as anybody is asking this.
	 *
	 * The sibling is found rather than named: a service that answers a `List`
	 * over filters answers a `Watch` over the same filters, so the request is
	 * the query's own. That is a payday shape rather than a general one, and
	 * this is payday's package.
	 */
	private watch(v: Live): void {
		if (v.stop !== undefined) return

		const m = siblingWatch(v.method)
		if (m === undefined) return
		if (v.opts.watch === false) return

		// `[]` counts as no filters, and it is the shape that actually
		// arrives: protobuf-es materializes the repeated field, so a
		// filterless list carries `filters: []` and never leaves it out. The
		// server refuses the watch either way -- a watch says which rows it
		// is about, and one that says nothing is the whole table, for as long
		// as it is open -- and the refusal would land in the reopen path
		// below, which swallows it: a stream spent, the one retry spent
		// re-reading, and a screen that is quietly not live anyway. A page
		// that wants other people's writes live names a filter; `watch: true`
		// still insists, for a server whose `Watch` takes the whole table.
		const filters = (v.input as unknown as Record<string, unknown>)['filters']
		const filterless = filters === undefined || (Array.isArray(filters) && filters.length === 0)
		if (filterless && v.opts.watch !== true) return

		const stop = new AbortController()
		v.stop = stop

		void (async () => {
			try {
				const client = createClient(m.parent, this.transport) as unknown as Record<string, unknown>
				const f = client[m.localName] as (
					v: Message,
					o: { signal: AbortSignal },
				) => AsyncIterable<Message>

				// **Not** `skipSnapshot`. The list answered a moment ago and the
				// stream is established a moment after that, and anything that
				// changed in between is not replayed -- a skipped snapshot
				// loses it, and the store then holds a stale row until the next
				// write happens to correct it. Which is what `skip_snapshot`
				// says in the schema, and what it is off by default for.
				//
				// The snapshot re-sends rows the list just sent. That costs a
				// message and nothing else: the store reconciles by version, so
				// an answer it already has changes nothing and tells nobody.
				const req = create(m.input, { filters } as never)
				for await (const res of f(req, { signal: stop.signal })) {
					const items = (res as unknown as Record<string, unknown>)['items'] as
						| { id: Uint8Array; value?: Message }[]
						| undefined
					if (items === undefined) continue

					this.store.apply(entityOf(m), items)
				}
			} catch {
				// Fall through to the same place a stream that simply ended
				// does.
			}

			// The stream is over, and if nobody asked for that then this query
			// has quietly stopped being current -- which is the one failure the
			// whole layer exists to prevent. So: open it again, once, by
			// running the query again. That re-reads and re-establishes, and
			// the snapshot closes whatever was missed while it was down.
			//
			// **Once.** A stream that fails the same way twice is a server or a
			// network saying no, and a client that reopened forever would be a
			// page hammering it. What is left then is a store that stops
			// changing, which is visible to a person looking at it -- and the
			// next mount asks again.
			if (stop.signal.aborted || v.retried) return

			v.retried = true
			v.stop = undefined
			void this.run(v)
		})()
	}

	/**
	 * revalidate reads again every drawn query whose set the write may have
	 * changed.
	 *
	 * "May have" is the whole of it, and it is deliberately coarse: a row
	 * created belongs to some lists over its entity, a row erased leaves some,
	 * and a row edited can cross a filter it was inside -- and the only thing
	 * that knows which is the server. So the test is on the **entity**, and it
	 * is answered from the method's output type rather than from the rows an
	 * answer happened to name, because a list that came back empty named none
	 * and is exactly the one a create should fill.
	 *
	 * Only lists. A `Get` over a ref has no membership to change: if its row
	 * moved, the store already told everything drawing it, and reading again
	 * would be a call whose answer is the row that is already on screen.
	 */
	private revalidate(touched: ReadonlySet<string>): void {
		if (touched.size === 0) return

		for (const v of [...this.live.values()]) {
			// A query at rest re-reads when somebody draws it again; see
			// [Queries.wake]. Reading it now is a call for a screen that is not
			// there, and a page that has been open a while has more of those
			// than it has visible ones.
			if (v.idle) continue

			let hit = false
			for (const t of this.setsOf(v.method)) {
				if (!touched.has(t)) continue

				hit = true
				break
			}
			if (!hit) continue

			void this.run(v)
		}
	}

	/**
	 * setsOf is the entities a method answers with a **set** of.
	 *
	 * Read off the output descriptor, so it is a property of the method and is
	 * worked out once: `RobotListResponse` carries `repeated Robot items`, and
	 * a `Get` answering with one `Robot` carries no set at all.
	 */
	private setsOf(method: DescMethod): ReadonlySet<string> {
		let got = this.sets.get(method)
		if (got !== undefined) return got

		const out = new Set<string>()
		const seen = new Set<string>()

		const walk = (d: DescMessage, inList: boolean): void => {
			const at = `${d.typeName}:${inList}`
			if (seen.has(at)) return
			seen.add(at)

			if (this.entities.has(d.typeName)) {
				if (inList) out.add(d.typeName)

				return
			}

			for (const f of d.fields) {
				const to = messageOf(f)
				if (to === undefined) continue

				walk(to, inList || f.fieldKind === 'list')
			}
		}

		walk(method.output, false)
		this.sets.set(method, out)
		got = out

		return got
	}

	/**
	 * erased is the row a removal names, and what entity it is of.
	 *
	 * By name, the way `Watch` is found: payday generates `Erase` on every
	 * entity service, taking that entity's ref and answering with whether this
	 * call erased -- naming no row. This is payday's package and that is
	 * payday's shape.
	 *
	 * The entity is read from a sibling -- `Add` and `Get` answer with it -- so
	 * that a removal whose ref this cannot resolve still says *which* lists
	 * moved.
	 */
	private erased(method: DescMethod, input: Message): { typeName: string; id?: Uint8Array } | undefined {
		if (method.name !== 'Erase') return undefined

		let typeName: string | undefined
		for (const m of method.parent.methods) {
			if (!this.entities.has(m.output.typeName)) continue

			typeName = m.output.typeName
			break
		}
		if (typeName === undefined) return undefined

		// `oneof key { bytes id = 1; ... }` -- by identifier or by slug, and
		// only the first names a row this store holds.
		const ref = input as unknown as { key?: { case?: string; value?: unknown } }
		const id = ref.key?.case === 'id' ? ref.key.value : undefined

		return id instanceof Uint8Array ? { typeName, id } : { typeName }
	}

	/**
	 * rest is what a query does when nobody is drawing it any more.
	 *
	 * The **answer stays**. What stops is the stream and the row subscription:
	 * a page that navigated away should not go on holding a `Watch` open, and
	 * nothing needs to be told about a row nobody is showing. Coming back is
	 * then instant -- the cached answer, drawn at once, with a fresh call
	 * behind it.
	 *
	 * Dropping the entry instead is the eager version, and it makes going back
	 * a spinner for something the page had a moment ago. Keeping it is what
	 * "cached" means, and [Queries.forget] is how somebody says they have a
	 * reason to believe it moved.
	 */
	private rest(v: Live): void {
		v.off?.()
		v.off = undefined
		v.stop?.abort()
		v.stop = undefined
		v.idle = true
	}

	/**
	 * wake is somebody drawing a rested query again: show what is held, and
	 * find out whether it is still true.
	 */
	private wake(v: Live): void {
		if (!v.idle) return

		v.idle = false
		this.resubscribe(v)
		void this.run(v)
	}
}

/**
 * keyOf is what makes two asks the same ask.
 *
 * The request is walked in field order rather than `JSON.stringify`d, because
 * two requests built in a different order are the same request and a page that
 * built one of them in a loop would otherwise make a second call for it.
 */
export function keyOf(method: DescMethod, input: Message): string {
	return `${method.parent.typeName}/${method.name}#${stable(input)}`
}

function stable(v: unknown): string {
	if (v === undefined || v === null) return 'null'
	if (v instanceof Uint8Array) return `b${Array.from(v, (b) => b.toString(16).padStart(2, '0')).join('')}`
	if (Array.isArray(v)) return `[${v.map(stable).join(',')}]`
	if (typeof v === 'object') {
		const es = Object.entries(v as Record<string, unknown>)
			.filter(([k]) => k !== '$typeName' && k !== '$unknown')
			.sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))

		return `{${es.map(([k, w]) => `${k}:${stable(w)}`).join(',')}}`
	}

	return typeof v === 'bigint' ? `${v}n` : JSON.stringify(v)
}

/**
 * messageOf is the message a field holds, singly or in a list, or nothing when
 * it holds neither.
 *
 * A repeated message is `fieldKind: "list"` with `listKind: "message"` rather
 * than a message field that repeats, which is the shape that made the first
 * version of this walk skip every list -- so `items` was never looked in, and a
 * `List` normalized nothing at all.
 */
function messageOf(f: DescField): DescMessage | undefined {
	if (f.fieldKind === 'message') return f.message
	if (f.fieldKind === 'list' && f.listKind === 'message') return f.message

	return undefined
}

/** siblingWatch is the streaming method beside a query, when there is one. */
function siblingWatch(method: DescMethod): DescMethod | undefined {
	for (const m of method.parent.methods) {
		if (m.methodKind !== 'server_streaming') continue
		if (m.name !== 'Watch') continue

		return m
	}

	return undefined
}

/** entityOf is what a `Watch` on this method is about. */
function entityOf(watch: DescMethod): string {
	const items = watch.output.fields.find((f) => f.localName === 'items')
	const item = items === undefined ? undefined : messageOf(items)
	if (item === undefined) throw new Error(`query: ${watch.name} answers with no items`)

	const value = item.fields.find((f) => f.localName === 'value')
	const of = value === undefined ? undefined : messageOf(value)
	if (of === undefined) throw new Error(`query: ${watch.name} carries no value`)

	return of.typeName
}
