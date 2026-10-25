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

Each says its domain once and is copied at it, with one exception:
`archived.proto` and `access.proto` say 5 and 6, which apps on payday had spent
on entities of their own before payday took them. An app whose own entity holds
one gives payday's a number of its own, on the file that says its package is
the app's:

```proto
option (payday.app) = {domains: {archived: 240, access: 241}};
```

and `pd gen` writes it over the one `domain:` line of the copy before the
overlay is merged (payday#163). The rest keep theirs in every app.

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
has keeps the number payday gives it; a table an app opts into is numbered by
the app, because payday would otherwise be taking a number from every app that
had already spent it. Every field number they declare is payday's -- their 8..12
included -- and an overlay adds at the ones they leave empty.

What a row of the Event holds is a message the app declares for each kind of
operation, `option (payday.event)`, and `pd gen` keeps a lock beside the copies
that holds every one of them to what rows of it were written with -- see
`docs/guide/past.md`.

## `notify/`

An app enables notifications with two distinct domains of its own:

```proto
option (payday.app) = {notify: {notification: 40, receipt: 41}};
```

`pd gen` copies `notification.proto` and `notification_receipt.proto` into the
app's package, numbers their internal `NotificationRecord` and
`NotificationReceiptRecord`, and takes the copies out when the option goes.
The first file also supplies the public notification values and
`NotificationService`; these are projections and RPCs rather than extra tables.
Topic authorization and the identity whose receipt is recorded remain app
policy. A past is independent: either feature may be enabled without the other.

The record templates have domain zero until copied. Like the past's templates,
every record field they declare is payday's; an overlay adds its own outside
those numbers and may add indexes. The optional-feature marker is distinct
from its domain, so generation finds a record by its role rather than a fixed
number or the name of the app's package.
