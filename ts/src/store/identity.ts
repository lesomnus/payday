/**
 * What a store is opened for.
 *
 * @module
 */

import { digest } from './digest.js'

/**
 * identityOf is a credential as a store name: a digest of it, and never it.
 *
 * # Why the credential and not the user
 *
 * A store holds what its caller can see, and what a caller can see is the actor
 * **and the scope**. In payday those are separate: the same holder with a
 * credential narrowed to one tenant sees that tenant, and with a whole one sees
 * every tenant they are allowed. Keyed on the person, those two share a store,
 * and the narrowed session draws the wide session's rows -- nothing leaked,
 * since the server sent all of it to that person, and the screen is still
 * wrong.
 *
 * Keyed on the credential there is nothing to get right. A narrower one is a
 * different string, so it is a different store, so it starts empty and fills
 * with what it can actually see.
 *
 * # Why a digest
 *
 * The name is not a secret and the credential is. A store name reaches places a
 * token should not: it is in `IndexedDB` if anything persists, in a devtools
 * pane, and in whatever a bug report screenshots. A digest names the same
 * credential without carrying it.
 *
 * Truncated to sixteen hex characters, which is sixty-four bits. It is a name
 * and not a defence: two credentials colliding here would share a store, and
 * sixty-four bits of a SHA-256 is far past the number of credentials one
 * browser profile will ever hold.
 *
 * # It is not a session
 *
 * Nothing here expires anything or notices that a token was revoked. What it
 * answers is "is this the same credential as last time", and a token that was
 * rotated is a different one -- so a rotation starts an empty store, which
 * costs a refill of something the server has anyway.
 */
export async function identityOf(credential: string): Promise<string> {
	return digest(credential)
}
