/**
 * `slug` is the human-readable way to name a row.
 *
 * ```
 * @[TENANT/]ALIAS[#DOMAIN]
 *
 * @acme/arm-01#robot   the robot "arm-01", held by the tenant "acme"
 * @acme/arm-01         what kind of thing it is comes from where it is written
 * @acme#tenant         a tenant, which nothing is above
 * arm-01               the tenant comes from who is asking
 * ```
 *
 * A slug is what a person writes: an authorization header, a key in a config
 * file, a URI in a certificate, a line in a log, an argument to the CLI, a
 * segment of a path in the address bar. The wire does not carry one -- a
 * reference there is a structured message that both halves already hold as a
 * type, and reading text back into it on every request would be parsing what
 * nothing had to serialize.
 *
 * # Why the "@"
 *
 * It is not decoration. A field that has to take "an identifier or a name" can
 * be handed both, and the two written forms overlap: an alias is lowercase
 * letters, digits and hyphens, and so is a UUID.
 * "abcd1234-2a10-8abc-8a03-9f2e1c4d5b6a" breaks none of the rules in
 * {@link validate}. That the identifiers this app makes happen to begin with a
 * zero digit today is an accident of the clock -- a millisecond timestamp will
 * not reach a hex letter for thousands of years -- and not a rule anybody wrote
 * down. The "@" is the rule, and {@link is} is how a field asks which of the two
 * it was given.
 *
 * # Why the domain is an assertion
 *
 * The word after the "#" is the same domain an identifier carries in its ninth
 * byte, and it comes out of the same declaration in the schema. It is optional,
 * because most places that hold a slug already know what kind of thing belongs
 * there, and when it is written it is *checked* ({@link Slug.expect}) rather
 * than obeyed.
 *
 * Obeying it would be the cheaper thing and it costs a class of silence: a slug
 * carrying the wrong word would name a different kind of row, and the
 * disagreement that could have caught it is exactly what was thrown away.
 *
 * @module
 */

export {
	AliasError,
	type Part,
	alphabet,
	aliasMaxLen,
	parseAlias,
	randomAlias,
	randomAliasN,
	validate,
} from "./alias.js";
export { DomainError, NoSuchDomainError, Slug, is, parse } from "./slug.js";
