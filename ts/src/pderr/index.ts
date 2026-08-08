/**
 * A refusal, as a form can use it.
 *
 * The server puts the field path beside the message rather than inside it, in a
 * `google.rpc.BadRequest`; this is the other end of that. A page that had to
 * parse the message to find out which box to draw under would be making the
 * message a wire format nobody declared, and every rewording of it would break
 * a page.
 *
 * # The words are not for the person filling in the form
 *
 * payday does not carry a language, a tone, or a set of translations, and a
 * framework that starts writing sentences for end users has taken all three on.
 * So [Violation.why] is developer prose: what a UI matches on is the **code and
 * the field path**, and the sentence it shows is the app's, from a table the
 * app owns.
 *
 * That is why [violations] answers with paths and not with a rendered message,
 * and why [messages] takes the table rather than having one.
 *
 * @module
 */

import { ConnectError, Code } from '@connectrpc/connect'

import { BadRequestSchema } from '../../gen/google/rpc/error_details_pb.js'

/** Violation is one field of a request that was not acceptable. */
export interface Violation {
	/**
	 * Where it was, as a path from the root of the request in the proto field
	 * names: `alias`, `slug.tenant.alias`, `items[2].alias`.
	 *
	 * Empty means the request as a whole -- two fields that are each fine and
	 * contradict each other have nowhere else to go.
	 */
	readonly field: string

	/** The rule that was broken, for whoever is reading the code. */
	readonly why: string
}

/**
 * violations answers with what an error says was wrong, and an empty list for
 * one that does not say.
 *
 * Empty is the whole of the answer a page needs for every refusal that was
 * never about a form: it has a message and a code, which is what it had before
 * any of this, and it renders whatever it renders for a refusal it cannot
 * place.
 */
export function violations(err: unknown): Violation[] {
	if (!(err instanceof ConnectError)) return []

	const vs: Violation[] = []
	for (const d of err.findDetails(BadRequestSchema)) {
		for (const f of d.fieldViolations) {
			vs.push({ field: f.field, why: f.description })
		}
	}

	return vs
}

/**
 * byField is [violations] keyed by the box to draw under.
 *
 * Several violations on one field are joined, because a form has one place to
 * put them and dropping the second would be dropping a rule the caller also
 * broke.
 */
export function byField(err: unknown): Map<string, string[]> {
	const vs = new Map<string, string[]>()
	for (const v of violations(err)) {
		const got = vs.get(v.field)
		if (got === undefined) vs.set(v.field, [v.why])
		else got.push(v.why)
	}

	return vs
}

/**
 * messages renders a refusal with **the app's** words.
 *
 * `say` is the app's table: it is given the field path and the developer prose,
 * and answers with what to show somebody -- or undefined, which falls back to
 * whatever `say` does with an unknown field.
 *
 * It exists so that the fallback is a thing the app decides rather than
 * developer prose leaking onto a page, which is what happens when a UI shows
 * `err.message` because the field was not in its table.
 */
export function messages(
	err: unknown,
	say: (field: string, why: string) => string,
): Map<string, string[]> {
	const vs = new Map<string, string[]>()
	for (const v of violations(err)) {
		const got = vs.get(v.field)
		const text = say(v.field, v.why)
		if (got === undefined) vs.set(v.field, [text])
		else got.push(text)
	}

	return vs
}

/**
 * isInvalid reports a refusal that is about the request rather than about the
 * caller, the row, or the server.
 *
 * It is the check a form makes before looking for violations: everything else
 * -- gone, not allowed, not reachable -- is a different thing to show and none
 * of it belongs under a box.
 */
export function isInvalid(err: unknown): boolean {
	return err instanceof ConnectError && err.code === Code.InvalidArgument
}
