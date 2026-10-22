/**
 * The page `pd new` writes, run: CI's `new` job copies this into the app it
 * made, beside `src/`, and runs it against that app's own `src/store.ts` on
 * fake-indexeddb. The build before it says the page compiles; this says what
 * its store leaves on a browser somebody else used before.
 */

import 'fake-indexeddb/auto'

import type { Transport } from '@connectrpc/connect'
import { expect, it } from 'vitest'

import { open } from './src/store.js'

/** Never called: opening a store asks nothing of a server. */
const transport = {} as Transport

/** names is every database this origin has. */
async function names(): Promise<string[]> {
	return (await indexedDB.databases()).map((v) => v.name ?? '')
}

/** until waits for something to come true, or says it never did. */
async function until(f: () => Promise<boolean>, ms = 3_000): Promise<void> {
	const end = Date.now() + ms
	while (!(await f())) {
		if (Date.now() > end) throw new Error('it never happened')
		await new Promise((ok) => setTimeout(ok, 25))
	}
}

it("deletes every other caller's mirror once a caller's store is open", async () => {
	const before = await names()

	const theirs = await open(transport, 'theirs')
	await theirs.store.close()
	const left = (await names()).filter((v) => !before.includes(v))
	expect(left, 'opening a store made no mirror').toHaveLength(1)

	// Somebody else, on the same browser. Not waited on by `open`, so waited
	// on here.
	const mine = await open(transport, 'mine')
	await until(async () => !(await names()).includes(left[0] ?? ''))

	const now = (await names()).filter((v) => !before.includes(v))
	expect(now, "only this caller's mirror is left").toHaveLength(1)
	expect(now).not.toEqual(left)

	await mine.store.close()
})
