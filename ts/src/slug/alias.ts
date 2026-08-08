import { random } from "../random.js";

/**
 * The longest an alias can be.
 *
 * Sixty-three is the length of a DNS label, and that is the point: an alias
 * that fits one can be a hostname, a subdomain, a Kubernetes name, a
 * certificate SAN. None of that is needed today and all of it is cheap to keep,
 * whereas a limit raised later cannot be lowered -- by then rows are named.
 */
export const aliasMaxLen = 63;

/**
 * What a name this package makes up is spelled with: the lowercase letters,
 * less i, l and o. It is not what an alias may hold -- that is
 * {@link validate}, and it is wider -- but what is chosen when nobody is
 * choosing.
 *
 * Those three are gone because these strings are read aloud, written on paper
 * and typed back in, and "l" for "1" is a row that is not found -- or worse, a
 * row that is. Digits are gone with them: keeping "0" beside "o" or "1" beside
 * "l" is the same confusion from the other side, and the letters alone are
 * already 23^7 for a name of seven, which is more names than anything here will
 * need.
 *
 * It being letters only has a second effect worth having. Every character in it
 * is legal as the *first* character of an alias, so a generator never has a
 * special case at position zero -- which is exactly where the form this came
 * from went wrong, drawing the first character from a different modulus than
 * the rest and never once producing a "z".
 */
export const alphabet = "abcdefghjkmnpqrstuvwxyz";

/**
 * Begins with a lowercase letter, and is groups of lowercase alphanumerics
 * joined by single hyphens.
 *
 * Beginning with a letter is so that a name a person reads does not look like a
 * number they can count with. The hyphen is the only joiner -- an underscore is
 * legal in neither a DNS label nor a subdomain, and allowing it would spend a
 * door that costs nothing to keep shut.
 */
const aliasPattern = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/;

/**
 * The two places an alias can sit in a slug. They are carried in the error so
 * that whoever wrote "@acme /admin" is told which half of it was the problem,
 * which one message about "an alias" cannot say.
 */
export type Part = "alias" | "tenant";

/**
 * A string that cannot be an alias, and which rule it broke.
 *
 * It does not echo what it was given back: the rule and an example of a name
 * that keeps it are what somebody can act on.
 */
export class AliasError extends Error {
	constructor(
		readonly part: Part,
		readonly why: string,
	) {
		super(`${part}: ${why}`);
		this.name = "AliasError";
	}
}

/**
 * Refuses `v` if it is not an alias, saying which rule it broke.
 *
 * This is the bare grammar and nothing else: what a database column holds and
 * what a slug is made of. A string a person typed goes through
 * {@link parseAlias} first, which folds the difference between "  Acme " and
 * "acme" before there is anything to judge.
 */
export function validate(v: string): void {
	validateAs("alias", v);
}

export function validateAs(part: Part, v: string): void {
	if (v === "") {
		throw new AliasError(part, "must not be empty");
	}
	if (v.length > aliasMaxLen) {
		throw new AliasError(part, `must be at most ${aliasMaxLen} characters`);
	}
	if (!aliasPattern.test(v)) {
		throw new AliasError(
			part,
			'must begin with a lowercase letter and hold only lowercase letters, digits and single hyphens, such as "arm-01"',
		);
	}
}

/**
 * Normalizes `v` into an alias, or refuses it saying why it cannot be one.
 *
 * Surrounding spaces are dropped and the case is folded, so "  Acme " and
 * "acme" name the same row. The folding is what makes it safe to compare
 * aliases with "===" -- and with "=" in the database, where the column is
 * whatever was stored -- and normalizing on the way in is the only place it can
 * be done once.
 */
export function parseAlias(v: string): string {
	return parseAs("alias", v);
}

export function parseAs(part: Part, v: string): string {
	const u = trim(v).toLowerCase();
	validateAs(part, u);

	return u;
}

/**
 * Answers with an alias nobody chose, for a row that needs a name before
 * anybody has an opinion about it.
 *
 * Seven characters of {@link alphabet} is about 31 bits, which is enough that
 * these do not collide in a table a person will ever read, and short enough to
 * say over a phone.
 */
export function randomAlias(): string {
	return randomAliasN(7);
}

/**
 * {@link randomAlias} of a given length.
 *
 * It refuses a length that cannot be an alias rather than answering with
 * something that is not one. The length is written in the code and never comes
 * from a request, so there is nothing anybody could do about it at run time
 * anyway.
 */
export function randomAliasN(n: number): string {
	if (!Number.isInteger(n) || n < 1 || n > aliasMaxLen) {
		throw new RangeError(`slug: an alias of ${n} characters is not an alias`);
	}

	// 256 is not a multiple of 23, so folding a whole byte into the alphabet
	// would make the first three letters land slightly more often than the rest.
	// The skew is small enough not to matter for collisions and the fix is to
	// throw away the bytes that cause it, which costs one comparison and leaves
	// nothing to explain later.
	const limit = 256 - (256 % alphabet.length);

	let v = "";
	while (v.length < n) {
		for (const b of random(n)) {
			if (b >= limit) {
				continue;
			}

			v += alphabet[b % alphabet.length];
			if (v.length === n) {
				break;
			}
		}
	}

	return v;
}

/**
 * What Go's `strings.TrimSpace` trims, which is not what JavaScript's `trim`
 * trims.
 *
 * JavaScript also takes U+FEFF and does not take U+0085, so a name that arrived
 * with either would be one string on this side and another on the server: a row
 * written from the browser and never found from the CLI. The set is written out
 * so that both halves fold the same names together.
 */
const spaces = String.raw`\t\n\v\f\r    -     　`;
const trimmable = new RegExp(`^[${spaces}]+|[${spaces}]+$`, "g");

export function trim(v: string): string {
	return v.replace(trimmable, "");
}
