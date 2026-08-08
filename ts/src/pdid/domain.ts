/**
 * A domain says what kind of thing an identifier names.
 *
 * It is a number the schema declares and never a name, because it has to fit in
 * one byte of an identifier. The name is what a person writes -- see
 * {@link register} -- and the two are declared together so that they cannot
 * drift.
 *
 * Go says this with a `uint8` and TypeScript has one number type, so the range
 * is checked wherever a domain is written rather than by the type. It is not a
 * formality: `d & 0xff` would store 256 as the byte 0, and 0 is what an
 * identifier from another deployment reads as -- a silent demotion to "not one
 * of ours" instead of a mistake somebody hears about.
 */
export type Domain = number;

/**
 * The domain of an identifier that is not one of ours, and the one number
 * nothing may be registered as. An entity that forgot to declare a domain would
 * otherwise be every entity that forgot.
 */
export const Unknown: Domain = 0;

/**
 * What the schema declared, as generated code said it.
 *
 * The Go half locks this because a test that registers something is a write
 * after the reads have started. Here there is one thread and nothing to lock,
 * and the thing worth knowing instead is that a worker is another realm: it
 * loads its own copy of this module and registers into its own tables, so a
 * domain declared on the page is not declared in the worker.
 */
const byEntity = new Map<string, Domain>();
const byName = new Map<string, Domain>();
const names = new Map<Domain, string>();

/**
 * Records what the schema declared: the message `entity` is of domain `d`, and
 * a person writes that domain as `name`.
 *
 * Generated code calls it as the module loads, which is why it throws rather
 * than answering with anything -- a number used twice, or a domain of
 * {@link Unknown}, is a schema that should not have generated, and there is
 * nobody to hand a result to at that point.
 *
 * Registering the same entity twice with the same values is allowed, so that a
 * bundle holding two copies of the generated code does not fail to load.
 */
export function register(entity: string, d: Domain, name: string): void {
	assertDomain(d);
	if (d === Unknown) {
		throw new Error(
			`pdid: ${entity}: domain 0 is what an unregistered identifier reads as, so nothing may hold it`,
		);
	}
	if (entity === "" || name === "") {
		throw new Error(`pdid: domain ${d}: an entity and a name are both required`);
	}

	const e = byEntity.get(entity);
	if (e !== undefined && e !== d) {
		throw new Error(`pdid: ${entity}: already registered as domain ${e}, now ${d}`);
	}

	const n = names.get(d);
	if (n !== undefined && n !== name) {
		throw new Error(`pdid: domain ${d}: already registered as "${n}", now "${name}"`);
	}

	const v = byName.get(name);
	if (v !== undefined && v !== d) {
		throw new Error(`pdid: "${name}": already registered as domain ${v}, now ${d}`);
	}

	byEntity.set(entity, d);
	byName.set(name, d);
	names.set(d, name);
}

/** Answers with the domain of the message with the given full name. */
export function lookup(entity: string): Domain | undefined {
	return byEntity.get(entity);
}

/**
 * Answers with the domain a person writes as `name`, which is how the `#domain`
 * of a slug is read.
 */
export function domainOf(name: string): Domain | undefined {
	return byName.get(name);
}

/**
 * The name the schema gave this domain, or its number if nothing registered it
 * -- which is what an identifier from somewhere else looks like.
 */
export function domainName(d: Domain): string {
	const v = names.get(d);
	if (v !== undefined) {
		return v;
	}
	if (d === Unknown) {
		return "unknown";
	}

	return `domain(${d})`;
}

/**
 * Lists what has been registered, for a diagnostic that wants to say what this
 * side does know about.
 */
export function domains(): Map<Domain, string> {
	return new Map(names);
}

/**
 * An identifier of the wrong kind.
 *
 * A slug that says it names one kind of thing where another was expected throws
 * a subclass of this rather than an error of its own: whatever wants to know
 * "was I handed the wrong kind of thing" is asking one question, and whether it
 * was written as text or as sixteen bytes is not part of it.
 *
 * It carries no status code, unlike the Go half. This layer knows neither
 * protobuf nor a transport, and a code here would be the first thing to know
 * one.
 */
export class DomainError extends Error {
	constructor(
		readonly want: Domain,
		readonly got: Domain,
		message?: string,
	) {
		super(message ?? `id: names a ${domainName(got)}, and this is a ${domainName(want)}`);
		this.name = "DomainError";
	}
}

/**
 * Sixteen bytes, or the text of them, that do not say what they name.
 *
 * The Go half has two errors here -- one for text that is not a UUID at all and
 * one for a UUID that is not ours -- because its parsing comes from another
 * package. They are one thing to whoever asked: a string that does not name a
 * row in this deployment. The message says which rule broke.
 */
export class NotAnIdError extends Error {
	constructor(
		readonly value: string,
		why: string,
	) {
		super(`id: ${why}`);
		this.name = "NotAnIdError";
	}
}

/**
 * Refuses a domain that could not be written into a byte.
 *
 * `Number` is the only number TypeScript has, so this is the check Go gets from
 * declaring `uint8`: without it a domain of 300, or of 7.5, would be written as
 * some other domain and the identifier would name the wrong kind of thing while
 * looking perfectly well-formed.
 */
export function assertDomain(d: Domain): void {
	if (!Number.isInteger(d) || d < 0 || d > 0xff) {
		throw new RangeError(`pdid: ${d} is not a domain; a domain is a whole number from 0 to 255`);
	}
}
