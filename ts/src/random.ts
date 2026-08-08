/**
 * Bytes nobody can guess, from wherever this is running.
 *
 * There is no falling back to `Math.random`. A runtime with no random source is
 * a runtime that cannot name a row, and quietly naming one guessably instead
 * answers a question nobody asked -- the Go half stops the process for the same
 * reason. What it means in practice is a page served over plain HTTP, where the
 * browser withholds the interface; the fix is the page, not this.
 */
export function random(n: number): Uint8Array {
	const source = (globalThis as { crypto?: { getRandomValues?: (v: Uint8Array) => Uint8Array } })
		.crypto;
	if (source?.getRandomValues === undefined) {
		throw new Error(
			"payday: no crypto.getRandomValues, so nothing here can name a row; a page has one only in a secure context",
		);
	}

	return source.getRandomValues(new Uint8Array(n));
}
