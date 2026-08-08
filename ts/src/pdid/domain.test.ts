import { describe, expect, it } from "vitest";

import { Unknown, domainName, domainOf, domains, lookup, register } from "./domain.js";

const Robot = 7;

register("test.Robot", Robot, "robot");

describe("the registry", () => {
	it("says what the schema declared, by message and by the word a person writes", () => {
		expect(lookup("test.Robot")).toBe(Robot);
		expect(domainOf("robot")).toBe(Robot);
		expect(domainName(Robot)).toBe("robot");
	});

	it("has nothing to say about what nothing declared", () => {
		expect(lookup("test.Nothing")).toBeUndefined();
		expect(domainOf("nothing")).toBeUndefined();
	});

	it("reads a domain nobody registered as its number", () => {
		// Which is what an identifier from another deployment looks like: this
		// side cannot say what a 200 is, and saying so is better than guessing.
		expect(domainName(200)).toBe("domain(200)");
		expect(domainName(Unknown)).toBe("unknown");
	});

	it("takes the same declaration twice", () => {
		// A bundle can hold two copies of the generated code, and failing to load
		// over that would be a build layout deciding whether the page runs.
		expect(() => register("test.Robot", Robot, "robot")).not.toThrow();
	});

	it("refuses to let one number mean two things", () => {
		expect(() => register("test.Other", Robot, "other")).toThrow();
		expect(() => register("test.Robot", 9, "robot")).toThrow();
	});

	it("refuses the number an unregistered identifier already reads as", () => {
		expect(() => register("test.Zero", Unknown, "zero")).toThrow();
	});

	it("refuses a declaration missing either of the two names", () => {
		expect(() => register("", 11, "eleven")).toThrow();
		expect(() => register("test.Eleven", 11, "")).toThrow();

		// Left out entirely, which the signature stops in TypeScript and does
		// not stop anywhere else -- this package is published, and a JavaScript
		// caller reaches this function with two arguments. It used to store
		// `undefined` as the name and say nothing, so the domain registered and
		// every slug that read it back got `domain(11)` instead of a word.
		// @ts-expect-error -- the point is the call TypeScript refuses
		expect(() => register("test.Eleven", 11)).toThrow();
	});

	it("lists what it has, for a message that wants to say so", () => {
		expect([...domains().values()]).toContain("robot");

		// A copy: a diagnostic that walked the registry should not be able to
		// edit it on the way past.
		domains().set(200, "spaceship");
		expect(domainName(200)).toBe("domain(200)");
	});
});
