/**
 * The half of payday that knows neither protobuf nor a transport.
 *
 * Two things live here, and they are the two ways a payday app names a row: the
 * identifier a machine passes around, and the slug a person writes. Everything
 * above this -- generated types, a transport, a local store -- can be taken or
 * left; this cannot be left, because the alternative to having it is writing the
 * same rules a second time and finding out they drifted when a row goes missing.
 *
 * It is shipped for one reason above the others: an identifier can be made
 * here. A client that mints its own writes a batch of rows that already refer
 * to one another, so the batch API needs no language for "the thing I am about
 * to create" -- no placeholder, no two-pass fixup, no server-assigned handles
 * to swap back in.
 *
 * The two are separate modules and nothing here flattens them together, because
 * both have a `parse`, a `validate` and a `withDomain` and they mean different
 * things:
 *
 * ```ts
 * import { pdid, slug } from "@lesomnus/payday";
 * // or, taking one of them alone:
 * import * as pdid from "@lesomnus/payday/pdid";
 * ```
 *
 * @module
 */

export * as pdid from "./pdid/index.js";
export * as slug from "./slug/index.js";
