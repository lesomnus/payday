/**
 * The local store, which is layer 3 of the client and the last layer payday
 * takes a position on.
 *
 * It is opened over the declarations `pd gen --ts` writes, and everything it
 * does is written once rather than per entity -- see [EntityDesc] for why that
 * is the whole design.
 *
 *   import { Store } from '@lesomnus/payday/store'
 *   import { entities, Robot } from './gen/entities.js'
 *
 *   const store = Store.open(entities, { name: 'acme', identity: me })
 *   await store.put(Robot.typeName, await client.robot.get({ ref }))
 *
 * @module
 */

export { Store } from './store.js'
export type { Opts } from './store.js'
export { key, bytes } from './desc.js'
export type { EntityDesc, RefDesc, Row } from './desc.js'
export { flatten, newer } from './flat.js'
