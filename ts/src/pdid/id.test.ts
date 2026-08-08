import { beforeEach, describe, expect, it } from "vitest";

import { DomainError, NotAnIdError, Unknown, register } from "./domain.js";
import { Id, from, newId, of, parse, withDomain } from "./id.js";
import { forget } from "./stamp.js";

// Domains used by the tests, registered as generated code would register them.
// Nothing here depends on them being these particular numbers.
const Tenant = 1;
const Holder = 2;
const Robot = 7;

register("test.Tenant", Tenant, "tenant");
register("test.Holder", Holder, "holder");
register("test.Robot", Robot, "robot");

beforeEach(() => {
	forget();
});

/**
 * A fresh identifier with somebody else's version written into it, which is
 * what a UUID from another library looks like: everything about it is right
 * except the two things that say it is ours.
 */
function asVersion(n: number): string {
	const b = new Uint8Array(newId(Robot).bytes);
	b[6] = (n << 4) | (b[6] & 0x0f);

	return new Id(b).toString();
}

describe("a new identifier", () => {
	it("carries its domain", () => {
		const v = newId(Robot);

		expect(v.domain).toBe(Robot);
		expect(of(v.bytes)).toBe(Robot);
	});

	it("is a v8 with the standard variant", () => {
		const v = newId(Holder).bytes;

		expect(v[6] >> 4).toBe(8);
		expect(v[8] & 0xc0).toBe(0x80);
	});

	it("puts the domain in the last two digits of the fourth group", () => {
		// It is the whole reason byte 9 was chosen: somebody reading a log should
		// be able to see what a row was without counting bytes.
		const gs = newId(Robot).toString().split("-");

		expect(gs).toHaveLength(5);
		expect(gs[3].slice(2)).toBe("07");
	});

	it("is not the same one twice", () => {
		const seen = new Set<string>();
		for (let i = 0; i < 10000; i++) {
			const v = newId(Robot).toString();
			expect(seen.has(v), "made the same identifier twice").toBe(false);
			seen.add(v);
		}
	});

	it("refuses a domain that could not be written into a byte", () => {
		// TypeScript has one number type, so this is the check Go gets for free
		// from `uint8`. Without it 256 would be written as the byte 0, and an
		// identifier whose domain reads as Unknown looks exactly like one from
		// another deployment.
		expect(() => newId(256)).toThrow(RangeError);
		expect(() => newId(-1)).toThrow(RangeError);
		expect(() => newId(7.5)).toThrow(RangeError);
		expect(() => newId(Number.NaN)).toThrow(RangeError);
	});

	it("owns its bytes, so nothing it was made from can rename it", () => {
		const v = newId(Robot);
		const b = new Uint8Array(v.bytes);
		b[9] = Holder;

		expect(v.domain).toBe(Robot);
	});
});

describe("reading an identifier", () => {
	it("takes back what it wrote, as bytes and as text", () => {
		const v = newId(Holder);

		expect(parse(v.toString()).equals(v)).toBe(true);
		expect(from(v.bytes).equals(v)).toBe(true);
	});

	it("takes every written form the Go half takes", () => {
		const v = newId(Robot);
		const s = v.toString();

		for (const u of [
			s,
			s.toUpperCase(),
			`{${s}}`,
			`urn:uuid:${s}`,
			`URN:UUID:${s}`,
			s.replaceAll("-", ""),
		]) {
			expect(parse(u).equals(v), u).toBe(true);
		}
	});

	it("refuses a uuid this app did not make", () => {
		// A v4 is a perfectly good UUID and is refused anyway: its ninth byte is
		// random, so reading a domain out of it would be reading the caller's
		// coin toss as a claim about what the row is.
		for (const [v, what] of [
			[asVersion(4), "a v4, whose ninth byte happens to be something"],
			[asVersion(7), "a v7, which is what ours is made from"],
			["00000000-0000-0000-0000-000000000000", "the one that names nobody"],
			["00000000-0000-8000-0000-000000000000", "a v8 with the wrong variant"],
		]) {
			expect(() => parse(v), what).toThrow(NotAnIdError);
		}
	});

	it("refuses text that is not a uuid at all", () => {
		for (const v of [
			"",
			"not a uuid",
			// The hyphens have to be where they belong. Stripping them from
			// wherever they are would make this an identifier here and a parse
			// error on the server.
			"0199-c3f42a10-8abc8a039f2e1c4d5b6a",
			"0199c3f4-2a10-8abc-8a03-9f2e1c4d5bZZ",
			"{0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a",
			"urn:uuid-0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a",
		]) {
			expect(() => parse(v), v).toThrow(NotAnIdError);
		}
	});

	it("refuses bytes that are not sixteen", () => {
		expect(() => from(new Uint8Array([1, 2, 3]))).toThrow(NotAnIdError);
		expect(() => new Id(new Uint8Array(15))).toThrow(NotAnIdError);
	});

	it("copies what it was handed, since a decoder goes on writing to its buffer", () => {
		const b = new Uint8Array(newId(Robot).bytes);
		const v = from(b);
		b[9] = Holder;

		expect(v.domain).toBe(Robot);
	});

	it("says the number of a domain nothing registered", () => {
		// This is what an identifier from another deployment looks like, and it
		// reads: the version and the variant are still ours to believe.
		const v = newId(200);

		expect(of(v.bytes)).toBe(200);
		expect(v.domain).toBe(200);
	});

	it("answers Unknown for bytes that say nothing", () => {
		expect(of(new Uint8Array(16))).toBeUndefined();
		expect(new Id(new Uint8Array(16)).domain).toBe(Unknown);
	});
});

describe("an identifier read where a kind of thing was expected", () => {
	it("is kept when it names one", () => {
		const v = newId(Robot);

		expect(v.expect(Robot)).toBe(v);
	});

	it("is refused when it names another, saying which it actually was", () => {
		// Before the round trip, and in words: the alternative is a request that
		// comes back NotFound, which says nothing about why.
		expect(() => newId(Holder).expect(Robot)).toThrow(DomainError);
		expect(() => newId(Holder).expect(Robot)).toThrow(/holder/);
		expect(() => newId(Holder).expect(Robot)).toThrow(/robot/);
	});
});

describe("an identifier written into a document", () => {
	it("goes in as the text it prints as, and not as an object of sixteen fields", () => {
		const v = newId(Robot);

		expect(JSON.stringify({ id: v })).toBe(`{"id":"${v}"}`);
	});
});

describe("changing the domain", () => {
	it("leaves everything else alone and leaves the original alone", () => {
		const v = newId(Holder);
		const u = withDomain(v, Robot);

		expect(u.domain).toBe(Robot);
		expect(v.domain, "the original was changed").toBe(Holder);

		const a = new Uint8Array(v.bytes);
		const b = new Uint8Array(u.bytes);
		a[9] = 0;
		b[9] = 0;
		expect(a).toEqual(b);
	});
});
