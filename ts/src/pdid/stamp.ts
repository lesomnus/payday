/**
 * The clock an identifier is made from: a millisecond, and which one inside it
 * this is.
 *
 * The Go half borrows this from `uuid.NewV7`, which keeps a twelve-bit sequence
 * so that identifiers made inside one millisecond come out in the order they
 * were made. There is no such thing to borrow here, and dropping the sequence
 * would be dropping the ordering -- everything made in the same millisecond
 * would sort by its random tail, which is no order at all. So it is kept here,
 * and it is the reason {@link newId} writes the version into a nibble rather
 * than a byte: the byte holds four of these twelve bits.
 */

/** The sequence is twelve bits, which is what is left of bytes 6 and 7. */
const seqMax = 0x0fff;

/**
 * The last millisecond handed out, which is not always the one the clock says.
 *
 * A clock that steps backwards -- an NTP correction, a laptop waking up -- would
 * otherwise hand out an identifier that sorts before one already written, and a
 * consumer reading in identifier order would read the same row twice or not at
 * all. Time here only ever goes forward.
 */
let last = -1;
let seq = 0;

export type Stamp = {
	/** Milliseconds since the epoch, as the first six bytes hold it. */
	ms: number;
	/** Which identifier this is inside that millisecond. */
	seq: number;
};

/**
 * Answers with the millisecond and the sequence the next identifier is to
 * carry, so that two answers are never equal and the later one is never the
 * smaller.
 *
 * Exhausting the sequence borrows a millisecond from the future rather than
 * waiting for one or starting over. Starting over would make two identifiers
 * that sort the wrong way round, and waiting would make a call that blocks
 * inside what is otherwise arithmetic; borrowing costs a timestamp that is a
 * millisecond early, which nothing here reads as a time.
 */
export function stamp(): Stamp {
	const now = Date.now();
	if (now > last) {
		last = now;
		seq = 0;
	} else if (seq < seqMax) {
		seq += 1;
	} else {
		last += 1;
		seq = 0;
	}

	return { ms: last, seq };
}

/**
 * Forgets what has been handed out, for the test that has to watch the counter
 * from a known start. Nothing else has any business calling it -- it is how the
 * ordering above is broken -- so it is not exported from the package.
 */
export function forget(): void {
	last = -1;
	seq = 0;
}
