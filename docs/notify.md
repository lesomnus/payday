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
An app with accounts may add a namespace an account reads as itself,
`NotificationAccountService` and `pd.AccountNotifications`
([below](#an-accounts-own-notifications)).

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
`NotificationService` -- and `NotificationAccountService`, for an account
namespace -- whose policy checks also cover identifier lookup and receipt
writes. The generated Sink seals the notification record even when an
outer Audit layer is omitted, and the receipt record has no RPC at all: `Mark`
writes it, past the servers. Use the normal generated stack and recording
hooks for the rest; bypassing it through another exposed store would remove
that boundary. `ResourceService.Watch` is no second public path to
notification contents. A client-supplied Topic or CEL filter is not
authorization.

`notify.Policy` governs `NotificationService`; it does not filter audit or outbox
provenance. Audit RPCs can reveal publication metadata -- a receipt is not on
the trail, so who read what is not among it; restrict them to operators
through the app's method policy. Outbox has no public RPCs; keep its storage
and drainer access inside the deployment permission boundary.

## An account's own notifications

In an app whose holders are memberships of accounts (payday#184), a call is
served as one membership, in one tenant, and a few methods are the account's
own: its frame holds the account and no tenant (`frame.OfAccount`). Every
`NotificationService` method takes a tenant, and the wall answers a frame of no
tenant with nothing, so an account that is nobody's member -- the one an
invitation is addressed to -- could read no notification at all (payday#195).

An app opts in with `notify: {..., accounts: {}}`, which needs the app's own
`accounts` declaration. `pd gen` then copies a second service beside the first,
`NotificationAccountService`, whose `Get`, `List`, `Read`, `Count`, `Mark` and
`Listen` are each the account's own (`(payday.method) = {account: {}}`), and
writes `pd.AccountNotifications`, which serves them:

| | `NotificationService` | `NotificationAccountService` |
| --- | --- | --- |
| Called as | a membership, or anybody else a resolver frames | the account, as itself |
| Namespace | the tenant the request names | **the account namespace**: no tenant's |
| Reader | what the policy's `Reader` resolves | the same, usually the account |
| Publishing | the `Add` RPC | `AccountNotificationServer.Add`, the app's own code; no RPC |
| Policy | the app's | one of its own, handed to `pd.AccountNotifications` |

### The account namespace

It is a namespace like a tenant's, and held apart from every tenant's in both
directions:

- **An account reads no tenant's notifications.** The account service takes no
  tenant, and serves only a frame of an account calling as itself -- any other
  caller is `PermissionDenied`. A request that names a tenant is
  `InvalidArgument`, and a notification of a tenant asked for by its
  identifier is `NotFound`, as an inaccessible one always is. The tenant wall
  is not widened: what an account reads is never a row any tenant holds.
- **No tenant reads the account namespace.** `NotificationService` refuses a
  request that names no tenant, and a notification of the account namespace
  asked for by its identifier is `NotFound` to it.
- **Its Topic policy is its own.** `pd.AccountNotifications` takes a
  `notify.Policy` apart from the tenant one, asked with `pdid.Nil` for the
  tenant and an account's frame. A policy written for tenants that ignores its
  tenant argument would otherwise answer for accounts as well.

Within it, what keeps one account's notifications from another's is that
policy, as in a global app: payday holds the namespace's edges, and the app
holds its topics. An app that wants an account to read only what is addressed
to it publishes to the account's own resource Topic, `notify.Resource(account)`,
and allows that Topic to that reader alone; one that wants a notice every
account reads publishes to a named Topic every account may read.

In a tenant app the namespace is stored as rows of `tenant_id` Nil, which is
how payday's own field-tenanted records say "no tenant" (the trail, the access
record, a row's seal): the generated wall shows them to nobody, the trail files
their publications under no tenant -- the deployment's own record -- a tenant's
purge leaves them, and the expirer takes them by age with the rest. In a global
app it is the global namespace, the only one there is: an account reads, as
itself, what the policy lets it read there.

Everything else is what it is for a tenant: one notification per publication
and a receipt per first read, `Mark` atomic and ordered, the first read kept,
idempotent publication by producer and key, `Listen` hints after commit, and
cursors bound to the account and the service.

### Why not the other ways

- **Making `NotificationService`'s methods the account's own.** Every call an
  account credential makes would then be framed as the account, never as a
  membership: the tenant scope, the grant and the holder a tenant's
  notifications are authorized by would be gone from every call of every app
  with accounts, and what the account could read in each tenant would have to
  be declared, method by method, in place of the wall.
- **A table of its own for account notifications.** Two more domains for every
  app that declares accounts, a second store and a second copy of the same
  shape, to say what a Nil tenant already says in payday's own records.
- **A namespace per account.** An account's notifications would be its own by
  construction, with no policy to get wrong. But a notice to every account
  would be one notification per account, the fan-out this design exists to
  avoid -- and `notify.Resource(account)` with a policy allowing its owner
  gives the same isolation to the app that wants it.

### What it costs

- **Two services.** A page that shows a membership's notifications and the
  account's opens two streams and asks two services.
- **Two read states.** A receipt in the account namespace is the account's,
  across every tenant and device; a membership's receipt in a tenant is the
  membership's. The same person reading a tenant's notification and an
  account notification has a receipt under each identity.
- **Publishing is code.** There is no RPC to publish into the account
  namespace: a public `Add` there would be a write outside every tenant's wall
  that only a policy stood in front of. The deployment's own code publishes --
  an invitation sent, an account's workspace made -- through
  `AccountNotificationServer.Add`, under any frame its publisher policy allows.

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

Payload is an optional `google.protobuf.Any`, of a message the app declares
a notification may carry (below). The publishing app validates the kind and
payload type and owns their compatibility. Readers that do not recognize a
payload type preserve its opaque bytes and metadata on binary transports
instead of failing an entire page. A JSON gateway needs an explicit fallback
for unknown `Any` types; ordinary Protobuf JSON conversion is not that
fallback. Payload field names and type names must remain readable for as long
as stored notifications use them.

### A notification carries identifiers, not a person's data

A notification is kept for as long as `notify.keep` says, shown to every reader
its Topic policy lets in, and read through nothing that keeps a person's data in
step (payday#193). The access record is made of what the schema declares
`personal`, and nothing in an `Any` is declared, so a read of a name in a
payload is recorded nowhere. Forgetting a person blanks what declarations
point at, and reaches no notification. So the contract is that a notification
carries **identifiers**: people by their payday ids, rows by theirs, and the
page that shows a notification says them as they are called when it is read,
as an app's past does. "Kim returned the laptop" is a holder, a laptop and the
kind `asset.returned`; the sentence is made where it is shown, in the reader's
language, and a Kim forgotten since is shown as whatever a forgotten person is
shown as.

- **Kind and Topic names are routing keys**: a stable word for the kind of
  occurrence, and an app-defined interest. They are never the place for an
  email, a name, or anything a person typed. A Topic about a person is that
  person's resource id, not their name.
- **A payload is a declared message**: `option (payday.notification) = {};`
  on it. `Add` refuses a payload of any other type, with `InvalidArgument`,
  before policy or storage is asked. So every payload an app publishes is one
  the schema holds to carrying identifiers:
  - `pd gen` refuses a payload message, or a message it holds, with a
    `(payday.field)` that says anything but `personal: false`, a field holding
    an entity or a message of a generated contract (a row whole, or a Ref with
    a slug), or an `Any`, and an entity that says it is one.
  - `pd doctor` asks of every string in it that says nothing about itself, and
    of a field named for somebody. A code, a version or another system's
    identifier says `personal: false`; free text is where a name ends up, and
    belongs in the sentence the reader makes. A `bytes` field is taken for an
    identifier.
- **The access record needs nothing new.** A notification of identifiers
  shows nobody's data, and the page that resolves them reads people through
  the app's own RPCs, which record what they show as every read does. Neither
  does forgetting: an identifier is what the trail is made of too, and a
  notification holds nothing more of anybody. A receipt holds its reader's
  identifier and a time, and goes when its notification goes.

Holding a payload to a declared message was my choice building payday#193
(Claude's), over advice alone on the messages an app chose to declare. A
check that only covers what an app opts into covers least the app that most
needs it. A refusal at `Add` makes the declaration the price of a payload, so
the rules the declaration carries reach every one. Kind and Topic names are
runtime strings no declaration describes, so for them the contract is stated
here and not checked.

`NotificationReceipt` belongs to one `(notification_id, reader_id)` pair, with a
unique constraint on that pair. It is created on the first successful
`Mark`; absence means unread for an authorized reader. Receipt absence does
not decide whether the notification is visible. The first server-assigned
`date_read` is preserved across retries and concurrent calls. This contract has no
mark-unread, seen, archived, or acknowledged state.

Clients cache Read projections under `(tenant, reader, notification)`. A stale
Read response without a receipt must not erase a receipt already observed from
`Mark` or a newer query for the same tuple. Known read state is monotonic in this
contract, so that merge preserves the known receipt while the notification
remains visible. Losing Topic access or removing an expired notification still
removes the corresponding cached item; a different reader has a different cache.

Receipts do not prolong access. If the app withdraws Topic access, a receipt
does not entitle its reader to retrieve the notification. Source deletion
policies belong to the app and must preserve that boundary; how long a
notification is kept is the schema's (below).

### How long a notification is kept

As long as the app's `notify.keep` says, from its publication, and no longer
(payday#194). The declaration is required:

```proto
notify: {notification: 40, receipt: 41, keep: {days: 90}}
notify: {notification: 40, receipt: 41, keep: {days: 90, retry_days: 1}}
notify: {notification: 40, receipt: 41, keep: {forever: {why: "..."}}}
```

A notification and its receipts hold what the people who use an app are
interested in and what they read. Kept for no purpose once read or stale, they
grow without bound and nothing fails, so the app says how long, or says forever
and why: the dangerous answer is the one written down.

For `days`, `pd gen` writes `pd.NotificationKeep` and `pd.NotificationExpirer`,
a `spin.Spinner` called `payday.notify.expire` that the app puts in its spin
list (`pd doctor` says so when nothing does). Each pass erases the notifications
published longer ago than `keep`, oldest first, a batch at a time, each batch in
a transaction of its own with every receipt of them:

- **Each erasure is on the trail**, as the act `payday.notify.expire`, filed
  under the tenant the notification was in (`pd.RecordErased`), so a check of
  the rows finds nothing the trail says is a row and is not. A receipt is off
  the trail, and goes with nothing said.
- **Streams are told**: once a batch commits, the topics it took from are
  invalidated as an app's policy change invalidates them. A stream that showed
  one queries again and drops it.
- **A receipt never outlives its notification.** `Mark` holds each notification
  it reads until its transaction ends -- a share lock on PostgreSQL, taken in
  identifier order, which the expiry takes its own in too -- so an expiry
  erasing one waits for the receipt being written of it and erases that as
  well, or erases first and `Mark` answers `NotFound`.
- **Run twice it takes nothing twice**: a pass of the same moment finds what the
  other took gone, under a lease or around a failover.

For `forever`, nothing is generated and nothing goes. Either way the app may
remove what it decides to sooner, through its own code; the declaration is the
longest anything is kept.

### A receipt is kept off the trail

A publication is written through the app's generated servers, so it is on the
trail, versioned, and noted to the Watch and the outbox as every write is. A
receipt is not (payday#197). Its record has no RPC; `Mark` writes it through
the ent client, in the transaction it reads and locks its notifications in,
and its kind says `trail: {off: {why: ...}}`. A receipt records that one reader
read one notification. There is one for every first read, so a line of the
trail for each made the trail grow with reading rather than with anything
anybody decided, and kept every person's reading history -- an identifier and
a time per read -- for as long as the trail is kept, which is longer than the
receipt is.

Everything else a receipt is held to stays where it was: the tenant wall, one
receipt per `(notification, reader)` by a unique index, `Mark` atomic and in
request order, and the first read kept under retries and concurrent calls.
`Mark` notes the hint the reader's other sessions are owed in its transaction,
carried in the change itself (`notify.ReceiptMethod`), and the Watch publishes
it once that commits.

What being off the trail costs:

- **No outbox for a receipt's hint.** A publication's change reaches every
  recorder the stack has, the outbox's included, so a hint survives a process
  that stops between the commit and the publish. A receipt's reaches the Watch
  alone. If the process stops there, the reader's other sessions -- on other
  replicas, since this one's went with it -- learn of the receipt when they next
  query: on another hint, or on reconnecting. Until then they show it unread,
  and they are never shown it unread after a query that saw it.
- **The trail says nothing of reading.** Who read a notification, and when, is
  the receipt, and goes when the receipt goes.
- **A check of the rows does not read receipts.** `pd.Digests` reads every kind
  but the ones kept off the trail, and says which those are and why. A receipt
  row added in the database past `Mark` is not found by it. A receipt holds no
  secret and lets nobody in; the most one can do is show a notification as
  read to its reader.

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
between differently encoded messages. Absent Payload and a Payload whose
message is empty are distinct. Keys are compared exactly without trimming or case folding.
Current publication policy is checked even on a retry. The key is kept with its
notification, for `notify.keep` from the publication: a retry within that
returns the same Notification, and one after it publishes again, because both
are gone. So a producer retries one publication for no longer than the app
keeps it. An app whose producers retry for a known time -- a queue replayed
after an outage -- says so as `keep.retry_days`, and `pd gen` refuses a `keep`
shorter than it: a retention made shorter than the retry period is a guarantee
made shorter, refused where it is decided rather than found when a reader sees a
notification twice. With `forever` the key is kept forever. No deletion or
expiry RPC is exposed; the expiry is the deployment's.

An app's domain change and notification publication can use the same database
transaction. Begin the transaction and use the notification server's
`WithDriver` method to join it. This uses payday's existing `enttx.Rebind` and
layer `WithDriver` pattern; sharing a context alone does not share a transaction.
A publication uses the recording path, so its audit, version and change
records commit with it; a receipt is written by `Mark` in the same transaction,
off the trail, with its change hint noted there (see above).

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
- Topic policy cannot be bypassed through ID lookup, Read, raw CRUD,
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
- With an account namespace: an account in no tenant reads what is addressed to
  it as itself; it reads no tenant's notification, no tenant reads its
  namespace, and nobody but an account calling as itself reads there.

This combines recognizable mechanisms with a deliberate storage choice.
[ntfy](https://docs.ntfy.sh/publish/) demonstrates app-defined Topic keys and
subscription; [Novu](https://docs.novu.co/platform/concepts/topics) and
[Knock](https://docs.knock.app/concepts/subscriptions) separate Topic identity
from custom data but fan out to registered subscribers.
[Laravel](https://laravel.com/docs/12.x/notifications) and
[Noticed](https://github.com/excid3/noticed) support recipients beyond a User
model and store per-recipient read state. This contract instead retains one
shared notification and creates a reader's receipt only when it is needed.
