# Routing live changes

A broker transports hints after commit. A hint tells a reader to reconcile
current state; it is neither a read grant nor a durable replay cursor.
`ResourceService.Watch` also carries immutable mutation envelopes, so adapters
must preserve each mutation even when several change the same row.

`watch.Broker` still supports Publish and Subscribe. An adapter can additionally
implement `watch.ScopedBroker.Open(ctx, interest)`. Open must install the actual
subscription before returning. The caller closes it on every exit. Transport
loss and backlog overflow close affected channels; a reconnect creates new
subscriptions rather than silently continuing streams with a gap.

## What can be routed

| Stream | Safe interest | Checks after delivery |
| --- | --- | --- |
| Generated entity Watch | Generated service and resolved immutable IDs | Current row access and projection |
| Notification Listen | Namespace, tenant, topic kind/value, shared audience or reader | Current tenant access, reader identity and topic policy |
| ResourceService Watch | Broad subscription | Per-reader public projection followed by CEL |

Interest dimensions intersect; values inside a dimension are alternatives.
A named topic remains arbitrary application text, including characters a broker
would treat as separators or wildcards. `watch.RouteKey` hashes an unambiguous
tuple into an opaque route; topic names are never raw broker subjects.

The caller's tenant (`Event.Tenant`) is not the changed row's tenant. It must not
be used to narrow entity watches: cross-tenant writes, ownership changes and
deletes would otherwise disappear. Arbitrary CEL likewise cannot be assumed to
have a safe native broker equivalent.

`Change.Routes == nil` means the publisher did not describe application routing.
Such changes must reach scoped listeners. An explicitly empty slice says the
metadata is complete and there are no application routes. Custom publishers can
leave metadata unknown until they implement it. Missing service or key metadata
also falls back to broader delivery. These hints never replace authorization.

## Work shared on a replica

`watch.Fanout` bounds each local listener independently and gives all copies of
one event a shared memo. `Event.Shared` is for context-independent enrichment,
not policy decisions, projections or a reader's receipt state. Distinct
publications always get distinct memos, including updates to the same row.

Notification Listen interprets each event once per service instance. Generated
notification changes carry their write-time tenant/topic hint, so interpreting
one requires no row lookup. A custom store's Change resolver runs once per
local event, under a separate five-second context; cancellation by one reader
does not cancel another reader's interpretation. Implement Change without using
a caller's identity. Permissions still run separately for each affected stream.
Receipts carry a hint naming their reader; only that reader's route is interested.

Memory supports scoped local delivery. A Watch using an older broadcast-only
broker shares one upstream subscription while any local listener remains.
PostgreSQL retains LISTEN/NOTIFY broadcast between replicas; local scoping saves
work, but does not reduce PostgreSQL's network fanout.

## Transaction and outbox

Generated notification recorders request row images inside the writing
transaction and produce both the hint and routes there. Outbox rows retain the
same metadata. A later drain does not have to fetch a notification that may
already have expired. Resource mutations retain their own event ID, public
images and ordering; no adapter may coalesce them by row ID.

Applications upgrading a database with data must regenerate with this payday
and run `pd migrate` before starting servers or drainers built with it. The
outbox adds nullable `routes` and `signal` columns. Rows without metadata use
broad delivery. No broker or outbox acknowledgement means a client consumed the
event: reconnecting Notification clients still reconcile with Read, and entity
Watch clients obtain a new snapshot.

## NATS transport

The optional [NATS adapter tutorial](guide/nats.md) covers deployment, policy
invalidation and reconnect flows. Ordinary Core NATS pub/sub sends a copy to
every interested replica; a shared queue group would incorrectly distribute
clients' notifications between replicas. Native patterns combine generated
service, key and opaque application route. Unknown dimensions have explicit
fallback patterns. The first local listener installs each pattern and the last
releases it. Overlapping patterns suppress copies by transport publication ID,
never by resource ID; outbox retries remain separate publications.

The implementation follows [Core NATS publish/subscribe](https://docs.nats.io/learn/core-nats/)
and its at-most-once live semantics. It adds no durable consumer claim.
