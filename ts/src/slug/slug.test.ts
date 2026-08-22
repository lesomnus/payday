import { describe, expect, it } from "vitest";

import { DomainError as IdDomainError, Unknown, register } from "../pdid/domain.js";
import { AliasError } from "./alias.js";
import { DomainError, NoSuchDomainError, Slug, is, parse } from "./slug.js";

// Domains used by the tests, registered as generated code would register them,
// so that the word after the "#" resolves to something.
const Tenant = 1;
const Holder = 2;
const Robot = 7;

register("test.Tenant", Tenant, "tenant");
register("test.Holder", Holder, "holder");
register("test.Robot", Robot, "robot");

describe("reading a slug", () => {
	it("reads every shape the format has", () => {
		for (const [v, what, tenant, alias, domain] of [
			["@acme/arm-01#robot", "all of it", "acme", "arm-01", Robot],
			["@acme/arm-01", "no domain, which the reader knows", "acme", "arm-01", Unknown],
			["@acme#tenant", "a tenant, which nothing is above", "", "acme", Tenant],
			["@arm-01", "neither, which the caller's context knows", "", "arm-01", Unknown],
			["arm-01", "not even the mark", "", "arm-01", Unknown],
			["acme/arm-01#robot", "all of it without the mark", "acme", "arm-01", Robot],
		] as [string, string, string, string, number][]) {
			const u = parse(v);

			expect(u.tenant, what).toBe(tenant);
			expect(u.alias, what).toBe(alias);
			expect(u.domain, what).toBe(domain);
			expect(u.hasTenant, what).toBe(tenant !== "");
			expect(u.hasDomain, what).toBe(domain !== Unknown);
		}
	});

	it("folds the ways of writing one name into one name", () => {
		const want = parse("@acme/arm-01#robot");
		for (const v of [
			"@acme/arm-01#robot",
			"  @acme/arm-01#robot  ",
			"@ACME/Arm-01#Robot",
			"acme/arm-01#robot",

			// Room around the separators is the same untidiness as room around the
			// whole thing, and normalizing is what makes two spellings one value.
			"@acme / arm-01 # robot",
		]) {
			expect(want.equals(parse(v)), `${v} named something else`).toBe(true);
		}
	});

	it("refuses a part that is missing rather than untidy", () => {
		for (const [v, what] of [
			["", "nothing at all"],
			["@", "the mark alone"],
			["/arm-01", "a tenant that is not there"],

			// The form this came from built exactly this out of an empty tenant,
			// and read it back as a slug with no tenant -- so one row had two names
			// that were not the same string.
			["@/arm-01", "the same, marked"],

			["acme/", "an alias that is not there"],
			["#robot", "a kind of thing and nothing it applies to"],
			["@acme/arm-01#", "a hash that promises a kind of thing"],
			["@acme/robots/arm-01", "a tenant of a tenant"],
			["@acme/Arm_01", "an alias that is not one"],
			["@1cme/arm-01", "a tenant that is not one"],
		]) {
			expect(() => parse(v), what).toThrow();
		}
	});

	it("says which half was wrong", () => {
		expect(() => parse("@1cme/arm-01")).toThrow(AliasError);
		expect(() => parse("@1cme/arm-01")).toThrow(/^tenant:/);
		expect(() => parse("@acme/-arm")).toThrow(/^alias:/);
	});
});

// Parsing dropping the "@" and printing never putting it back is the asymmetry
// this came from: a slug that went through both came out as a different string
// than it went in as, which shows up as a cache key that misses or a config file
// that stops matching itself.
describe("writing a slug back out", () => {
	it("writes the mark that proves it is one", () => {
		for (const v of ["@acme/arm-01#robot", "@acme/arm-01", "@acme#tenant", "@arm-01"]) {
			const u = parse(v);

			expect(u.toString()).toBe(v);
			expect(is(u.toString()), `${u} would be taken for an identifier`).toBe(true);
		}
	});

	it("and reading it back names the same row", () => {
		for (const v of ["@acme/arm-01#robot", "  ACME/Arm-01  ", "acme#tenant", "arm-01"]) {
			const u = parse(v);
			const w = parse(u.toString());

			expect(u.equals(w)).toBe(true);
			expect(u.toString()).toBe(w.toString());
		}
	});

	it("goes into a document as the text it prints as", () => {
		// The alternative is three fields nothing reads back, carrying a domain
		// number that means whatever the deployment reading it thinks it means.
		const v = parse("@acme/arm-01#robot");

		expect(JSON.stringify({ of: v })).toBe('{"of":"@acme/arm-01#robot"}');
	});
});

describe("the mark", () => {
	it("is the only thing that says a name is not an identifier", () => {
		// A UUID and an alias cannot be told apart by looking at them, so
		// something has to say.
		const id = "abcd1234-2a10-8abc-8a03-9f2e1c4d5b6a";

		expect(is(id)).toBe(false);
		expect(is(`@${id}`), "a name that looks like an identifier is still a name").toBe(true);
		expect(is("@acme/arm-01")).toBe(true);
		expect(is("  @acme/arm-01")).toBe(true);
		expect(is("acme/arm-01")).toBe(false);
		expect(is("")).toBe(false);
	});
});

describe("building a slug from its parts", () => {
	it("takes the parts and normalizes them", () => {
		expect(new Slug("  Acme ", "Arm-01", Robot).toString()).toBe("@acme/arm-01#robot");
	});

	it("takes an empty part as one left to the reader", () => {
		const v = new Slug("", "arm-01");

		expect(v.hasTenant).toBe(false);
		expect(v.hasDomain).toBe(false);
		expect(v.toString()).toBe("@arm-01");
	});

	it("refuses a part that cannot be a name", () => {
		expect(() => new Slug("acme", "", Robot)).toThrow(AliasError);
		expect(() => new Slug("ACME CORP", "arm-01", Robot)).toThrow(/^tenant:/);
	});
});

describe("the domain a slug asserts", () => {
	it("is taken from where the slug was read when it says nothing", () => {
		const v = parse("@acme/arm-01").expect(Robot);

		expect(v.domain).toBe(Robot);
		expect(v.toString()).toBe("@acme/arm-01#robot");
	});

	it("is kept when it agrees", () => {
		expect(parse("@acme/arm-01#robot").expect(Robot).domain).toBe(Robot);
	});

	it("is refused when it disagrees, rather than obeyed", () => {
		let err: unknown;
		try {
			parse("@acme/admin#holder").expect(Robot);
		} catch (e) {
			err = e;
		}

		// The same error an identifier of the wrong kind throws, since it is the
		// same mistake written another way.
		expect(err).toBeInstanceOf(IdDomainError);
		expect(err).toBeInstanceOf(DomainError);

		// And it says what it actually was, which is the whole point of refusing
		// here rather than looking for a robot named "admin".
		expect((err as Error).message).toContain("holder");
		expect((err as Error).message).toContain("robot");
	});

	it("cannot be checked when nothing registered the word, so it is refused", () => {
		// The form this came from answered "unknown" for a word it did not have,
		// which is what it also answered for a slug that said nothing -- so a typo
		// here silently became no assertion at all, and the check that would have
		// caught it never ran.
		expect(() => parse("@acme/admin#robto")).toThrow(NoSuchDomainError);

		// It says what this side does have, since somebody who wrote the wrong
		// word wants the list of right ones.
		expect(() => parse("@acme/admin#robto")).toThrow(/robot/);
		expect(() => parse("@acme/admin#")).toThrow(NoSuchDomainError);
	});

	it("prints as its number when this deployment does not know it", () => {
		// An identifier from another deployment reads like this, and printing the
		// number is the honest answer: this side cannot say what a 200 is, so it
		// cannot check anything written about one either -- which is why reading it
		// back is refused rather than guessed at.
		const v = parse("@acme/arm-01").withDomain(200);

		expect(v.toString()).toBe("@acme/arm-01#domain(200)");
		expect(() => parse(v.toString())).toThrow(NoSuchDomainError);
	});
});

// The first of the three defects this came from: the domain was assembled by
// cutting the text at the "#" and appending the new word, and the "#" was in
// neither half.
describe("saying what a slug names", () => {
	it("writes the separator with the word", () => {
		expect(parse("@acme/admin").withDomain(Robot).toString()).toBe("@acme/admin#robot");
	});

	it("and the concatenation it replaced named another row entirely", () => {
		// fleet: a[:i] + Slug(v.String()), where i is the index of the "#" or the
		// end. This is what made it dangerous rather than merely ugly --
		// "acme/adminrobot" is a well-formed slug that parses, names a different
		// row, and says nothing about having been mangled.
		const a = "acme/admin";
		const i = a.includes("#") ? a.indexOf("#") : a.length;
		const got = a.slice(0, i) + "robot";

		expect(got).toBe("acme/adminrobot");
		expect(parse(got).alias, "this was expected to still parse; that is the danger").toBe(
			"adminrobot",
		);
	});

	it("replaces the word rather than adding to it", () => {
		expect(parse("@acme/admin#holder").withDomain(Robot).toString()).toBe("@acme/admin#robot");
	});

	it("leaves the slug it was asked about alone", () => {
		const v = parse("@acme/admin#holder");
		const u = v.withDomain(Robot);

		expect(u.domain).toBe(Robot);
		expect(v.domain, "the original was changed").toBe(Holder);
	});
});

describe("saying who holds a slug", () => {
	it("puts the slug under the tenant it names", () => {
		// The form this came from took a slug here and read its *tenant*, so
		// handing it the tenant itself changed nothing at all -- and said nothing
		// about it.
		expect(parse("@admin#holder").withTenant("acme").toString()).toBe("@acme/admin#holder");
	});

	it("replaces one, and an empty one drops it", () => {
		const v = parse("@acme/admin").withTenant("other");
		expect(v.toString()).toBe("@other/admin");

		const u = v.withTenant("");
		expect(u.hasTenant).toBe(false);
		expect(u.toString()).toBe("@admin");
	});

	it("refuses a tenant that is not a name", () => {
		expect(() => parse("@admin").withTenant("Acme Corp")).toThrow(AliasError);
	});
});
