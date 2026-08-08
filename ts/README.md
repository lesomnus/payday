# @lesomnus/payday

The two ways a [payday](https://github.com/lesomnus/payday) app names a row —
the identifier a machine passes around, and the slug a person writes — for
whatever runs outside the server.

This is the bottom layer of the client side, and the only one that takes nothing
with it: no protobuf, no transport, no framework, no dependencies. It is the
same rules as the Go half of `pdid` and `slug`, and it is shipped rather than
rewritten because rules written twice drift, and the way that drift is noticed
is a row that one side can name and the other cannot find.

The reason to have it at all is that **an identifier can be made here.** A
client that mints its own writes a batch of rows that already refer to one
another, so the batch API needs no language for "the thing I am about to
create" — no placeholder, no second pass, no server-assigned handles to swap
back in.

```sh
npm install @lesomnus/payday
```

## The identifier

```ts
import { pdid } from "@lesomnus/payday";

// Generated code says this, from what the schema declared.
pdid.register("app.Robot", 7, "robot");

const v = pdid.newId(7);
v.toString(); // "0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a"
v.domain; //     7
//                                   ^^ it is in there, at byte 9

pdid.parse(somebodysUuid); // throws unless it is a v8 of the standard variant
pdid.of(sixteenBytes); //      the same question, answered rather than thrown
v.expect(2); //                throws: it names a robot, and this is a holder
```

It is a UUIDv8 carrying a millisecond timestamp, a twelve-bit sequence, one byte
saying what kind of thing it names, and 54 bits of randomness. Two things about
it are worth knowing:

- **Identifiers made inside one millisecond come back in the order they were
  made.** That is the sequence, and it is why the version is written into a
  nibble rather than a byte — the other nibble is the top of the counter.
- **The version and the variant are checked before the domain is read.** A v4
  whose ninth byte happens to be 7 is not a robot; it is a UUID somebody else
  made, and reading a domain out of it would be reading a coin toss as a claim.

## The slug

```ts
import { slug } from "@lesomnus/payday";

const v = slug.parse("@acme/arm-01#robot");
v.tenant; // "acme"
v.alias; //  "arm-01"
v.domain; // 7

slug.parse("@acme/arm-01").expect(7); // takes the domain from where it was read
slug.parse("@acme/admin#holder").expect(7); // throws: it says it is a holder
```

The grammar is `@[TENANT/]ALIAS[#DOMAIN]`, and an alias begins with a lowercase
letter, holds `[a-z0-9]` joined by single hyphens, and is at most 63 characters
— the length of a DNS label. Case and surrounding space are folded on the way
in, so `"  ACME / Arm-01 "` and `"acme/arm-01"` are one slug.

**Why the `@`:** a field that takes "an identifier or a name" can be handed
both, and the two look alike — `abcd1234-2a10-8abc-8a03-9f2e1c4d5b6a` is a
perfectly legal alias — so the mark is the only thing that says which one this
is, and `slug.is()` is how a field asks.

**Why the domain is an assertion rather than a name:** obeying `#robot` would
make a slug carrying the wrong word name a different kind of row, silently;
checking it (`Slug.expect`) turns the same typo into the disagreement that
catches it.

## Agreeing with the server

Everything here is written against the Go half and refuses what it refuses. Two
places where that took saying out loud, because the runtime would otherwise have
had an opinion of its own:

- **Whitespace.** `String.prototype.trim` takes off the byte order mark and
  leaves U+0085; Go's `strings.TrimSpace` is the other way round. The set is
  written out by code point so that both halves fold the same names together.
- **The written forms of a UUID.** All four Go reads — plain, braced,
  `urn:uuid:`-prefixed, and the thirty-two digits with no hyphens — are read
  here, and the hyphens are checked where they belong rather than stripped from
  wherever they are.

## Development

```sh
npm run check   # types, including the tests
npm test        # vitest
npm run build   # dist, with declarations
```

## Licence

[Apache 2.0](https://github.com/lesomnus/payday/blob/main/LICENSE).
