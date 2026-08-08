/**
 * `pdid` is the identifier every row of a payday app is named by.
 *
 * It is a UUID, and it carries one byte saying what kind of thing it names.
 * That byte is the whole of what this adds, and it buys three things a plain
 * UUID cannot:
 *
 * - **A reference can be checked before the server is.** Something handed a
 *   Holder's identifier where it wanted a Tenant's refuses it here, with a
 *   message that says which kind it actually was, rather than sending a request
 *   that comes back NotFound saying nothing.
 * - **A row that is gone can still be named.** An audit trail records what was
 *   written to by identifier and not by table, so a row erased later leaves an
 *   identifier that answers to nothing. The domain outlives the row.
 * - **Whatever answers to it can be found without asking every table.** On this
 *   side that is a local store with one key space rather than one per kind.
 *
 * # The layout
 *
 * It is a UUIDv8, which is the version the standard set aside for exactly this:
 * everything but the version and variant is the implementation's to define.
 *
 * ```
 *  0                   1                   2                   3
 *  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 * |                          unix_ts_ms                           |
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 * |          unix_ts_ms           |1 0 0 0|          seq          |
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 * |1 0|    rand   |     domain    |             rand              |
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 * |                             rand                              |
 * +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
 * ```
 *
 * A millisecond timestamp, a twelve-bit counter that orders what is made inside
 * one of them, the domain, and 54 bits of randomness.
 *
 * The counter is the part worth knowing about, because it is easy to lose. The
 * Go half gets it from `uuid.NewV7`, whose twelve bits of `rand_a` are a
 * sequence that makes identifiers made in the same millisecond come out in the
 * order they were made; there is nothing to get it from here, so it is kept
 * here. It is also why the version is written into the high nibble of byte 6 and
 * not as the whole byte: the low nibble is the top four bits of that sequence,
 * and taking them leaves 256 orderable identifiers per millisecond instead of
 * 4096.
 *
 * The domain sits at byte 9, which is the second half of the fourth group when a
 * UUID is written out. It reads without counting:
 *
 * ```
 * 0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a
 *                     ^^ domain
 * ```
 *
 * The fourth group always starts with 8, 9, a or b, since that is what the
 * variant makes of it, so the two digits that matter are the ones after it.
 *
 * # Nothing is a domain until something says so
 *
 * A {@link Domain} is a number, and what it means is the app's. {@link register}
 * is how generated code says it, from what the schema declared, so that the
 * number and the name it goes by are said once and in the same place. Reading a
 * domain out of an identifier that nothing registered answers with the number
 * and no name, which is what an identifier from another deployment looks like.
 *
 * # Why this exists on this side at all
 *
 * Making an identifier is the one thing a client cannot ask the server for
 * without waiting for it. A client that can make its own writes a batch of rows
 * that already refer to one another, so the batch API needs no language for
 * "the thing I am about to create" -- and the same code names a row it is about
 * to send, holds it in a local store, and finds it again when the round trip
 * comes back.
 *
 * @module
 */

export * from "./domain.js";
export * from "./id.js";
