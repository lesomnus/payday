/**
 * A short name for a long string.
 *
 * @module
 */

// Declared here rather than by putting `DOM` in `lib`, which would make every
// browser global compile in a package that runs in a worker, in Node and in a
// page. These two are in all three, and they are the whole of what this module
// needs from outside the language.
declare const crypto: {
	readonly subtle: { digest(algorithm: string, data: Uint8Array): Promise<ArrayBuffer> }
}

declare const TextEncoder: { new (): { encode(v: string): Uint8Array } }

/**
 * digest is a string as sixteen hex characters.
 *
 * Sixty-four bits of a SHA-256, and a **name** rather than a defence: what it
 * is asked is "is this the same string as last time", and two of them colliding
 * would mean two things sharing a name. Sixty-four bits is far past the number
 * of names one browser profile will ever hold.
 */
export async function digest(v: string): Promise<string> {
	const out = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(v))

	let s = ''
	for (const b of new Uint8Array(out).slice(0, 8)) {
		s += b.toString(16).padStart(2, '0')
	}

	return s
}
