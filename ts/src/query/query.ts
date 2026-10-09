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
 * # And the writes that wait
 *
 * A write made with no connection is kept rather than failed -- see
 * [Queries.send] -- and sent in the order it was made once there is somebody
 * to send it to. It is drawn as what it is, a write waiting, and **not** as the
 * row it will make: guessing the row is the optimistic update this layer does
 * not have, and the reason is the same one. The answer is the row.
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
import { Code, ConnectError, createClient, type Transport } from '@connectrpc/connect'

import { random } from '../random.js'
import { WRITES, key, type EntityDesc, type Key, type Queued, type Refusal, type Store } from '../store/index.js'

// Declared rather than putting `DOM` in `lib`: these two are in a page, a
// worker and Node, and they are all this module needs from outside the
// language. See `store/identity.ts` for the same reasoning.
type AbortSignal = { readonly aborted: boolean }
type Aborter = { readonly signal: AbortSignal; abort(): void }
declare const AbortController: { new (): Aborter }

// Web Locks, which is what makes "one page sends a store's writes" true across
// tabs. In a page, a worker and Node; absent is a realm of one, see [exclusive].
declare const navigator: { readonly locks?: Locks } | undefined
type Locks = { request<T>(name: string, f: () => Promise<T>): Promise<T> }

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

/**
 * Write is one write [Queries.send] is holding: waiting to be sent, or refused
 * when it was.
 */
export interface Write {
	/** What it is called here, from when it was made until it is let go. */
	readonly id: string

	/** `<service>/<method>`, which is what it was made with. */
	readonly name: string

	/**
	 * The method, and undefined when this page knows no service by that name --
	 * a write kept by a deploy that had one. It waits rather than being dropped;
	 * see [QueriesOpts.services].
	 */
	readonly method: DescMethod | undefined

	/** The request, read back, and undefined wherever `method` is. */
	readonly input: Message | undefined

	readonly at: Date

	readonly state: 'waiting' | 'refused'

	/**
	 * What the server refused it with: the `ConnectError` a call would have
	 * thrown, details and all, so whatever reads one -- `pderr` among them --
	 * reads this the same way.
	 */
	readonly error: ConnectError | undefined
}

/** QueriesOpts is what the query layer may be told beyond its store. */
export interface QueriesOpts {
	/**
	 * Services beside the entities', for a write [Queries.send] kept to one of
	 * them.
	 *
	 * Every entity's service is known already, an app's own RPCs on it
	 * included. A write to some other service is kept by name, and reading it
	 * back after a reload needs the descriptor -- which a page that has not sent
	 * through that service yet has only if it was told here.
	 */
	readonly services?: readonly DescService[]
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

	/** Every service a kept write may name, by full name; see [Queries.methodOf]. */
	private readonly services = new Map<string, DescService>()

	/** The drain that is running, and the one waiting to; see [Queries.flush]. */
	private flushing: Promise<void> = Promise.resolve()
	private next: Promise<void> | undefined

	/** What [Queries.writes] last answered, and what it was answered from. */
	private seen: { of: readonly Queued[]; v: readonly Write[] } | undefined

	constructor(store: Store, transport: Transport, entities: readonly EntityDesc[], opts: QueriesOpts = {}) {
		this.store = store
		this.transport = transport
		this.entities = new Map(entities.map((v) => [v.typeName, v]))

		for (const v of entities) if (v.service !== undefined) this.services.set(v.service.typeName, v.service)
		for (const v of opts.services ?? []) this.services.set(v.typeName, v)
	}

	/**
	 * raw is the transport these queries are asked over, for a caller that
	 * wants the answer and **not** the bookkeeping.
	 *
	 * Everything else here puts what it read into the store, which is the
	 * point: a page asks a question and every screen holding one of those rows
	 * is right afterwards. That is exactly wrong for a caller whose subject is
	 * the store -- something showing what the server says beside what this side
	 * believes would make the two agree by looking at them.
	 *
	 * So it hands over the transport rather than a method to call: what to do
	 * with it is `createClient`, and nothing here is in the way.
	 */
	get raw(): Transport {
		return this.transport
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

		const touched = this.landed(method as DescMethod, req, res)
		if (opts.revalidate !== false) this.revalidate(touched)
		this.resume()

		return res as MessageShape<O>
	}

	/**
	 * send is [Queries.call] for a write that may have to wait for a
	 * connection, and it answers once the write is **kept** -- not once it is
	 * made.
	 *
	 *   const id = pdid.newId(RobotDomain).bytes
	 *   await queries.send(RobotService.method.add, { id, tenant, alias })
	 *
	 * The write goes to the store's mirror first, so a page closed a moment
	 * later still has it, and is then tried. Answered, it lands the way a
	 * call's answer does. Not answered -- no network, or a server that is not
	 * there -- it waits, and the ones made after it wait behind it, so they are
	 * sent in the order they were made. Refused, it stays in [Queries.writes]
	 * with the error the call would have thrown, until somebody lets it go.
	 *
	 * # The same write, however many times it is sent
	 *
	 * An answer can be lost on the way back, and the write is then sent again.
	 * So what is worth queueing is a write that is the same write twice, and an
	 * `Add` that names its row is the case this knows: the identifier is minted
	 * here, the second `Add` is refused `AlreadyExists`, and the row is read by
	 * that identifier instead -- which is the answer the first one never got. An
	 * `Erase` is the same write by nature. A `Patch` that names the version it
	 * read is refused the second time, which is right about the row and wrong
	 * about the write; and an app's own RPC is the same write twice when its
	 * request carries an identifier the server knows it by.
	 *
	 * # When it is sent
	 *
	 * Now, and then whenever anything answers: a read that came back is a
	 * connection that came back. A page that would like to try sooner -- on the
	 * browser's `online`, say -- calls [Queries.flush].
	 */
	async send<I extends DescMessage, O extends DescMessage>(
		method: DescMethodUnary<I, O>,
		input: MessageInitShape<I>,
	): Promise<Write> {
		const req = create(method.input, input)
		this.services.set(method.parent.typeName, method.parent)

		const v: Queued = {
			id: mint(),
			method: nameOf(method as DescMethod),
			input: toBinary(method.input, req),
			at: Date.now(),
		}
		await this.store.writes.add(v)

		void this.flush().catch(() => {})

		return this.writeOf(v)
	}

	/**
	 * writes is every write [Queries.send] is holding, in the order they were
	 * made, **now** -- the same array until one is kept, sent or refused.
	 */
	writes(): readonly Write[] {
		const of = this.store.writes.all()
		if (this.seen?.of === of) return this.seen.v

		const v = of.map((w) => this.writeOf(w))
		this.seen = { of, v }

		return v
	}

	/** subscribeWrites tells `cb` when [Queries.writes] would answer differently. */
	subscribeWrites(cb: () => void): () => void {
		return this.store.subscribe([WRITES], cb)
	}

	/**
	 * flush sends what is waiting, in order, and answers when it has sent what
	 * it can.
	 *
	 * One at a time per store, across every page of the origin where the
	 * platform has Web Locks: two tabs open on one store both hold its queue,
	 * and a write is an effect, so only one of them may be sending it. The one
	 * that holds the lock reads the queue from the mirror before it starts, so
	 * what another tab sent is not sent again and what another tab kept is
	 * sent.
	 *
	 * Rejects only when the mirror does. What the server says lands in
	 * [Queries.writes], not here.
	 */
	flush(): Promise<void> {
		if (this.next !== undefined) return this.next

		const next = this.flushing.then(() => {
			this.next = undefined

			return exclusive(this.lockName(), () => this.drain())
		})
		this.next = next
		this.flushing = next.catch(() => {})

		return next
	}

	/**
	 * dismiss lets a write go: a refused one somebody has read, or a waiting
	 * one taken back before it was sent.
	 *
	 * Under the same lock as sending, so a write is either sent or let go and
	 * never both.
	 */
	dismiss(id: string): Promise<void> {
		return exclusive(this.lockName(), () => this.store.writes.drop(id))
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
			this.resume()
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

	/**
	 * landed is the answer to a write put where everything reading will see
	 * it, and answers with the entities whose lists it may have moved.
	 *
	 * A removal is the one write whose answer names no row -- see
	 * [Queries.call] -- so its subject is read from the request.
	 */
	private landed(method: DescMethod, req: Message, res: Message): Set<string> {
		const touched = new Set<string>()
		for (const r of this.absorb(method.output, res)) touched.add(r.typeName)

		const gone = this.erased(method, req)
		if (gone !== undefined) {
			// The identifier is there only when the row was named by one. A ref
			// by slug says which row to the server and not to this, and the
			// re-read after is what takes it off the screen.
			if (gone.id !== undefined) this.store.apply(gone.typeName, [{ id: gone.id }])
			touched.add(gone.typeName)
		}

		return touched
	}

	/**
	 * drain sends every write that is waiting, in order, and stops at the first
	 * the server did not answer -- the rest would not be answered either, and
	 * would arrive out of order if they were.
	 *
	 * A refused write does not stop it. The server judged that one, and the
	 * next is judged on its own; one that needed the refused one to have
	 * happened is refused for that, which is what it should say.
	 *
	 * The lists are read again **once**, when it stops, over every entity the
	 * writes touched: a replay of two hundred writes made in a basement is one
	 * re-read of each list on screen, not two hundred.
	 */
	private async drain(): Promise<void> {
		const q = this.store.writes

		// Another page on this store may have sent some of these, or kept more.
		await q.reload()

		const touched = new Set<string>()
		try {
			for (const w of q.all()) {
				if (w.refused !== undefined) continue

				// Let go since the loop began: dismissed, or the caller is
				// done -- and sending what a caller who has gone wrote, under
				// whatever credential the transport carries now, is the one
				// thing this must not do.
				if (!q.has(w.id)) continue

				// Left waiting rather than dropped: a later deploy, or a page
				// told about the service, can still send it.
				const method = this.methodOf(w.method)
				if (method === undefined) continue

				let req: Message
				try {
					req = fromBinary(method.input, w.input) as Message
				} catch (err) {
					await q.refuse(w.id, {
						code: Code.DataLoss,
						message: `the request no longer reads as a ${method.input.typeName}: ${String(err)}`,
						details: [],
					})
					continue
				}

				const got = await this.attempt(method, req)
				if (got === undefined) return
				if (got instanceof ConnectError) {
					await q.refuse(w.id, refusalOf(got))
					continue
				}

				// Let go before it lands, so that a page closed in between
				// sends it again -- which an `Add` with its own identifier
				// survives -- rather than keeping a write that happened.
				await q.drop(w.id)
				for (const t of this.landed(method, req, got)) touched.add(t)
			}
		} finally {
			this.revalidate(touched)
		}
	}

	/**
	 * attempt sends one write, and answers with the answer, with the refusal,
	 * or with nothing when nobody answered.
	 */
	private async attempt(method: DescMethod, req: Message): Promise<Message | ConnectError | undefined> {
		try {
			return (await this.invoke(method, req)) as Message
		} catch (err) {
			if (unanswered(err)) return undefined

			const e = ConnectError.from(err)
			if (e.code !== Code.AlreadyExists) return e

			const was = await this.already(method, req)
			if (was === null) return undefined

			return was ?? e
		}
	}

	/**
	 * already is the row an `Add` made the last time it was sent, when it was --
	 * undefined when it was not, and null when that could not be asked.
	 *
	 * `AlreadyExists` is the one refusal that can mean "this already
	 * happened", and it can as well mean a slug somebody else holds. So the row
	 * is read by the identifier this side minted: there, it is the answer the
	 * first send never got; not there, the refusal was about something else and
	 * stands.
	 */
	private async already(method: DescMethod, req: Message): Promise<Message | undefined | null> {
		if (method.name !== 'Add') return undefined

		const id = (req as unknown as { id?: unknown }).id
		if (!(id instanceof Uint8Array) || id.length === 0) return undefined

		const get = method.parent.methods.find((m) => m.name === 'Get' && m.methodKind === 'unary')
		if (get === undefined) return undefined

		try {
			const ref = create(get.input, { ref: { key: { case: 'id', value: id } } } as never) as Message

			return (await this.invoke(get, ref)) as Message
		} catch (err) {
			return unanswered(err) ? null : undefined
		}
	}

	/**
	 * resume sends what is waiting, once a call has shown there is somebody to
	 * send it to.
	 */
	private resume(): void {
		if (!this.store.writes.all().some((w) => w.refused === undefined)) return

		void this.flush().catch(() => {})
	}

	/** methodOf is the unary method a kept write names, if this page has it. */
	private methodOf(name: string): DescMethod | undefined {
		const at = name.lastIndexOf('/')
		if (at < 0) return undefined

		const m = this.services.get(name.slice(0, at))?.methods.find((v) => v.name === name.slice(at + 1))

		return m?.methodKind === 'unary' ? m : undefined
	}

	/** writeOf is a kept write as a page reads one. */
	private writeOf(v: Queued): Write {
		const method = this.methodOf(v.method)

		let input: Message | undefined
		if (method !== undefined) {
			try {
				input = fromBinary(method.input, v.input) as Message
			} catch {
				input = undefined
			}
		}

		return {
			id: v.id,
			name: v.method,
			method,
			input,
			at: new Date(v.at),
			state: v.refused === undefined ? 'waiting' : 'refused',
			error: v.refused === undefined ? undefined : errorOf(v.refused),
		}
	}

	/** lockName is what one store's sending is serialized on, across pages. */
	private lockName(): string {
		return `payday/${this.store.name}/writes`
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

/**
 * unanswered is whether a failed call is one the server never answered --
 * the only kind worth sending again.
 *
 * `Unavailable` is a proxy or the transport saying so. `Canceled` and
 * `DeadlineExceeded` are this side giving up, which says nothing about whether
 * the server got it. `Unknown` with a cause is a `fetch` that failed, which is
 * how connect-web reports a page with no network: the `TypeError` wrapped in a
 * ConnectError with no code of its own. And something that is not a
 * ConnectError at all is a transport that broke before it could say anything.
 *
 * Anything else, the server said -- and saying it again changes nothing.
 */
function unanswered(err: unknown): boolean {
	if (!(err instanceof ConnectError)) return true

	switch (err.code) {
		case Code.Unavailable:
		case Code.Canceled:
		case Code.DeadlineExceeded:
			return true
		case Code.Unknown:
			return err.cause !== undefined && !(err.cause instanceof ConnectError)
		default:
			return false
	}
}

/** refusalOf is a refusal in the shape a mirror holds. */
function refusalOf(err: ConnectError): Refusal {
	return {
		code: err.code,
		message: err.rawMessage,
		details: err.details.map((d) => {
			if (!('desc' in d)) return { type: d.type, value: d.value }

			return { type: d.desc.typeName, value: toBinary(d.desc, create(d.desc, d.value)) }
		}),
	}
}

/** errorOf is a kept refusal as the error a call would have thrown. */
function errorOf(v: Refusal): ConnectError {
	const err = new ConnectError(v.message, v.code)
	err.details = v.details.map((d) => ({ type: d.type, value: d.value }))

	return err
}

/** nameOf is how a kept write names its method. */
function nameOf(method: DescMethod): string {
	return `${method.parent.typeName}/${method.name}`
}

/**
 * mint is a kept write's name, which sorts in the order writes were made: the
 * time, then a count for two in one millisecond, then enough randomness that
 * two pages minting at once do not collide.
 */
function mint(): string {
	const at = Date.now()
	count = at === last ? count + 1 : 0
	last = at

	const tail = Array.from(random(6), (b) => b.toString(16).padStart(2, '0')).join('')

	return `${at.toString(36).padStart(9, '0')}-${count.toString(36).padStart(4, '0')}-${tail}`
}

let last = 0
let count = 0

/** One holder at a time per name, within this realm, where there are no Web Locks. */
const held = new Map<string, Promise<void>>()

/**
 * exclusive runs `f` holding `name`: across every page of the origin where
 * the platform has Web Locks, and across everything in this realm where it
 * does not -- which is a realm with no other page to be exclusive of.
 */
async function exclusive<T>(name: string, f: () => Promise<T>): Promise<T> {
	const locks = typeof navigator === 'undefined' ? undefined : navigator.locks
	if (locks !== undefined) return locks.request(name, f)

	const before = held.get(name) ?? Promise.resolve()
	const run = before.then(f, f)
	const tail = run.then(
		() => {},
		() => {},
	)
	held.set(name, tail)

	try {
		return await run
	} finally {
		if (held.get(name) === tail) held.delete(name)
	}
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
