/**
 * React over a payday store, which is deliberately this small.
 *
 * §10.4 of the design draws the line at the store rather than at a hook: what
 * is worth enforcing is normalizing, ordering two answers about one row and
 * applying a `Watch`, none of which is taste. A view framework is taste, and a
 * framework payday *required* would be the thing that makes full-stack
 * frameworks fail.
 *
 * So this is a binding and not a layer. It is `useSyncExternalStore` over
 * [Queries.subscribe], and the same file for Vue or Svelte is the same length
 * -- which is the point: an app that wants neither throws away thirty lines.
 *
 * `react` is an **optional** peer dependency. Importing this entry is what
 * makes it required, and nothing else in the package reaches for it.
 *
 * @module
 */

import {
	createContext,
	createElement,
	useCallback,
	useContext,
	useEffect,
	useMemo,
	useRef,
	useState,
	useSyncExternalStore,
	type ReactNode,
} from 'react'

import type { DescMessage, DescMethodUnary, Message, MessageInitShape, MessageShape } from '@bufbuild/protobuf'

import type { CallOpts, Entry, Queries, QueryOpts } from '../query/index.js'
import type { Store } from '../store/index.js'

/** App is what a page reads through. */
export interface App {
	readonly store: Store
	readonly queries: Queries
}

const Ctx = createContext<App | undefined>(undefined)

/** Provider puts one app's store and queries in reach of the tree under it. */
export function Provider(props: { app: App; children?: ReactNode }): ReactNode {
	return createElement(Ctx.Provider, { value: props.app }, props.children)
}

/** useApp is the store and queries this tree was given. */
export function useApp(): App {
	const v = useContext(Ctx)
	if (v === undefined) {
		throw new Error('payday: no <Provider app={...}> above this component')
	}

	return v
}

/**
 * useQuery reads one RPC and re-renders when what it answered with changes.
 *
 *   const { data } = useQuery(RobotService.method.list, { filters: [] })
 *
 * The dependency is not declared anywhere: calling this **is** the declaration.
 * The rows the answer carried are what this component is drawing, so a change
 * to any of them -- from this query, from another one showing the same row,
 * from a `Watch`, from a write -- re-renders it, and every other place that row
 * appears at the same time.
 *
 * Two components asking the same thing share one call, one entry and one
 * stream; the last one to unmount is what closes them.
 */
export function useQuery<I extends DescMessage, O extends DescMessage>(
	method: DescMethodUnary<I, O>,
	input: MessageInitShape<I>,
	opts?: QueryOpts,
): Entry<MessageShape<O>> {
	const { queries } = useApp()

	// The entry is created on the first read and is the same object until it
	// changes, which is what `useSyncExternalStore` requires of a snapshot: one
	// that is rebuilt every call renders forever.
	const entry = queries.get(method, input, opts)

	const [subscribe, snapshot] = useMemo(
		() => [(cb: () => void) => queries.subscribe(entry.key, cb), () => queries.get(method, input, opts)],
		// The key is the method and the request, so this is "the same question"
		// rather than "the same object" -- a request rebuilt on every render is
		// still one query.
		// eslint-disable-next-line react-hooks/exhaustive-deps
		[queries, entry.key],
	)

	return useSyncExternalStore(subscribe, snapshot, snapshot)
}

/** Call is a write, and where it has got to. */
export interface Call<I extends DescMessage, O extends DescMessage> {
	/**
	 * Make the write.
	 *
	 * Answers with what the server answered, and **throws what it threw** --
	 * so an ordinary `try`/`catch` around an await works, and a caller who
	 * would rather render the failure reads `error` instead. A caller who does
	 * neither leaves a rejected promise, which is a caller ignoring a failed
	 * write.
	 */
	readonly call: (input: MessageInitShape<I>, opts?: CallOpts) => Promise<MessageShape<O>>

	readonly state: 'idle' | 'pending' | 'ok' | 'error'
	readonly data: MessageShape<O> | undefined
	readonly error: unknown
}

/**
 * useCall is a write that puts what it answered with where everything reading
 * will see it.
 *
 *   const add = useCall(RobotService.method.add)
 *   await add.call({ alias, tenant })
 *
 * There is no invalidation to declare and no list to update. The row the write
 * answered with goes into the store, so every place already showing it is
 * right at once; the lists it may now belong to are read again. See
 * [Queries.call] for why the second one is a round trip and not a local
 * insertion.
 *
 * Unlike [useQuery] this is **not** shared: two components calling the same
 * write are two writes, which is what a person clicking two buttons means.
 * What they share is where the answers land.
 */
export function useCall<I extends DescMessage, O extends DescMessage>(method: DescMethodUnary<I, O>): Call<I, O> {
	const { queries } = useApp()

	const [at, setAt] = useState<{
		state: 'idle' | 'pending' | 'ok' | 'error'
		data: MessageShape<O> | undefined
		error: unknown
	}>({ state: 'idle', data: undefined, error: undefined })

	// Which write is the current one, so that two in flight settle in the order
	// they were made rather than the order they answered -- and so that a
	// component gone from the screen is not told about either.
	const gen = useRef(0)
	const live = useRef(true)
	useEffect(() => {
		live.current = true

		return () => {
			live.current = false
		}
	}, [])

	const call = useCallback(
		async (input: MessageInitShape<I>, opts?: CallOpts): Promise<MessageShape<O>> => {
			const n = ++gen.current
			setAt({ state: 'pending', data: undefined, error: undefined })

			try {
				const data = await queries.call(method, input, opts)
				if (live.current && n === gen.current) setAt({ state: 'ok', data, error: undefined })

				return data
			} catch (error) {
				if (live.current && n === gen.current) setAt({ state: 'error', data: undefined, error })

				throw error
			}
		},
		[queries, method],
	)

	return { call, state: at.state, data: at.data, error: at.error }
}

/**
 * useRow reads one row straight from the store.
 *
 * For a neighbour: a listed row names its tenant and does not carry it, so a
 * component showing the tenant's name asks for it here. It answers with what is
 * held now and re-renders when that changes -- and if nothing has it, with
 * undefined, which is the caller's cue to ask the server for it.
 */
export function useRow<T extends Message>(typeName: string, id: Uint8Array | string | undefined): T | undefined {
	const { store } = useApp()

	const [subscribe, snapshot] = useMemo(() => {
		if (id === undefined) return [() => () => {}, () => undefined] as const

		const key = store.rowKey(typeName, id)

		return [
			(cb: () => void) => store.subscribe([key], cb),
			() => store.row(typeName, id),
		] as const
	}, [store, typeName, id === undefined ? undefined : store.rowKey(typeName, id)])

	const row = useSyncExternalStore(subscribe, snapshot, snapshot)

	return useMemo(() => (row === undefined ? undefined : store.message<T>(typeName, row)), [store, typeName, row])
}
