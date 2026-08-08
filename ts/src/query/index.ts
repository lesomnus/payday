/**
 * Reads that keep themselves up to date; see [Queries].
 *
 *   const queries = new Queries(store, transport, entities)
 *   const entry = queries.get(RobotService.method.list, { filters: [] })
 *
 * A framework binding is a few lines over this -- `subscribe` and a snapshot --
 * and `@lesomnus/payday/react` is that for React.
 *
 * @module
 */

export { Queries, keyOf } from './query.js'
export type { CallOpts, Entry, QueryOpts, State } from './query.js'
