import { random } from "../random.js";
import { type Domain, DomainError, NotAnIdError, Unknown, assertDomain } from "./domain.js";
import { stamp } from "./stamp.js";

/** An identifier is a UUID, and a UUID is sixteen bytes. */
const size = 16;

/** The version this deployment's identifiers are, and the variant of every UUID worth reading. */
const version = 8;
const variantRfc4122 = 0x80;

/**
 * An identifier that says what kind of thing it names.
 *
 * The sixteen bytes are the value: this holds them and nothing else, and two
 * identifiers naming the same row hold the same sixteen. `bytes` is the array
 * itself rather than a copy of it, so writing into it renames a row -- which is
 * why every way of making one here allocates its own, and why {@link from}
 * copies what it was handed.
 *
 * There is no identifier that names nothing, which the Go half has as `Nil`
 * because every Go type has a zero value. Here that is `undefined`, and sixteen
 * zero bytes cannot become one of these anyway: {@link from} reads their
 * version and variant and refuses them.
 */
export class Id {
	readonly bytes: Uint8Array;

	/**
	 * Takes sixteen bytes and asks nothing about them, which is what a
	 * conversion does in the Go half.
	 *
	 * The ways in that check are {@link parse} and {@link from}. This one is for
	 * whatever already knows -- a store reading back what it wrote -- and for a
	 * test that has to hold an identifier that is deliberately wrong.
	 */
	constructor(bytes: Uint8Array) {
		if (bytes.length !== size) {
			throw new NotAnIdError(format(bytes), `invalid UUID (got ${bytes.length} bytes)`);
		}

		this.bytes = bytes;
	}

	/** What this identifier names, and {@link Unknown} if it is not one of ours. */
	get domain(): Domain {
		return of(this.bytes) ?? Unknown;
	}

	/** The written form, which is also the key it is stored under on this side. */
	toString(): string {
		return format(this.bytes);
	}

	/**
	 * The written form again, for whatever is written out with
	 * `JSON.stringify`.
	 *
	 * Without it an identifier goes into a document as `{"0":1,"1":153,...}`,
	 * which is what an object holding a typed array looks like, and nothing
	 * reads that back.
	 */
	toJSON(): string {
		return this.toString();
	}

	equals(v: Id): boolean {
		for (let i = 0; i < size; i++) {
			if (this.bytes[i] !== v.bytes[i]) {
				return false;
			}
		}

		return true;
	}

	/**
	 * Answers with this identifier read where a `d` was expected, and refuses it
	 * if it names something else.
	 *
	 * This is the half of the Go `Mint` that belongs on this side. The other
	 * half -- deciding what key a row is written under -- is the server's, and
	 * the server checks again: what a client asserts about an identifier it
	 * hands over is the client's word. What this buys is the same mistake caught
	 * before the round trip, in a message that says which kind it actually was
	 * rather than a NotFound that says nothing.
	 */
	expect(d: Domain): Id {
		assertDomain(d);

		const got = this.domain;
		if (got !== d) {
			throw new DomainError(d, got);
		}

		return this;
	}
}

/**
 * Answers with a fresh identifier of the given domain.
 *
 * The layout is the one in the package comment: a millisecond timestamp, the
 * twelve-bit sequence that orders what is made inside one of them, the domain,
 * and the rest random.
 *
 * The version is written into the high nibble of byte 6 and the low nibble is
 * left alone, because the low nibble is the top of that sequence. Writing the
 * byte whole -- which is the obvious way, and what an earlier implementation of
 * this idea did -- leaves 256 orderable identifiers per millisecond instead of
 * 4096.
 */
export function newId(d: Domain): Id {
	assertDomain(d);

	const b = random(size);
	const { ms, seq } = stamp();

	// Six bytes of milliseconds, written by division rather than by shifting: a
	// shift in JavaScript is an operation on 32 bits, and a millisecond
	// timestamp stopped fitting in 32 of them seven weeks after the epoch.
	let t = ms;
	for (let i = 5; i >= 0; i--) {
		b[i] = t % 256;
		t = Math.floor(t / 256);
	}

	b[6] = (version << 4) | ((seq >> 8) & 0x0f);
	b[7] = seq & 0xff;
	b[8] = (b[8] & 0x3f) | variantRfc4122;
	b[9] = d;

	return new Id(b);
}

/**
 * Answers with the domain `b` names, and `undefined` if `b` is not an
 * identifier of this shape at all.
 *
 * The check is the point. Every sixteen bytes can be read as a UUID and every
 * UUID has a ninth byte, so without it anything would be taken to be making a
 * claim about itself -- a v4 whose ninth byte happens to be 3 would pass as
 * whatever 3 was registered as. What the version and the variant say is the
 * only reason to believe the rest.
 */
export function of(b: Uint8Array): Domain | undefined {
	if (b.length !== size) {
		return undefined;
	}
	if (b[6] >> 4 !== version) {
		return undefined;
	}
	if ((b[8] & 0xc0) !== variantRfc4122) {
		return undefined;
	}

	return b[9];
}

/**
 * Reads an identifier out of the sixteen bytes a message carries.
 *
 * It refuses bytes that are not an identifier of this shape, since whoever sent
 * them is naming a row this deployment never wrote. They are copied on the way
 * in: what a decoder hands over is usually a view onto a buffer it goes on
 * writing to.
 */
export function from(b: Uint8Array): Id {
	if (b.length !== size) {
		throw new NotAnIdError(format(b), `invalid UUID (got ${b.length} bytes)`);
	}

	const v = new Uint8Array(b);
	if (of(v) === undefined) {
		throw new NotAnIdError(format(v), `${format(v)} is not an identifier this app makes`);
	}

	return new Id(v);
}

/**
 * Reads an identifier out of its written form.
 *
 * It takes every form the Go half takes -- the plain one, braced, prefixed with
 * `urn:uuid:`, and the thirty-two digits with no hyphens -- so that a string one
 * half reads is a string the other half reads. Taking fewer would mean refusing,
 * in the browser, a name the server had already accepted.
 */
export function parse(s: string): Id {
	const v = decode(s);
	if (of(v) === undefined) {
		throw new NotAnIdError(s, `${format(v)} is not an identifier this app makes`);
	}

	return new Id(v);
}

/**
 * Answers with this identifier as one of `d`.
 *
 * It is for tests and for whatever has to build an identifier a piece at a
 * time. An identifier that names a row already has a domain, and changing it
 * makes one that names nothing.
 */
export function withDomain(v: Id, d: Domain): Id {
	assertDomain(d);

	const b = new Uint8Array(v.bytes);
	b[9] = d;

	return new Id(b);
}

const hex = Array.from({ length: 256 }, (_, i) => i.toString(16).padStart(2, "0"));

function format(b: Uint8Array): string {
	let s = "";
	for (let i = 0; i < b.length; i++) {
		if (i === 4 || i === 6 || i === 8 || i === 10) {
			s += "-";
		}
		s += hex[b[i]];
	}

	return s;
}

/** Where the bytes of the plain written form begin, in order. */
const digits = [0, 2, 4, 6, 9, 11, 14, 16, 19, 21, 24, 26, 28, 30, 32, 34];

/**
 * Reads the written forms into bytes, and refuses everything else.
 *
 * The hyphens are checked where they belong rather than stripped from wherever
 * they are. Stripping is the shorter thing to write and it makes
 * "0199-c3f42a10-8abc8a039f2e1c4d5b6a" an identifier here and a parse error on
 * the server, which is the sort of disagreement this package exists in order
 * not to have.
 */
function decode(s: string): Uint8Array {
	const b = new Uint8Array(size);

	let v = s;
	switch (v.length) {
		case 36:
			break;

		// The URN form, which is what an XML document or a certificate holds.
		case 36 + 9:
			if (v.slice(0, 9).toLowerCase() !== "urn:uuid:") {
				throw new NotAnIdError(s, `invalid urn prefix: "${v.slice(0, 9)}"`);
			}
			v = v.slice(9);
			break;

		// The braced form, which is what a Windows registry holds.
		case 36 + 2:
			if (v[0] !== "{" || v[37] !== "}") {
				throw new NotAnIdError(s, `invalid bracketed UUID format: ${s}`);
			}
			v = v.slice(1, 37);
			break;

		// The hyphenless form, which is what a URL that could not spare them
		// holds.
		case 32:
			for (let i = 0; i < size; i++) {
				b[i] = byteAt(s, v, i * 2);
			}
			return b;

		default:
			throw new NotAnIdError(s, `invalid UUID length: ${s.length}`);
	}

	if (v[8] !== "-" || v[13] !== "-" || v[18] !== "-" || v[23] !== "-") {
		throw new NotAnIdError(s, `invalid UUID format: ${s}`);
	}
	for (let i = 0; i < size; i++) {
		b[i] = byteAt(s, v, digits[i]);
	}

	return b;
}

const isByte = /^[0-9a-f]{2}$/i;

function byteAt(s: string, v: string, i: number): number {
	const d = v.slice(i, i + 2);
	if (!isByte.test(d)) {
		throw new NotAnIdError(s, `invalid UUID format: ${s}`);
	}

	return Number.parseInt(d, 16);
}
