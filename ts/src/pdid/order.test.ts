import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { newId } from "./id.js";
import { forget, stamp } from "./stamp.js";

// This is the reason the version is written into a nibble rather than a byte.
//
// The twelve-bit sequence in bytes 6 and 7 is what makes identifiers made
// inside one millisecond come out in the order they were made. Writing the
// version as a whole byte -- which is the obvious way to do it, and what an
// earlier implementation of this idea did -- takes the top four bits of that
// sequence with it, and the ordering holds for 256 per millisecond instead of
// 4096.
//
// The naive layout is built here beside the real one so that this says what is
// being avoided rather than only that it was avoided.

// More than 256 so that the naive layout has to wrap, and comfortably under
// 4096 so that the real one does not.
const n = 2000;

/** Byte order, which is the order a database and a local store sort them in. */
function ordered(vs: Uint8Array[]): boolean {
	for (let i = 1; i < vs.length; i++) {
		if (compare(vs[i - 1], vs[i]) >= 0) {
			return false;
		}
	}

	return true;
}

function compare(a: Uint8Array, b: Uint8Array): number {
	for (let i = 0; i < a.length; i++) {
		if (a[i] !== b[i]) {
			return a[i] - b[i];
		}
	}

	return 0;
}

/**
 * The same identifier with the version written as a whole byte, which is the
 * one difference under test: the low nibble of byte 6 is the top four bits of
 * the sequence, and writing the byte whole takes them.
 */
function naive(d: number): Uint8Array {
	const b = new Uint8Array(newId(d).bytes);
	b[6] = 0x80;

	return b;
}

beforeEach(() => {
	// The clock is held still so that "in one millisecond" is what this is
	// actually about, rather than however many the loop happens to take.
	vi.useFakeTimers();
	vi.setSystemTime(new Date("2026-08-08T00:00:00.000Z"));
	forget();
});

afterEach(() => {
	vi.useRealTimers();
});

describe("identifiers made inside one millisecond", () => {
	it("come back in the order they were made", () => {
		const vs = Array.from({ length: n }, () => newId(7).bytes);

		expect(ordered(vs), "the sequence counter was lost; see the note in newId").toBe(true);
	});

	it("would not if the version were written as a byte", () => {
		const vs = Array.from({ length: n }, () => naive(7));

		expect(ordered(vs), "this was expected to lose its order").toBe(false);
	});

	it("keep their order past the four thousand the sequence holds", () => {
		// Exhausting it borrows a millisecond from the future, which is the one
		// answer that keeps both properties: starting the count over would make
		// two identifiers that sort the wrong way round, and waiting for the
		// clock would make a call that blocks inside what is otherwise
		// arithmetic.
		const vs = Array.from({ length: 5000 }, () => newId(7).bytes);

		expect(ordered(vs)).toBe(true);
		expect(new Set(vs.map((v) => v.toString())).size).toBe(vs.length);
	});
});

describe("the counter", () => {
	it("counts up inside a millisecond and starts over when the clock moves", () => {
		expect(stamp()).toEqual({ ms: Date.now(), seq: 0 });
		expect(stamp()).toEqual({ ms: Date.now(), seq: 1 });
		expect(stamp()).toEqual({ ms: Date.now(), seq: 2 });

		vi.advanceTimersByTime(1);
		expect(stamp()).toEqual({ ms: Date.now(), seq: 0 });
	});

	it("does not go back when the clock does", () => {
		// A correction from NTP, or a laptop waking up. An identifier that sorted
		// before one already written would be read twice, or not at all, by
		// anything reading in identifier order.
		const first = stamp();
		vi.setSystemTime(new Date("2020-01-01T00:00:00.000Z"));

		const second = stamp();
		expect(second.ms).toBe(first.ms);
		expect(second.seq).toBe(first.seq + 1);
	});
});
