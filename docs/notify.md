# Notifications

Notifications are an optional feature: an app chooses its Topic policies and
publishes when domain work completes, while payday stores notifications, records
each reader's first read lazily, and manages live invalidations. Start with
[adding notifications to an app](guide/notifications.md) for setup and examples.
The runtime's detail is the [`notify` package](../notify/notify.go); the
[Protobuf source](../schema/notify/notification.proto) is copied into the app.

An app opts in through `(payday.app).notify`, choosing the domains of its
notification and receipt identifiers. The public messages and service live in
its own Protobuf package: `app.NotificationService`, with `app` standing for the
app's package. The generated `pd.Notifications` helper binds that service to the
app's server stack, driver, Watch, and policy; the app registers it explicitly
with `app.RegisterNotificationServiceServer`. Keeping a past is not a prerequisite.

The [adoption contract](notify-adoption-contract.md) covers global apps,
filtered reader queries, source counts and atomic batch marking, including
the boundary between notification receipts and an application read cursor.

## Topics and readers

A notification is published to a **Topic**. The Topic identifies an interest;
it does not enumerate recipients or prescribe who may read it. Clients choose
the Topics they want to query or listen to, and the app authorizes that choice.
Publishing creates one notification and no per-reader rows or subscriptions.

`NotificationTopic` has exactly one of these keys:

| Key | Meaning |
| --- | --- |
| `resource_id` | A payday identifier, whose domain identifies the resource's kind |
| `name` | An app-defined, stable, nonempty, case-sensitive key |

Both forms use the app's configured notification namespace. By default this
is a tenant: a named Topic `maintenance` in one tenant is distinct from the
same name in another. An app declaring `notify.global: {}` instead has one
global namespace, with no synthetic Tenant row or Tenant relationship. A name that happens to look like a UUID
is also distinct from a resource Topic containing that UUID. There is no implicit
trimming, case folding, path syntax, or conversion between the two forms.
Names contain 1 to 256 UTF-8 bytes; Kind contains 1 to 128 UTF-8 bytes. NUL is
rejected in Topic names, Kind, idempotency and producer keys, and payload type
URLs. The byte limits include the entire encoded string.
All resource IDs are nonzero payday IDs in domains registered by the app.
`tenant_id` is required on Add, Topic queries and Mark in tenant mode. Global
mode requires it to be absent, and public notification/receipt tenant fields
are empty. Get derives the namespace from the authorized record. Generated
servers fix the mode from the app declaration; a request cannot switch it.
Changing an existing store's mode requires an explicit migration.

The resource form needs no `job/` or `tenant/` prefix: `pdid` already carries a
domain. Its shape does not establish that a resource exists or grant permission
to it. The app defines those rules. In particular, a Tenant Topic does not mean
"all Holders of this tenant" unless the app chooses that policy.

For example, an app publishes a notification to a Job's ID when a Robot finishes
the Job. Its read policy may allow that Robot, the Robot's owner, and the Job's
issuer. The app may separately allow a named `maintenance` Topic. These are the
same notification mechanism with different app policies.

A **reader** is the persistent payday identifier under which receipts are
recorded. It can identify a Holder, a Robot, or another app resource. The server
resolves it from authenticated context and the app's delegation policy. The RPCs
contain no caller-selected receipt owner. An owner reading as themselves and an
owner authorized to act as their Robot can therefore have different receipts;
the app must make that delegation explicit.

## The app's policy contract

The runtime must provide these policy decisions without requiring the app to
manage open streams or write notification storage code:

| Decision | Input | Result |
| --- | --- | --- |
| Establish publisher | Authenticated context, tenant, Topic | Permission to publish and a stable, server-established producer key |
| Resolve reader | Authenticated context, tenant | A persistent reader ID, after validating any delegation |
| Authorize reading | Authenticated context, tenant, resolved reader, Topic | Permission to read that Topic |

In global mode these policy methods receive `pdid.Nil` as the tenant argument.
Authentication and a persistent reader remain required; global does not mean
anonymous or publicly readable.

These decisions form `notify.Policy`: `Publisher`, `Reader`, and `Read`.
The app's policy must refuse Topics it does not handle. Every read RPC requires
a persistent resolved reader; an authenticated frame alone is insufficient.
A publisher may instead be an authenticated service principal: its
producer key need not be a Holder ID and must stay stable across retries and app
replicas. Producer keys contain 1 to 256 UTF-8 bytes without NUL.

Policies that query the app database also implement `notify.PolicyBinder`.
Its `WithDriver(dialect.Driver) (notify.Policy, error)` method rebuilds the
policy's queries on the supplied driver. The runtime binds it when constructing
the service and when entering a write transaction, so authorization observes
domain rows created or changed in that same transaction. A policy using only
authenticated context and fixed named-Topic rules needs no binder.

An ordinary error from a policy is a sanitized `PermissionDenied`; its internal
message is not returned to the client. To distinguish an operational failure,
return a gRPC status with an intentional, client-safe message, such as
`Unavailable`. Context cancellation and deadlines retain their status codes.

Every operation first respects the configured global/tenant namespace. Topic policy cannot
widen the tenant wall. `List`, `Read`, `Count`, and `Listen` authorize their complete
explicit Topic set before reading or subscribing. `Get` and `Mark` read the
notification through the wall and apply the same Topic policy; an absent or
inaccessible notification is answered as `NotFound`.

The generated storage paths are private. Application calls use
`NotificationService`, whose policy checks also cover identifier lookup and
receipt writes. The generated Sink seals private records even when an outer
Audit layer is omitted. Use the normal generated stack and recording hooks;
bypassing it through another exposed store would remove that boundary.
Neither Batch nor `ResourceService.Watch` is a second public path to
notification contents. A client-supplied Topic or CEL filter is not authorization.

`notify.Policy` governs `NotificationService`; it does not filter audit or outbox
provenance. Audit RPCs can reveal publication and reader metadata; restrict
them to operators through the app's method policy. Outbox has no public RPCs;
keep its storage and drainer access inside the deployment permission boundary.

## Stored notification and receipt

The notification is immutable after publication. These concepts remain separate:

| Value | Purpose |
| --- | --- |
| Notification ID | Identifies this one publication |
| Topic | Identifies the interest being published to |
| Kind | Names the kind of occurrence, such as `job.completed` |
| Source | Optionally names the resource the occurrence concerns |
| Payload | Carries app-defined data describing the occurrence |

Source and Topic may refer to the same resource, different resources, or only
one may be a resource at all. A Topic key is used for routing and authorization;
putting an extra key inside Payload does not create another subscription scope.

Payload is an optional `google.protobuf.Any`. The publishing app validates the
kind and payload type and owns their compatibility and data-retention rules. It
must classify personal and secret content explicitly; arbitrary `Any` contents are
not automatically safe to copy to an audit log or resource stream. Readers that
do not recognize a payload type preserve its opaque bytes and metadata on binary
transports instead of failing an entire page. A JSON gateway needs an explicit
fallback for unknown `Any` types; ordinary Protobuf JSON conversion is not that
fallback. Payload field names and type names must remain readable for as long as
stored notifications use them.

`NotificationReceipt` belongs to one `(notification_id, reader_id)` pair, with a
unique constraint on that pair. It is created on the first successful
`Mark`; absence means unread for an authorized reader. Receipt absence does
not decide whether the notification is visible. The first server-assigned
`data_read` is preserved across retries and concurrent calls. This contract has no
mark-unread, seen, archived, or acknowledged state.

Clients cache Read projections under `(tenant, reader, notification)`. A stale
Read response without a receipt must not erase a receipt already observed from
`Mark` or a newer query for the same tuple. Known read state is monotonic in this
contract, so that merge preserves the known receipt while the notification
remains visible. Losing Topic access or removing an expired notification still
removes the corresponding cached item; a different reader has a different cache.

Receipts do not prolong access. If the app withdraws Topic access, a receipt
does not entitle its reader to retrieve the notification. Retention and source
deletion policies belong to the app and must preserve that boundary.

## RPC contract

All methods belong to `NotificationService` in the app's package.

| RPC | Result and meaning |
| --- | --- |
| `Add` | Publishes one immutable `Notification` |
| `Get` | Retrieves one authorized `Notification` |
| `List` | Pages authorized `Notification` values in explicit Topics |
| `Read` | Retrieves a page of `NotificationReadItem` values, pairing each notification with this reader's existing receipt; creates no receipt |
| `Count` | Counts the complete authorized reader selection and its distinct nonempty sources, overall and per Topic |
| `Mark` | Atomically records this reader's first read of explicit refs and returns ordered Receipts plus the reader ID |
| `Listen` | Streams readiness and Topic invalidation hints |

Publication is named `Add`, so there is no second `Publish` RPC beside it.
`Get` and `List` return notification values without
adding per-reader state to a shared resource. `Read` is an explicit projection,
which also avoids changing the generated List item type or making clients load
all readers' receipts through an unrestricted relationship expansion. It is not
a separate service. Topic selection is implemented by the notification service;
it does not use the ordinary generated List filter vocabulary. The public
service is registered explicitly alongside the app's ordinary entity services.

`Read` retrieves state; it does not mark notifications as read. `Mark` is the
operation that creates a receipt and records the reader's first read time.

`List`, `Read`, `Count`, and `Listen` take 1 to 100 distinct Topics in one namespace; empty
sets and duplicates are invalid. They do not discover every Topic a reader might
be allowed to read. An app can provide that discovery from its own relationships
without making reverse membership lookup a requirement of the feature.

List and Read use `(date_created, id)` in descending order. Size zero selects
50, positive sizes above 100 are capped at 100, and negative sizes are invalid.
The request's `cursor` carries the previous response's `next`.
Their cursors are authenticated, opaque, and bound to the tenant,
authenticated identity, resolved reader, RPC, normalized Topic set, normalized reader filters, app namespace, scope mode,
and order. Topic order in a request does not change its selection. Reusing a
cursor for a different scope is `InvalidArgument`.

`Read.filter` selects exact Kind values, exact source IDs and unread-only state.
Empty kind/source lists are unrestricted; within each list choices use OR and
across predicates they use AND. Each list allows at most 100 distinct valid
values. Receipt absence is evaluated only for the server-resolved reader.
Shared `List` has no reader filter. Filters are applied before paging, and are
part of the signed cursor's scope.

`Count` uses the same reader predicates without pagination. One database
statement gives consistent total/per-Topic counts and distinct-source counts.
Source-less notifications contribute to `count`, not `source_count`. The total
source count deduplicates across Topics, so it need not equal the sum of the
per-Topic source counts. Topic results follow request order and include zeros.
All requested Topics are authorized before any counts are returned.

Pages reflect current authorization rather than a point-in-time snapshot.
Relationship changes can alter what is visible between pages. Implementations
must narrow by tenant and authorized Topics before applying the page limit,
then read only the current reader's receipts. Read and Count are separate
snapshots; invalidation requires reconciling both.

### Idempotency and transactions

`Add` requires an idempotency key of 1 to 256 UTF-8 bytes. Its scope is
`(tenant, producer, key)`, with the producer established by server policy.
Repeating a publication with the same scope and content returns the same
Notification. Reusing the key with
different publication content is `AlreadyExists`. The comparison includes
Topic, Kind, Source, and Payload and excludes server-generated IDs and times.
Payload equality means the same `Any.type_url` and encoded `Any.value` bytes;
retrying callers retain that encoding rather than relying on semantic equality
between differently encoded messages. Absent Payload and present-empty Payload
are distinct. Keys are compared exactly without trimming or case folding.
Current publication policy is checked even on a retry. The app documents its
retry period and retains both the key and the original Notification throughout
it. Notification retention cannot silently shorten that guarantee. After this
period, retention may remove the notification and its key; idempotency beyond
that period is not promised. No deletion or expiry operation is exposed here.

An app's domain change and notification publication can use the same database
transaction. Begin the transaction and use the notification server's
`WithDriver` method to join it. This uses payday's existing `enttx.Rebind` and
layer `WithDriver` pattern; sharing a context alone does not share a transaction.
Notification and Receipt mutations use the recording path so that audit,
version, and change records commit with the mutation.

Database-backed policy must join that driver too. Implement `notify.PolicyBinder`
instead of closing over a database client that always uses the original driver.
Otherwise a policy cannot see an uncommitted Job or ownership change even though
the notification store has joined the business transaction.

When the app owns the outer transaction, wrap that complete operation in the
shared Watch's `Transaction` call, as in the guide. It releases immediate hints
only after the outer commit succeeds, including in a background worker. A
rebound notification server alone does not own or commit that outer transaction.

`Mark` accepts 1–100 distinct explicit refs, within one namespace, and resolves
one reader. All refs must be accessible; a missing/inaccessible/out-of-scope ref
rejects the whole transaction without partial receipts. Responses follow the
request's ref order. Single-item marking uses the same RPC with one ref.
Existing receipts retain their IDs and first-read times, including under
concurrent and retried calls. A new notification arriving during a call is not
marked unless explicitly named. Page cursors and creation times are not
read-through watermarks. A
failed transaction leaves no notification, receipt, or observable change hint.
Hints are published after commit, including writes initiated by app workers.
The existing change-notification outbox can support that delivery; this does
not introduce email, mobile push, or a delivery workflow.

### Listening and reconciliation

`Listen` is authored for this contract rather than inherited from an entity's
generated Watch. Its first response is `ready`, sent only after the live
subscription is installed. It identifies the resolved reader, which stays fixed
for the stream. Later responses contain nonempty, duplicate-free subsets of the
requested Topics. They carry no notification payload or another reader's
receipt state and may be duplicated or coalesced. There is no replay cursor,
initial snapshot, exactly-once promise, or cross-transaction ordering guarantee.

The client keeps the stream open, waits for `ready`, then calls `Read` and `Count` (or
`List`). A hint received during that query marks the result dirty and causes
another query after it finishes. After reconnecting, the client repeats this
sequence. A backlog or lost broker connection ends the stream so the client
cannot silently retain state after losing a hint.

A notification write invalidates its Topic. A receipt write invalidates that
Topic for the same reader's sessions. App policy changes are another input:
after committing a change to Job ownership, membership, delegation, or another
authorization dependency, the app must notify the runtime to invalidate affected
Topics with `NotificationServer.Invalidate`. This is an internal app operation,
not a public RPC. Check its returned error and retry a failed invalidation after
the domain transaction has committed. The framework cannot infer arbitrary
policy dependencies from application code.

The runtime revalidates reader identity and Topic access before delivering a
hint. If access has been withdrawn or the resolved reader has changed, it
terminates the subscription with `PermissionDenied`; the client discards cached
data in that subscription's scope. An app invalidation must also trigger this
revalidation for an otherwise idle stream. Current reads always reauthorize,
including when no notification row has changed.

## Refusals

| Status | Meaning |
| --- | --- |
| `InvalidArgument` | Invalid ID or Topic, unsupported request shape, or cursor from another scope |
| `Unauthenticated` | No verified request identity |
| `PermissionDenied` | Publication, explicit Topic access, or delegation is refused; a live subscription loses access |
| `NotFound` | A notification requested by ID is absent or inaccessible |
| `FailedPrecondition` | Authenticated context cannot establish a persistent reader where one is required |
| `AlreadyExists` | An idempotency key names a different publication |
| `ResourceExhausted` | A bounded request limit or live subscription backlog is exceeded |
| `Unavailable` | The live notification mechanism is unavailable; reconnect and reconcile |

## Checks for an app's integration

- One publication creates one Notification and zero Receipts for any reader count.
- Resource and named Topics remain separate and isolated by tenant.
- The Job example works for a Robot and for authorized Holder readers without
  making all readers Holders or creating subscription rows.
- Topic policy cannot be bypassed through ID lookup, Read, raw CRUD, Batch,
  relationship loading, or the resource stream.
- Publication retries and concurrent Mark preserve identity and first times;
  rollback publishes nothing and commits preserve the required records.
- A second session for the same reader learns of the first receipt creation;
  another reader's state is unchanged and undisclosed, and a stale Read response
  cannot undo an observed read.
- Ready-before-query, reconnect, backlog, and app policy changes converge through
  reconciliation, including removal of access without a new notification.
- Unknown payloads remain readable as opaque values and cannot break a page.
- Opting out leaves no notification entities, RPCs, runtime wiring, or dependency
  on the past. Opting in works inside the app process and database.

This combines recognizable mechanisms with a deliberate storage choice.
[ntfy](https://docs.ntfy.sh/publish/) demonstrates app-defined Topic keys and
subscription; [Novu](https://docs.novu.co/platform/concepts/topics) and
[Knock](https://docs.knock.app/concepts/subscriptions) separate Topic identity
from custom data but fan out to registered subscribers.
[Laravel](https://laravel.com/docs/12.x/notifications) and
[Noticed](https://github.com/excid3/noticed) support recipients beyond a User
model and store per-recipient read state. This contract instead retains one
shared notification and creates a reader's receipt only when it is needed.
