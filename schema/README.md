# schema

payday's own entities: the tenant a deployment is divided by, the holder a
request is from, and the trail of what was written.

**They are sources an app generates from, not a module it imports.** That is why
they are here rather than under `proto/`, which is the buf module holding the
`payday.entity` option — an app *imports* the option and *copies* these.

The reason is that generation is one set of types in one module. An app's
messages, its ent schema and its servers are generated together; an entity that
arrived as somebody else's compiled package would be a second ent client and a
second store, and the edge from a row to its tenant would not compile. `pd gen`
copies these, merges whatever the app added in `proto/ext/payday/*.ext.proto`,
and rewrites `go_package` to the app's module.

Fields **1, 2, 4..7 and 13..15 are payday's** — 3 is the app's, held for the
set edge — and an app adds its own in 8..12 and from 16. An overlay that
redeclares a number payday owns is refused: protobuf-merge takes the overlay's
word, so `alias` would quietly become whatever it said.

## `past/`

Two more, which an app has only when it asks for them: an `Event` for each of
its operations and a `Fact` for each field of a resource from a moment on, the
app's past (payday#151). An app declares it once, on the file that says its
package is the app's:

```proto
option (payday.app) = {past: {event: 17, fact: 16}};
```

and `pd gen` copies these two in beside the rest -- the same copy, the same
overlays in `proto/ext/payday/`, the same refusals -- and takes them out when
the declaration goes.

**Their domains are the app's.** Each file says `domain: 0`, which is no domain
at all, and `pd gen` writes the declared number in its place. A table every app
has gets a number payday fixes; a table an app opts into is numbered by the app,
because payday would otherwise be taking a number from every app that had
already spent it. Every field number they declare is payday's -- their 8..12
included -- and an overlay adds at the ones they leave empty.

