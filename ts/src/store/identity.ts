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
 * costs a refill of something the server has anyway, and leaves the last
 * one's mirror for `deleteOtherDisks` to take away.
 *
 * # A page signed in by cookie
 *
 * It cannot call this, because it cannot read its credential: a session cookie
 * is HttpOnly, and that is the whole point of one. Keying the store on the person
 * instead -- asking who they are and using the answer -- is the case above that
 * goes wrong, made worse by a mirror on disk that keeps the wide session's rows
 * for the narrowed one to draw after a reload.
 *
 * So the server says the name. `GET /session`, which `auth/authsession` serves
 * beside the sign-in, answers `{ identity, id, tenantId }`, and `identity` is a
 * digest of what the session lets its caller see -- who it names, in which
 * tenant, narrowed how -- worked out where the session can be read
 * (`authsession.IdentityOf`). A page passes it where this answer would have
 * gone. The other two are who the session names, which is what the page's
 * first question about the person is asked with. `pd new` writes both pages:
 * `main.tsx`, which holds a credential, and `session.tsx`, which holds a
 * cookie.
 *
 * It is not a digest of the session's key, and that difference is the one
 * worth having. A session ends by itself -- it idles out, or the next sign-in
 * replaces its cookie -- and a store named by its key is a mirror nobody opens
 * again, holding the person's rows and the writes they made and never sent.
 * Named by what it sees, the person's next sign-in opens the same store and
 * sends what was waiting, as them; and a narrower session is still a name of
 * its own, which is the argument above kept.
 */
export async function identityOf(credential: string): Promise<string> {
	return digest(credential)
}
