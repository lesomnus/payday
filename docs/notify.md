# Notifications: proposed contract

This is the contract for the optional notification feature discussed in
[payday#177](https://github.com/lesomnus/payday/issues/177). The
[Protobuf contract](contracts/notify/notification.proto) is compiled separately
so that its messages and RPCs can be reviewed before the feature is implemented.
It is not a service registered by payday today. No `App.notify` option, database
migration, generated server, or client integration is introduced by this contract.

The intended integration follows the past: an app opts into a feature whose
entities live in its own Protobuf package and whose domains it chooses. Here the
entities are `Notification` and `NotificationReceipt`. The service is therefore
`app.NotificationService`, with `app` standing for the app's package, rather than
a fixed `payday.NotificationService`. Keeping a past is not a prerequisite.

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

Both forms are scoped to a tenant. A named Topic `maintenance` in one tenant is
distinct from the same name in another. A name that happens to look like a UUID
is also distinct from a resource Topic containing that UUID. There is no implicit
trimming, case folding, path syntax, or conversion between the two forms.
Names contain 1 to 256 UTF-8 bytes; Kind contains 1 to 128 UTF-8 bytes.
All resource IDs are nonzero payday IDs in domains registered by the app.
`tenant_id` is required on Add and the Topic queries; Get and MarkRead derive
the tenant from the scoped notification lookup.

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

These are the decisions a future runtime interface must expose, not names of Go
interfaces already implemented. An unknown or unhandled Topic is denied. Every
read RPC requires a persistent resolved reader; an authenticated frame alone is
insufficient. A publisher may instead be an authenticated service principal: its
producer key need not be a Holder ID and must stay stable across retries and app
replicas.

Every operation first respects the request's tenant scope. Topic policy cannot
widen the tenant wall. `List`, `Inbox`, and `Listen` authorize their complete
explicit Topic set before reading or subscribing. `Get` and `MarkRead` read the
notification through the wall and apply the same Topic policy; an absent or
inaccessible notification is answered as `NotFound`.

An implementation must apply these rules to every route that can reach the
data, including generated methods, Batch operations, relationship expansion,
and `ResourceService.Watch`. Private writes must be sealed through the stack or
its method policy, not just an outer interceptor that Batch can bypass. Custom
read rules also need their historical equivalent in the resource projector.
Neither a client-supplied Topic nor a CEL filter is an authorization decision.

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
`MarkRead`; absence means unread for an authorized reader. Receipt absence does
not decide whether the notification is visible. The first server-assigned
`data_read` is preserved across retries and concurrent calls. This contract has no
mark-unread, seen, archived, or acknowledged state.

Clients cache Inbox projections under `(tenant, reader, notification)`. A stale
Inbox response without a receipt must not erase a receipt already observed from
`MarkRead` or a newer query for the same tuple. Reading is monotonic in this
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
| `Inbox` | Pages `NotificationInboxItem` values, pairing each notification with this reader's receipt when one exists |
| `MarkRead` | Returns this reader's `NotificationReceipt`, creating it lazily |
| `Listen` | Streams readiness and Topic invalidation hints |

Publication completes the generated meaning of `Add`, so there is no second
`Publish` RPC beside it. `Get` and `List` return notification values without
adding per-reader state to a shared resource. `Inbox` is an explicit projection,
which also avoids changing the generated List item type or making clients load
all readers' receipts through an unrestricted relationship expansion. It is not
a separate service. Topic selection is a hand-declared query contract; the
existing generated List filter vocabulary does not implement it automatically.
This preview is not a drop-in overlay on today's generated service. The
implementation must supply the declared Topic query through generation and
server wiring while retaining the shared entity results.

`List`, `Inbox`, and `Listen` take 1 to 100 distinct Topics in one tenant; empty
sets and duplicates are invalid. They do not discover every Topic a reader might
be allowed to read. An app can provide that discovery from its own relationships
without making reverse membership lookup a requirement of the feature.

List and Inbox use `(date_created, id)` in descending order. Size zero selects
50, positive sizes above 100 are capped at 100, and negative sizes are invalid.
Their cursors are authenticated, opaque, and bound to the tenant,
authenticated identity, resolved reader, RPC, normalized Topic set, query filters,
and order. Topic order in a request does not change its selection. Reusing a
cursor for a different scope is `InvalidArgument`.

Pages reflect current authorization rather than a point-in-time snapshot.
Relationship changes can alter what is visible between pages. Implementations
must narrow by tenant and authorized Topics before applying the page limit,
then read only the current reader's receipts. Any future unread filter must
also be applied before the limit.

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

An app's domain change and notification publication must be able to use the
same database transaction. The implementation must use payday's existing
`BeginTx`, `enttx.Rebind`, and layer `WithDriver` pattern; sharing a context alone
does not share a transaction. Notification and Receipt mutations must pass
through the recording path so that audit, version, and change records commit
with the mutation. A direct SQL UPSERT that silently skips those records does
not satisfy this contract.

Concurrent `MarkRead` calls return the same receipt and first read time. A
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

The client keeps the stream open, waits for `ready`, then calls `Inbox` (or
`List`). A hint received during that query marks the result dirty and causes
another query after it finishes. After reconnecting, the client repeats this
sequence. A backlog or lost broker connection ends the stream so the client
cannot silently retain state after losing a hint.

A notification write invalidates its Topic. A receipt write invalidates that
Topic for the same reader's sessions. App policy changes are another input:
after committing a change to Job ownership, membership, delegation, or another
authorization dependency, the app must notify the runtime to invalidate affected
Topics. This is an internal app operation, not an unauthenticated RPC. The
framework cannot infer arbitrary policy dependencies from application code.

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

## What implementing this contract must demonstrate

- One publication creates one Notification and zero Receipts for any reader count.
- Resource and named Topics remain separate and isolated by tenant.
- The Job example works for a Robot and for authorized Holder readers without
  making all readers Holders or creating subscription rows.
- Topic policy cannot be bypassed through ID lookup, Inbox, raw CRUD, Batch,
  relationship loading, or the resource stream.
- Publication retries and concurrent MarkRead preserve identity and first times;
  rollback publishes nothing and commits preserve the required records.
- A second session for the same reader learns of the first receipt creation;
  another reader's state is unchanged and undisclosed, and a stale Inbox response
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

The proposal is checked independently of the published schema:

```sh
cd docs/contracts/notify
buf format --diff --exit-code
buf lint
buf build
```
