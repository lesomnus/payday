# Notification adoption contract

Implemented by [#178](https://github.com/lesomnus/payday/pull/178), tracking
[#182](https://github.com/lesomnus/payday/issues/182). The authoritative messages
and RPCs are in the [public contract](contracts/notify/notification.proto),
checked against the [source copied into an app](../schema/notify/notification.proto).
The [reference](notify.md) specifies the runtime and the
[guide](guide/notifications.md) demonstrates generated Go/TypeScript usage.

## Global scope and persistent readers

The default app declaration generates tenant-scoped notifications. Setting
`notify.global: {}` generates global private records with no Tenant field or
relationship. The same public tenant selectors remain available for tenant
apps: required there, absent in global apps. The app declaration fixes the mode;
a request or runtime option cannot widen a generated server's namespace.
Changing the mode of existing storage requires an explicit migration.

Notify reuses `Entity.Global` and `Entity.Tenanted` in a `oneof tenancy`.
`global: {}` and `tenanted: {field: "tenant_id"}` are mutually exclusive;
omission retains the shipped records' `tenant_id` tenancy. Explicit `tenanted`
must name that field: the copied records have no `tenant` edge. Other paths,
columns and stamps are refused because they do not exist on these records.

Global records need no fake Tenant, sentinel tenant ID or nullable-Tenant
uniqueness exception. Publication keys and lazy receipts remain unique under
concurrent calls. Both modes still require authenticated/trusted context,
server-established producers and explicit Topic authorization.

The application resolves a persistent reader ID from that context. Web and TUI
acting as the same owner use the same reader across restarts and devices;
other identities have independent receipts. A browser connection or token is
not the reader's durable identity. The application creates the identity and
controls delegation; the client never supplies a receipt owner. A fake Holder
is not required. Global policy methods receive `pdid.Nil` as the tenant argument.

## Reader queries and badge counts

`Read.filter` accepts exact kinds, source IDs and unread-only selection. Empty
selector lists impose no restriction, alternatives within a list use OR, and
the different predicates use AND. Invalid/duplicate selectors are rejected.
The store applies all predicates before limiting a page, including receipt
absence for this reader. Normalized filters, namespace and scope mode bind the
signed cursor alongside its existing authorization context.

`Count` evaluates the same selection without paging. Total and per-Topic
results share one database snapshot. `count` includes all matching notifications;
`source_count` deduplicates nonempty source IDs. A source-less notification
contributes only to `count`. Topic entries follow request order, including
zeros. The total source count deduplicates across Topics and is not the sum
of their individual source counts. Unauthorized Topics cannot yield partial
counts.

This lets a session badge count unique unread Sessions instead of completed
turns, and a Task badge select its own completion kinds, without downloading
notification history. `Read`, `Count`, `Mark` and `Listen.ready` identify the
resolved reader so clients can partition and clear their cached projections.

## Atomic marking of explicit references

`Mark` accepts 1–100 distinct refs in one namespace and returns ordered receipts
and the resolved reader. It checks every reference within the transaction,
creates all missing receipts or none, and preserves existing IDs/first-read
times. Concurrent retries agree on the same receipts. Overlapping batches take
locks in ID order to avoid deadlocks.

Only explicitly named notifications are marked. A notification arriving during
the call remains unread. A timestamp or notification page cursor is not a
commit/read-through watermark. The pre-release single-ref request and Receipt
response have been replaced; regenerate app/client code together.

## Subscription and reconciliation

`Listen` remains READY plus Topic invalidation, not payload delivery. Subscribe
before querying, wait for READY, then reconcile `Read` and `Count`. An invalidation
arriving while either query runs schedules another pass rather than being
cleared by an older response. A stale Read cannot erase an already observed
receipt for the same namespace/reader/notification.

Reconnect with that same sequence. Reader/access changes retain the existing
termination rules. Mutations invalidate only the relevant reader's receipts
after commit. The scoped-broker work in
[#179](https://github.com/lesomnus/payday/issues/179) can optimize dispatch without
changing this protocol.

## cxz's integration boundary

cxz has global Project, Session and Task resources. A Project resource Topic can
carry source Session/Task IDs and kinds such as `session.turn.completed`,
`session.question.requested`, `session.turn.failed` and `task.completed`.
Payloads carry the app event/version needed to locate the source. Every token
or tool event need not become an alert. Reading a Question notification does
not answer or dismiss the pending Question.

Notification read state is not a transcript cursor. cxz separately keeps how
far the user has actually read a Session, with its journal generation when
sequences can reset. Fetching history alone does not advance it. Notifications
whose source events were actually read can then be passed to `Mark`.

Task records belong to the project runtime and the Manager proxies their API.
Publish from the authoritative server with a stable occurrence key: retries of
one transition reuse it; a later completion after reopening does not. State and
publication can share a transaction only within the same storage boundary.
Journal/Manager crossings need a durable checkpoint or outbox with idempotent
replay, rather than client-only Watch inference. cxz still needs to choose
that boundary and its persistent reader policy when adopting this feature.

## Verification

The runtime tests cover filtering across pages, source counts, cursor binding,
policy enforcement, ordered/atomic/retried marking and reader isolation.
The sample app exercises real generated SQL and concurrent writes. A separate
full-generation fixture switches the app to global mode and runs it without
Tenant rows, including duplicate publication and receipt races. The same
fixtures run against SQLite and PostgreSQL. Contract snapshots, generated
Go/TypeScript and WASM builds are checked with the ordinary generation gates.
