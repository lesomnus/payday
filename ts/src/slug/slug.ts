import {
	type Domain,
	DomainError as IdDomainError,
	Unknown,
	assertDomain,
	domainName,
	domainOf,
	domains,
} from "../pdid/domain.js";
import { parseAs, trim } from "./alias.js";

/**
 * A name for one row.
 *
 * It holds the three parts rather than the text they were written as. A string
 * -- which is what this was before -- is a name anything can be cast into, so a
 * function taking one has been told nothing; and every accessor has to split the
 * text again, which is a parser running wherever a caller happened to ask a
 * question. Here, holding one is proof that it parsed.
 *
 * There is no slug that names nothing, which the Go half has because every Go
 * type has a zero value. Here a field nobody filled in is `undefined`, and so
 * every slug that exists has an alias.
 */
export class Slug {
	/** The alias of who holds this, or empty when the writer left it to the reader. */
	readonly tenant: string;

	/** The name itself, and the one part that is never optional. */
	readonly alias: string;

	/**
	 * What the writer said this names, and {@link Unknown} when they said
	 * nothing. Nothing may register as Unknown -- see `pdid.register` -- so "no
	 * domain" cannot be confused with a real one.
	 */
	readonly domain: Domain;

	/**
	 * Answers with the slug naming `alias`, held by `tenant`, of domain `d`.
	 *
	 * An empty tenant and a domain of {@link Unknown} are how a caller says that
	 * part is left to whoever reads it. Both parts are normalized as
	 * {@link parseAlias} normalizes, so a slug built from what a person typed is
	 * the same value as one built from what the database holds.
	 */
	constructor(tenant: string, alias: string, d: Domain = Unknown) {
		assertDomain(d);

		this.tenant = tenant === "" ? "" : parseAs("tenant", tenant);
		this.alias = parseAs("alias", alias);
		this.domain = d;
	}

	get hasTenant(): boolean {
		return this.tenant !== "";
	}

	get hasDomain(): boolean {
		return this.domain !== Unknown;
	}

	/**
	 * Writes this slug out, with the "@" that {@link parse} does not insist on.
	 *
	 * Reading the output back answers with an equal slug. That was not true of
	 * the form this came from, where parsing dropped the "@" and printing never
	 * put it back, so a value that went through both came out as a different
	 * string than it went in as -- which is a difference that shows up as a
	 * cache key that misses or a config file that stops matching itself.
	 *
	 * A domain this deployment never registered prints as its number, and such a
	 * slug does not read back. That is the honest answer rather than a lossy
	 * one: this side cannot say what kind of thing "domain(200)" is, so it
	 * cannot check anything written about it.
	 */
	toString(): string {
		let s = "@";
		if (this.tenant !== "") {
			s += `${this.tenant}/`;
		}
		s += this.alias;
		if (this.domain !== Unknown) {
			s += `#${domainName(this.domain)}`;
		}

		return s;
	}

	/**
	 * The written form again, for whatever is written out with
	 * `JSON.stringify`.
	 *
	 * A slug goes into documents -- a config key, a JSON field, a flag -- and
	 * without this it goes in as `{"tenant":"acme","alias":"arm-01","domain":7}`,
	 * which is three fields nothing reads back and a domain number that means
	 * whatever the deployment that reads it thinks it means.
	 */
	toJSON(): string {
		return this.toString();
	}

	equals(v: Slug): boolean {
		return this.tenant === v.tenant && this.alias === v.alias && this.domain === v.domain;
	}

	/**
	 * Answers with this slug held by `tenant`, or with no tenant at all when
	 * `tenant` is empty.
	 *
	 * It takes the alias of the tenant and not another slug. Taking a slug is
	 * what the form this came from did, and it read the *tenant* of what it was
	 * handed, so `parse("admin").withTenant(parse("acme"))` answered with
	 * "admin" -- unchanged, and quietly, since "acme" names a tenant without
	 * being under one.
	 */
	withTenant(tenant: string): Slug {
		return new Slug(tenant, this.alias, this.domain);
	}

	/**
	 * Answers with this slug saying it names a `d`.
	 *
	 * It is a claim being written, not a row being renamed: what it is for is
	 * putting the domain a reader knows from context onto a slug that arrived
	 * without one, so that the whole thing can be printed. Use {@link expect} to
	 * go the other way and check a claim that was already made.
	 */
	withDomain(d: Domain): Slug {
		return new Slug(this.tenant, this.alias, d);
	}

	/**
	 * Answers with this slug read where a `d` was expected.
	 *
	 * A slug that said nothing takes `d` from where it was read, which is why
	 * the domain is optional at all. A slug that said something else is
	 * **refused**, with the same error a reference of the wrong kind is refused
	 * with, because the two are the same mistake written two ways and a caller
	 * should not have to learn which one they made.
	 */
	expect(d: Domain): Slug {
		if (this.domain === Unknown) {
			return this.withDomain(d);
		}
		if (this.domain !== d) {
			throw new DomainError(this, d, this.domain);
		}

		return this;
	}
}

/**
 * Reads a slug out of its written form.
 *
 * The leading "@" is optional here and certain in {@link Slug.toString}: a slug
 * arrives from places that have already decided the field is a name -- a config
 * key, a CLI argument, a path segment -- and demanding the mark there would be
 * demanding it of somebody who has nothing to disambiguate. It is written back
 * because whatever reads the output next may not be so sure; see {@link is}.
 *
 * Each part is normalized on its own, so "@ACME / Arm-01" and "@acme/arm-01"
 * are the same slug for the same reason "  Acme " and "acme" are the same
 * alias. What is refused is a part that is missing rather than untidy:
 * "/admin", "acme/" and "@admin#" are somebody's typo, and reading them as
 * though the missing part had been left out on purpose would name a row nobody
 * asked for.
 */
export function parse(s: string): Slug {
	let v = trim(s);
	if (v.startsWith("@")) {
		v = v.slice(1);
	}

	let d = Unknown;
	const hash = v.indexOf("#");
	if (hash >= 0) {
		const name = trim(v.slice(hash + 1)).toLowerCase();

		const got = domainOf(name);
		if (got === undefined) {
			throw new NoSuchDomainError(name);
		}

		d = got;
		v = v.slice(0, hash);
	}

	const slash = v.indexOf("/");
	if (slash < 0) {
		return new Slug("", v, d);
	}

	// An empty tenant is refused rather than dropped, which is why this does not
	// hand the empty string to the constructor: "@/arm-01" is what the form this
	// came from built out of a slug with no tenant, and read back as a slug with
	// no tenant -- so one row had two names that were not the same string.
	const tenant = parseAs("tenant", v.slice(0, slash));

	return new Slug(tenant, v.slice(slash + 1), d);
}

/**
 * Reports whether `s` is written as a slug rather than as an identifier.
 *
 * This is the whole job of the "@": one field, two kinds of reference, and a
 * mark that says which without either side guessing from the shape of what
 * follows. An alias is lowercase letters, digits and hyphens, and so is a UUID
 * -- "abcd1234-2a10-8abc-8a03-9f2e1c4d5b6a" breaks none of the rules an alias
 * has to keep.
 */
export function is(s: string): boolean {
	return trim(s).startsWith("@");
}

/**
 * A slug that says it names one kind of thing where another was expected.
 *
 * It extends the error an identifier of the wrong kind throws rather than
 * carrying an identity of its own: whatever wants to know "was I handed the
 * wrong kind of thing" is asking one question, and whether the caller wrote it
 * as text or as sixteen bytes is not part of it.
 */
export class DomainError extends IdDomainError {
	constructor(
		readonly slug: Slug,
		want: Domain,
		got: Domain,
	) {
		super(want, got, `slug: ${slug} names a ${domainName(got)}, and this is a ${domainName(want)}`);
		this.name = "SlugDomainError";
	}
}

/**
 * A "#word" naming a kind of thing this deployment has no domain for.
 *
 * It is refused rather than read as "no domain was said", which is what the
 * form this came from did -- its lookup answered with the unknown domain for
 * anything it did not recognize, so "@acme/admin#robto" was a slug with no
 * assertion in it and the check that would have caught the typo never ran.
 *
 * The message says what this side does have, since whoever wrote the wrong word
 * usually wants the list of right ones. On this side that list is what
 * generated code registered as it loaded, so an empty one is more often a
 * bundle that did not import the schema than a deployment with no kinds.
 */
export class NoSuchDomainError extends Error {
	constructor(readonly kind: string) {
		super(NoSuchDomainError.why(kind));
		this.name = "NoSuchDomainError";
	}

	private static why(name: string): string {
		if (name === "") {
			return 'slug: the "#" says a kind of thing follows, and none does';
		}

		const vs = [...domains().values()].sort();
		if (vs.length === 0) {
			return `slug: nothing here is a "${name}"; nothing has declared a kind at all`;
		}

		return `slug: nothing here is a "${name}"; it has ${vs.join(", ")}`;
	}
}
