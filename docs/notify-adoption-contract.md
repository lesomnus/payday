# Notification reader-state extensions

Status: contract proposal for review in [#178](https://github.com/lesomnus/payday/pull/178).
Tracks [#182](https://github.com/lesomnus/payday/issues/182).
The RPC shapes below are proposed changes, not APIs implemented by the current
runtime. [Notifications](notify.md) and its generated contract continue to
describe what runs today. Implementation and regeneration follow contract
review; this proposal does not change the generated-contract snapshot.

## Use case

cxz has global Project, Session and Task resources. Its web client currently
keeps completion/read markers in browser session storage. Task records belong
to the project runtime; the Manager proxies their API. A notification should
survive a reconnect, and reading it in Web should update another window or TUI
without making those clients infer completion from resource snapshots.

The existing Topic, immutable publication, lazy Receipt and invalidation model
fits this use case. The missing pieces are global scope, efficient reader-state
queries and marking a displayed set without one RPC per notification.

## Global apps

Extend the existing app option, preserving its tenant-scoped default:

```proto
message Notify {
  uint32 notification = 1;
  uint32 receipt = 2;
  bool global = 3;
}
```

For example, `notify: {notification: 40, receipt: 41, global: true}` declares
one app-global notification namespace. It does not make notifications public.

| App mode | Request tenant selector              | Storage and access                     |
| -------- | ------------------------------------ | -------------------------------------- |
| Default  | A valid Tenant ID is required        | Existing tenant wall and Topic policy  |
| Global   | Tenant selector must be absent/empty | Global records and explicit app policy |

The mode is fixed for the generated notification store. A nonempty tenant in
global mode, or a missing tenant in tenant mode, is `InvalidArgument`; neither
falls back to the other mode. Changing the mode of an existing store requires
an explicit migration. Mixed global and tenant notifications in one store are
outside this proposal.

Keep the existing public `tenant_id` fields and their types. They are empty in
global mode. Do not allocate a fake Tenant, use a Project ID as a Tenant ID,
or put a sentinel UUID into that field.
Generated global records must not acquire a Tenant relationship merely for
notification storage. Publication and receipt uniqueness must work without a
nullable-Tenant uniqueness loophole.

The existing policy methods remain server responsibilities. In global mode
their tenant argument is `pdid.Nil`; this is valid only when the app opted into
that mode. Publishing still establishes a stable producer, reading still
resolves a persistent reader, and every Topic still needs authorization. A
trusted local transport can establish an app principal; an absent identity is
not an anonymous default reader.

Cursors, receipt lookups, invalidations and idempotency keys bind the app and
scope mode as well as their existing policy context. Global mode must not
weaken the private-record, Audit or common-resource-stream boundaries.

## Persistent readers

Retain the current `Reader` policy contract rather than adding a client-selected
reader field. The app maps authenticated/trusted context to a persistent payday
ID. Web and TUI acting as the same owner use the same ID across restarts and
devices. A different authorized actor has independent receipts.

Creating that identity and defining delegation belong to the app. A browser
session ID, connection ID or access token is not its durable identity. No fake
Holder is required. `Read`, `Count`, `Mark` and `Listen.ready` return the resolved
reader so clients can partition caches and discard state when identity changes.

## Filter reader projections before paging

Add a shared reader filter:

```proto
message NotificationReadFilter {
  repeated string kinds = 1;
  repeated bytes source_ids = 2;
  bool unread_only = 3;
}
```

Add `NotificationReadFilter filter = 5` to `NotificationReadRequest`; keep its
existing topics, size, cursor and tenant selectors. With an absent/empty filter,
`Read` has its current behavior.

- Empty kinds/source lists impose no restriction. Values within a list use OR;
  the different predicates and authorized Topic selection use AND.
- `unread_only` means no Receipt for the resolved reader. Another reader's
  Receipt never affects membership. Querying creates no Receipt.
- Reject invalid or duplicate selector values. Reuse current Kind/ID validation
  and the existing 100-selector bound for each list.
- Apply scope, authorization, kind/source and unread predicates before the page
  limit. Do not fetch a page and discard read items in memory.
- Bind the complete normalized filter into the signed cursor. A cursor cannot
  move between readers, filters or scope modes.

`List` remains a shared-Notification query. Reader filtering belongs to `Read`,
not an extra reader field on the Notification entity.

## Count without downloading history

Add one reader-projection RPC:

```proto
rpc Count(NotificationCountRequest) returns (NotificationCountResponse);

message NotificationCountRequest {
  repeated NotificationTopic topics = 1;
  bytes tenant_id = 4;
  NotificationReadFilter filter = 5;
}

message NotificationCountResponse {
  uint64 count = 1;
  uint64 source_count = 2;
  repeated NotificationTopicCount topics = 3;
  bytes reader_id = 4;
}

message NotificationTopicCount {
  NotificationTopic topic = 1;
  uint64 count = 2;
  uint64 source_count = 3;
}
```

`Count` applies exactly the same authorized selection and reader filter as
`Read`, without pagination. Its result is exact at one database snapshot, not
an estimate assembled by scanning client pages.

`count` counts matching Notifications. `source_count` counts distinct nonempty
`source_id` values. A notification without a source contributes only to `count`.
The top-level source count deduplicates across the requested Topics; per-Topic
source counts must not be summed to recreate it. Return one Topic result per
requested Topic, in request order, including zeros. Authorize all Topics before
returning any counts; do not silently omit unauthorized Topics.

For example, multiple unread turn completions for one Session contribute
several notifications but one source. A session badge can ask for unread session
kinds and use `source_count`; a Task badge can select Task-completion kinds.

No inbox version or cross-RPC snapshot is implied. A `Read` page and `Count`
response can observe different commits. Both need reconciliation on invalidation.

## One Mark operation for a displayed set

Generalize the existing `Mark` instead of adding competing single/batch APIs:

```proto
rpc Mark(NotificationMarkRequest) returns (NotificationMarkResponse);

message NotificationMarkRequest {
  repeated NotificationRef refs = 1;
  bytes tenant_id = 2;
}

message NotificationMarkResponse {
  repeated NotificationReceipt items = 1;
  bytes reader_id = 2;
}
```

This deliberately changes the unmerged contract: replace the singular `ref`
and Receipt response, then update the guide and generated clients together.
A single notification uses the same RPC with one reference.

- Accept 1–100 distinct references in one notification namespace. Reject an
  empty or duplicate set. Use the same tenant/global selection rule as `Read`.
- Resolve one reader, authorize every referenced Notification and revalidate
  policy within the transaction. A missing/inaccessible/out-of-scope reference
  must not disclose another scope or leave a partial batch of receipts.
- Create missing receipts and retain existing receipt IDs and first-read times.
  Concurrent/retried marks return the same per-notification receipts. A batch
  does not need a second application idempotency key.
- Commit all writes together. Return receipts in request order, including those
  that already existed, with the server-resolved reader.
- Invalidate the affected Topics for that reader after commit, coalescing them
  into one transaction notification where the broker supports it.

Only explicit references are marked. A notification arriving during the call
is not accidentally marked by a broad Topic filter or a timestamp cutoff.
Page cursors and creation times are not commit/read-through watermarks.

## Listen and reconnect

Keep `Listen` as READY plus invalidation, not notification-body delivery.
Subscribe before the initial query, wait for READY, then reconcile `Read` and
`Count`. An invalidation arriving during either query schedules another pass;
it must not be cleared by an older in-flight response. Cache first-read receipts
monotonically for the same app/scope/reader/notification.

Reconnection repeats that sequence. Reader or access changes retain the current
stream termination rules. Global subscriptions use the same app/scope namespace
as reads and writes. The scoped-broker work in
[#179](https://github.com/lesomnus/payday/issues/179) can optimize dispatch without
changing this client contract.

## What stays in cxz

Notification read state is not a transcript cursor. cxz separately keeps how far
the user has actually read a Session, including its journal generation if event
sequences can reset. Merely fetching history or opening a notification does not
advance that cursor. Notifications whose source event has actually been read
can then be passed to `Mark`.

Use a Project resource Topic, source Session/Task IDs and kinds such as
`session.turn.completed`, `session.question.requested`, `session.turn.failed`
and `task.completed`. Payloads carry the app event/version needed to locate
the source. Do not publish each token or tool event as an alert. Reading a
Question notification does not answer or dismiss the pending Question.

Publish from the authoritative server with a stable occurrence key. A repeated
Task completion after reopening is a different occurrence; a retry of the same
transition is not. Task state and publication can share a transaction only when
they share the storage boundary. Journal/Manager crossings require a durable
checkpoint or outbox and idempotent replay, not a client-only Watch heuristic.
cxz decides where that boundary and its persistent reader live before integration.

## Implementation acceptance

- Global and tenant fixtures reject the wrong scope selector. Global clients
  need no Tenant row, and unauthenticated calls do not gain access.
- Concurrent global publication/marking preserves uniqueness and first-read
  times in both SQLite and PostgreSQL; rollback emits no invalidation.
- A later unread page is still found when earlier Notifications are all read.
  Cursor reuse with a different filter/reader/scope fails.
- Counts cover more than one page, deduplicate sources correctly, include zero
  Topics and source-less notifications, and enforce policy before aggregation.
- Batch marking is atomic, bounded and retry-safe, preserves response order,
  and leaves a concurrently arriving Notification unread.
- Two Web/TUI clients for the same reader synchronize; a different reader's
  receipts and counts stay independent. Reconnect and in-flight invalidation
  races reconcile to durable state.
- Generated Go/TypeScript, the public contract snapshot, opt-in validation,
  guide examples, private-record enforcement and WASM builds move together.
- Existing no-notify and tenant-notify apps retain their current behavior except
  for the intentionally generalized pre-release `Mark` signature.

The contract review and implementation are separate checkpoints. These additions
must be implemented and validated before #178 advertises them as available.
