import { describe, expect, it } from "vitest";

import {
	AliasError,
	aliasMaxLen,
	alphabet,
	parseAlias,
	randomAlias,
	randomAliasN,
	trim,
	validate,
} from "./alias.js";

// What the alphabet exists to keep out of a name somebody has to read off a
// screen and type somewhere else.
const confusable = "il01o";

describe("an alias", () => {
	it("is letters, digits and single hyphens, beginning with a letter", () => {
		for (const [v, what] of [
			["a", "a single letter"],
			["ab", "letters"],
			["a1", "a digit after a letter"],
			["arm-01", "a hyphen"],
			["a-b-c", "several hyphens"],
			["acme-corp", "the ordinary case"],
			["z", "the last letter, which a generator once could not produce"],
			["a".repeat(aliasMaxLen), "exactly the limit"],

			// The reason the "@" exists. This is a UUID and it breaks none of the
			// rules, so nothing about the shape of a string says which of the two
			// kinds of reference it is.
			["abcd1234-2a10-8abc-8a03-9f2e1c4d5b6a", "a UUID beginning with a hex letter"],
		]) {
			expect(() => validate(v), what).not.toThrow();
		}
	});

	it("is not anything else", () => {
		for (const [v, what] of [
			["", "nothing at all"],
			[" ", "a space"],
			["1a", "a leading digit, which reads as a number"],
			["-a", "a leading hyphen"],
			["a-", "a trailing hyphen"],
			["a--b", "two hyphens together"],
			["-", "a hyphen alone"],

			// An underscore is legal in neither a DNS label nor a subdomain, and
			// allowing it here would spend a door that costs nothing to keep shut.
			["a_b", "an underscore"],
			["_a", "a leading underscore"],

			["Acme", "an uppercase letter, which parseAlias would have folded"],
			["a b", "a space inside"],
			["a.b", "a dot"],
			["a/b", "a slash, which is the tenant separator"],
			["a#b", "a hash, which is the domain separator"],
			["@a", "the mark that is not part of the name"],
			["aä", "a letter that is not one of the twenty-six"],
			["a\n", "a newline at the end"],
			["a".repeat(aliasMaxLen + 1), "one character past the limit"],
		]) {
			expect(() => validate(v), what).toThrow(AliasError);
		}
	});

	it("says which half of a slug was the problem", () => {
		try {
			validate("1cme");
			expect.unreachable();
		} catch (e) {
			expect(e).toBeInstanceOf(AliasError);
			expect((e as AliasError).part).toBe("alias");
		}
	});
});

describe("reading an alias", () => {
	it("folds the ways of writing one name into one name", () => {
		for (const v of ["acme", "  acme ", "ACME", "  Acme ", "\tAcMe\n"]) {
			expect(parseAlias(v), v).toBe("acme");
		}
	});

	it("judges what it normalized, not what it was given", () => {
		// Trimming happens first, so a name that is only spaces is empty rather
		// than malformed, and the message says so.
		expect(() => parseAlias("   ")).toThrow(/empty/);

		// And folding happens first, so an uppercase name is accepted here and
		// refused by validate. Only one of the two is asked about typing.
		expect(parseAlias("ACME")).toBe("acme");
		expect(() => validate("ACME")).toThrow(AliasError);
	});

	it("takes off the space Go takes off, and no more", () => {
		// JavaScript's own trim takes the byte order mark and does not take the
		// next-line character; Go's TrimSpace is the other way round. A name that
		// folded differently on the two sides would be a row written from a page and
		// then not found from the CLI, so the set is ours rather than the runtime's.
		const nel = String.fromCharCode(0x85);
		const bom = String.fromCharCode(0xfeff);
		const ideographic = String.fromCharCode(0x3000);

		expect(trim(` acme${ideographic}`), "the space both of them take").toBe("acme");
		expect(trim(`${nel}acme`), "next line, which Go takes and JavaScript does not").toBe("acme");
		expect(trim(`${bom}acme`), "the byte order mark, which Go leaves").toBe(`${bom}acme`);
		expect(() => parseAlias(`${bom}acme`)).toThrow(AliasError);
	});
});

describe("a name nobody chose", () => {
	it("is an alias", () => {
		for (let i = 0; i < 1000; i++) {
			const v = randomAlias();
			expect(v).toHaveLength(7);
			expect(() => validate(v), v).not.toThrow();
		}
	});

	// The birthday bound is the point rather than a nuisance, and demanding
	// none was this test being wrong about its own subject.
	//
	// Ten thousand draws from 23^7 collide about 1.5% of the time -- n^2/2N, or
	// 1e8 over 6.8e9 -- so a test that asserted uniqueness failed about one run
	// in seventy, and it did, here, after the Go twin had already been fixed
	// for it. What is worth asserting is that the draws are wide and
	// independent; uniqueness is the database's to enforce.
	it("collides about as often as the arithmetic says", () => {
		const n = 10000;
		const seen = new Set<string>();
		let dups = 0;
		for (let i = 0; i < n; i++) {
			const v = randomAlias();
			if (seen.has(v)) dups++;
			seen.add(v);
		}

		// One is expected about one run in seventy; three would mean the draws
		// are not what they claim to be, and that is what this catches.
		expect(dups, `the names are narrower than ${alphabet.length}^7`).toBeLessThan(3);
	});

	it("holds nothing that can be misread", () => {
		for (let i = 0; i < 1000; i++) {
			const v = randomAliasN(aliasMaxLen);
			for (const c of confusable) {
				expect(v.includes(c), `${v} holds a ${c}`).toBe(false);
			}
		}
	});

	it("can begin with any letter of the alphabet", () => {
		// This is the defect the letters-only alphabet closed. Drawing the first
		// character from its own modulus is what made it possible, and the
		// modulus was one short: over a thirty-six character charset, `v % ('z' -
		// 'a')` never reaches the z.
		const seen = new Set<string>();
		for (let i = 0; i < 5000; i++) {
			seen.add(randomAlias()[0]);
		}

		for (const c of alphabet) {
			expect(seen.has(c), `no name ever began with ${c}`).toBe(true);
		}
	});

	it("refuses a length that is not an alias", () => {
		expect(() => randomAliasN(0)).toThrow(RangeError);
		expect(() => randomAliasN(-1)).toThrow(RangeError);
		expect(() => randomAliasN(aliasMaxLen + 1)).toThrow(RangeError);
	});
});
