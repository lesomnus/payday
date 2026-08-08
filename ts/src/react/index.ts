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

import { createContext, createElement, useContext, useMemo, useSyncExternalStore, type ReactNode } from 'react'

import type { DescMessage, DescMethodUnary, Message, MessageInitShape, MessageShape } from '@bufbuild/protobuf'

import type { Entry, Queries, QueryOpts } from '../query/index.js'
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
