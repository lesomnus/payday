# Adding notifications to an app

This guide adds a named announcement Topic, then a Job Topic whose readers are
chosen by application relationships. The app supplies a policy and publishes
when its domain work completes. Payday stores one notification, creates receipts
only when readers mark it, and manages live subscriptions.

The [reference](../notify.md) describes identity, paging, idempotency, and the
wire contract. The snippets below use `app` for your generated Protobuf package
and `pd` for its generated `server/pd` package, as the server guide does.

## 1. Opt in and generate

On the file declaring your app package, choose two unused entity domains:

```proto
option (payday.app) = {
  notify: {notification: 40, receipt: 41}
};
```

These numbers are examples, not reserved notification domains. Each must be
unused in your app, in `7` to `247`, and different from the other. Choose them
once: existing identifiers retain their domains. You do not need a `past`
declaration to use notifications.

A global app adds `global: true` inside `notify`. Its requests omit tenant IDs,
its private records have no Tenant relationship, and its policy receives
`pdid.Nil` for the tenant argument. Generated servers fix this mode from the
declaration; setting `notify.Options.Global` cannot override it. Authentication
and persistent reader identity are still required. See the
[reader-state contract](../notify-adoption-contract.md) for global app setup.

Use versions of both the payday Go module and its Buf dependency that include
`App.notify`. For an existing app pinned to older options, run `buf dep update`
to refresh `buf.lock` before generating; `pd gen` preserves an existing pin.

Regenerate, including the client when the app has one:

```sh
go tool pd gen --ts .
```

Run your app's migration command before serving the new schema. The public
service is in your package: callers use `/app.NotificationService/Read`, with
your package in place of `app`.

## 2. Define a named Topic policy

A Topic chooses an interest; it does not name its readers. Here the app allows
authenticated readers in a tenant to read `maintenance` and `allhands`, and
allows one configured identity to publish to them. Another app can use different
rules without changing notification storage or stream handling.

The policy uses these imports:

```go
import (
    "context"
    "fmt"

    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/status"

    "github.com/lesomnus/payday/gate"
    "github.com/lesomnus/payday/notify"
    "github.com/lesomnus/payday/pdid"
)
```

```go
type announcementPolicy struct {
    publisher pdid.Id
}

func (p announcementPolicy) Publisher(ctx context.Context, tenant pdid.Id, topic notify.Topic) (string, error) {
    f, err := gate.Actor(ctx)
    if err != nil {
        return "", err
    }
    if f.Tenant != tenant || f.Actor.IsZero() || f.Actor != p.publisher || !announcement(topic) {
        return "", status.Error(codes.PermissionDenied, "cannot publish this announcement")
    }
    return f.Actor.String(), nil
}

func (p announcementPolicy) Reader(ctx context.Context, tenant pdid.Id) (pdid.Id, error) {
    f, err := gate.Actor(ctx)
    if err != nil {
        return pdid.Nil, err
    }
    if f.Tenant != tenant || f.Actor.IsZero() {
        return pdid.Nil, status.Error(codes.FailedPrecondition, "a persistent reader is required")
    }
    return f.Actor, nil
}

func (p announcementPolicy) Read(ctx context.Context, tenant, reader pdid.Id, topic notify.Topic) error {
    if !announcement(topic) {
        return status.Error(codes.PermissionDenied, "cannot read this topic")
    }
    return nil
}

func announcement(topic notify.Topic) bool {
    return topic.Resource.IsZero() && (topic.Name == "maintenance" || topic.Name == "allhands")
}
```

The runtime checks tenant scope before invoking Topic policy. This example's
additional `f.Tenant` check deliberately permits only a caller's own tenant.
Returning a reader ID is an authorization decision: do not copy an arbitrary
request header into it. To let an owner act as a Robot, establish and validate
that delegation in authentication or in `Reader`, then return the Robot's ID.

Construct `policy := announcementPolicy{publisher: publisherID}` with the app's
configured publishing identity. If your policy permits revoking reader status
while a stream is open, `Reader` must consult that current app state as well as
the authenticated frame. An app policy invalidation reruns these methods; it
does not replace their application-specific checks.

The producer returned by `Publisher` must stay stable across retries and server
replicas. A service principal can return another stable key after authorizing
the call; publication does not require that principal to own a receipt.

Topic names, Kind, idempotency keys, and producer keys must be valid UTF-8
without NUL. Ordinary policy errors are returned as a sanitized
`PermissionDenied`. Return an explicit gRPC status with a client-safe message
when callers need to distinguish an operational failure from access denial.

## 3. Register the service

Use the same stack, driver, and Watch as the rest of the app:

```go
notifications, err := pd.Notifications(stack, drv, w, policy, notify.Options{
    CursorKey: cursorKey,
})
if err != nil {
    return err
}
app.RegisterNotificationServiceServer(grpcServer, notifications)
```

Here `stack` is the app's server stack, `drv` its database driver, `w` its
configured Watch, and `policy` the implementation above. Load `cursorKey` from
the app's configuration: it is a secret of at least 32 bytes, shared by all
replicas and preserved across restarts. Rotating it invalidates old page cursors.

Keep the normal authentication and method-policy interceptors in front of this
service. For a policy that lists allowed RPCs, add only the notification methods
each caller needs. A caller allowed to read does not thereby gain permission to
publish; the notification policy still makes its own publishing decision.

Restrict Audit RPCs to separately authorized operators in that method policy,
and keep Outbox storage accessible only to deployment components. `NotificationService` Topic and reader checks do not filter audit
or outbox provenance records, which can include publication and reader metadata.

## 4. Publish one announcement

The service's `Add` method publishes. The following is an internal server call
using an authenticated `ctx`, `notifications` from the previous section, and a
known tenant ID:

```go
topic := new(app.NotificationTopic)
topic.SetName("maintenance")

notification, err := notifications.Add(ctx, app.NotificationAddRequest_builder{
    TenantId:       tenant.Bytes(),
    Topic:          topic,
    Kind:           "maintenance.scheduled",
    IdempotencyKey: "maintenance-2026-10-25",
}.Build())
if err != nil {
    return err
}
fmt.Printf("published %x\n", notification.GetId())
```

The key identifies this publication from the server-established producer in this
tenant. Retrying the same request returns the same notification. A different
Topic, Kind, Source, or encoded Payload under that key is `AlreadyExists`.

Add `Payload` when the client needs custom data. Pack a Protobuf message with
`anypb.New` and keep the resulting `Any` when retrying. Payload is independent of
the Topic key and does not create another subscription scope.

Validate domain-specific Kind and Payload combinations in the publishing code.
`Publisher` authorizes a Topic; it does not inspect the request body. If external
producers can call `Add`, put that validation in the app's service wrapper before
forwarding to `notifications.Add`.

Publishing does not enumerate readers or create receipts. A tenant with ten
readers and a tenant with ten thousand readers both add one notification. Calling
`Read` also creates no receipt; only `Mark` records a reader's first read time.

## 5. Read and mark from a client

Create the generated client on an authenticated connection:

```go
client := app.NewNotificationServiceClient(conn)
topic := new(app.NotificationTopic)
topic.SetName("maintenance")

page, err := client.Read(ctx, app.NotificationReadRequest_builder{
    TenantId: tenant.Bytes(),
    Topics:   []*app.NotificationTopic{topic},
    Size:     50,
}.Build())
if err != nil {
    return err
}

for _, item := range page.GetItems() {
    fmt.Printf("%s unread=%t\n", item.GetNotification().GetKind(), item.GetReceipt() == nil)
}
```

`Read` returns a notification and this reader's receipt together. `Get` and
`List` return shared notification data, without reader state. To continue a page,
put the response's `next` in a new request's `cursor` and preserve its tenant and
Topic set. Start from an empty cursor when reconciling a changed list.

Mark a displayed notification by its own ID, not its Topic ID:

```go
if len(page.GetItems()) == 0 {
    return nil
}
selected := page.GetItems()[0].GetNotification()
marked, err := client.Mark(ctx, app.NotificationMarkRequest_builder{
    TenantId: tenant.Bytes(),
    Refs: []*app.NotificationRef{app.NotificationRef_builder{Id: selected.GetId()}.Build()},
}.Build())
if err != nil {
    return err
}
fmt.Printf("marked %x\n", marked.GetItems()[0].GetNotificationId())
```

The server chooses the receipt owner and `data_read`. Repeated and concurrent
calls preserve the first read time. Cache reader projections separately from
the shared notification. A delayed response with no receipt must not clear a
receipt already observed for the same tenant, reader, and notification.

`Mark` accepts 1–100 distinct explicit references and returns receipts in that
order. It creates all missing receipts in one transaction or none, preserving
existing receipt IDs and first-read times on retries. An inaccessible reference
rejects the whole batch; a notification arriving during the call is not marked.

### Unread pages and badges

Filter before paging rather than discarding read rows from a client page:

```go
filter := app.NotificationReadFilter_builder{
    Kinds: []string{"job.completed"},
    UnreadOnly: true,
}.Build()
page, err := client.Read(ctx, app.NotificationReadRequest_builder{
    TenantId: tenant.Bytes(),
    Topics: []*app.NotificationTopic{topic},
    Filter: filter,
}.Build())
if err != nil {
    return err
}
counts, err := client.Count(ctx, app.NotificationCountRequest_builder{
    TenantId: tenant.Bytes(),
    Topics: []*app.NotificationTopic{topic},
    Filter: filter,
}.Build())
if err != nil {
    return err
}
fmt.Printf("%d unread notifications for %d resources\n", counts.GetCount(), counts.GetSourceCount())
```

`source_ids` can further narrow the filter. Empty selector lists mean all;
the kind, source, unread and authorized Topic predicates combine with AND.
Counts cover the complete selection, including per-Topic zeros. `source_count`
deduplicates nonempty source IDs across Topics; source-less notifications count
only toward `count`. Read and Count have independent database snapshots, so
reconcile both after invalidation. A conversation's own read-through cursor is
application state, not a notification page cursor.

## 6. Keep two clients current

Open `Listen` before the initial query and wait for its first `ready` response:

```go
stream, err := client.Listen(ctx, app.NotificationListenRequest_builder{
    TenantId: tenant.Bytes(),
    Topics:   []*app.NotificationTopic{topic},
}.Build())
if err != nil {
    return err
}
first, err := stream.Recv()
if err != nil {
    return err
}
if first.GetReady() == nil {
    return status.Error(codes.Internal, "listen did not start with ready")
}
reader := first.GetReady().GetReaderId()
fmt.Printf("reader %x\n", reader)
```

Keep the stream open while the initial `Read` runs. An `invalidated` response is
a reason to query again, not the new notification data. Do not discard a pending
hint because a query is in flight. A concurrent receiver can set a dirty flag
and query again after the current query finishes; a sequential receiver can
consume its queued hints afterward. Coalescing pending hints into one refresh is
safe.

Try the flow with two connections authenticated as the same reader:

1. Both subscribe, observe `ready`, and call `Read`.
2. The first client calls `Mark` for an unread notification.
3. The second client receives an invalidation and calls `Read` again.
4. Both see the same receipt ID and first read time.

Repeat with a third connection authenticated as another reader. Its receipt
remains absent until that reader marks the notification. The first reader's
receipt changes are not sent to the third connection.

After a dropped connection, reconnect, wait for the new `ready`, and query again.
There is no replay cursor on `Listen`. A full subscription backlog terminates the
stream rather than silently dropping invalidations. A reader identity change
also ends the stream: clear the previous reader's projection before opening the
new subscription.

For a browser client generated by `pd gen --ts`, the service descriptor lives in
`app/payday/notification_pb.js`. This example takes the page's authenticated
Connect transport, tenant ID bytes, render callback, and cancellation signal:

Use a binary transport when clients must preserve unknown Payload types. A JSON
transport needs a type registry or the explicit fallback described in the reference.

```ts
import { createClient, type Transport } from '@connectrpc/connect'
import {
  NotificationService,
  type NotificationReadResponse,
  type NotificationReceipt,
} from '../gen/app/payday/notification_pb.js'

export async function followMaintenance(
  transport: Transport,
  tenantId: Uint8Array,
  render: (page: NotificationReadResponse) => void,
  signal: AbortSignal,
): Promise<void> {
  const client = createClient(NotificationService, transport)
  const topics = [{ key: { case: 'name' as const, value: 'maintenance' } }]
  const known = new Map<string, NotificationReceipt>()
  const key = (id: Uint8Array) => Array.from(id).join(',')
  let reader: string | undefined

  for await (const response of client.listen({ tenantId, topics }, { signal })) {
    if (response.event.case === 'ready') {
      reader = key(response.event.value.readerId)
      known.clear()
    } else if (response.event.case !== 'invalidated') {
      continue
    }
    const page = await client.read({ tenantId, topics, size: 50 }, { signal })
    if (reader === undefined || key(page.readerId) !== reader) {
      throw new Error('notification reader changed; reopen the subscription')
    }
    for (const item of page.items) {
      if (!item.notification) continue
      const id = key(item.notification.id)
      if (item.receipt) known.set(id, item.receipt)
      else item.receipt = known.get(id)
    }
    render(page)
  }
}
```

This reads the first page again for each hint. The stream stays open and queued
hints are consumed after each query. A UI can coalesce them when it needs fewer
queries. If the UI also calls `mark`, put its returned receipt into the same
reader-scoped cache before accepting another query response. Handle a recoverable
stream error by reconnecting and refreshing; on `PermissionDenied`, remove the
old subscription's visible data. The generated client exposes `read`, `mark`,
and `listen`; no generic query-store Watch is needed for this flow.

## 7. Authorize Job Topics from app relationships

For a Job Topic, the app's read rule can use the Robot that processed it, that
Robot's owner, and the Job's issuer. Those are application relationships, not
fields supplied by the notification feature.

For example, define the lookup result explicitly:

```go
type jobReaders struct {
    robot  pdid.Id
    owner  pdid.Id
    issuer pdid.Id
}

type jobPolicy struct {
    announcementPolicy
    db *ent.Client
}

func (p jobPolicy) WithDriver(drv dialect.Driver) (notify.Policy, error) {
    p.db = ent.NewClient(ent.Driver(drv))
    return p, nil
}

func (p jobPolicy) Read(ctx context.Context, tenant, reader pdid.Id, topic notify.Topic) error {
    if topic.Resource.IsZero() {
        return p.announcementPolicy.Read(ctx, tenant, reader, topic)
    }
    allowed, err := p.lookup(ctx, tenant, topic.Resource)
    if err != nil {
        return err
    }
    if reader == allowed.robot || reader == allowed.owner || reader == allowed.issuer {
        return nil
    }
    return status.Error(codes.PermissionDenied, "not a reader of this job")
}

func (p jobPolicy) Publisher(ctx context.Context, tenant pdid.Id, topic notify.Topic) (string, error) {
    if topic.Resource.IsZero() {
        return p.announcementPolicy.Publisher(ctx, tenant, topic)
    }
    actor, err := p.Reader(ctx, tenant)
    if err != nil {
        return "", err
    }
    allowed, err := p.lookup(ctx, tenant, topic.Resource)
    if err != nil {
        return "", err
    }
    if actor != allowed.robot && actor != p.publisher {
        return "", status.Error(codes.PermissionDenied, "cannot publish this job completion")
    }
    return actor.String(), nil
}
```

The `lookup` method is your tenant-scoped domain query. It must establish that
the ID names a Job and load its Robot, that Robot's owner, and its issuer. The
sample app declares `robot_id` and `issuer_id` on
[Job](../../internal/apptest/proto/app/job.proto), and nullable `owner_id` on
[Robot](../../internal/apptest/proto/app/robot.proto). With those fields, the
lookup is:

```go
func (p jobPolicy) lookup(ctx context.Context, tenant, resource pdid.Id) (jobReaders, error) {
    deny := status.Error(codes.PermissionDenied, "cannot read this job")
    if resource.Domain() != pd.JobDomain {
        return jobReaders{}, deny
    }
    job, err := p.db.Job.Query().Where(
        entjob.IdEQ(resource.Uuid()), entjob.TenantIdEQ(tenant.Uuid()),
    ).Only(ctx)
    if ent.IsNotFound(err) {
        return jobReaders{}, deny
    }
    if err != nil {
        return jobReaders{}, status.Error(codes.Unavailable, "cannot load job readers")
    }
    readers := jobReaders{robot: pdid.Id(job.RobotId), issuer: pdid.Id(job.IssuerId)}
    robot, err := p.db.Robot.Query().Where(
        entrobot.IdEQ(job.RobotId), entrobot.TenantIdEQ(tenant.Uuid()),
    ).Only(ctx)
    if err != nil && !ent.IsNotFound(err) {
        return jobReaders{}, status.Error(codes.Unavailable, "cannot load robot ownership")
    }
    if err == nil && robot.OwnerId != nil {
        readers.owner = pdid.Id(*robot.OwnerId)
    }
    return readers, nil
}
```

Here `ent`, `entjob`, and `entrobot` are the app's generated ent client and its
`job` and `robot` predicate packages. Import `dialect` from
`github.com/protobuf-orm/ent/dialect` for `WithDriver`. Construct the policy with
`jobPolicy{announcementPolicy: announcementPolicy{publisher: publisherID}, db: db}`.
The domain check dispatches the Job rule; both queries also narrow by tenant.
Another app supplies its own relationships rather than treating every resource
ID as a Job.

The `WithDriver` method implements `notify.PolicyBinder`. The runtime calls it
automatically at construction and when entering a publication or receipt
transaction. It rebuilds these lookups on that transaction's driver, making a
new Job or changed owner visible before commit. Keep the lookup on `p.db`;
closing over the original client would defeat that rebinding. The pure named
announcement policy needs no `WithDriver` method.

For the sample app, the additional imports are:

```go
import (
    "github.com/protobuf-orm/ent/dialect"

    "github.com/lesomnus/payday/internal/apptest/internal/ent"
    entjob "github.com/lesomnus/payday/internal/apptest/internal/ent/job"
    entrobot "github.com/lesomnus/payday/internal/apptest/internal/ent/robot"
)
```

An app of your own uses the corresponding paths in its own module.

This publishing rule permits the processing Robot and the configured publisher.
Choose the rule that matches who can finish a Job in your app. When the Job
finishes, publish to its resource Topic:

```go
jobTopic := new(app.NotificationTopic)
jobTopic.SetResourceId(jobID.Bytes())

done, err := notifications.Add(ctx, app.NotificationAddRequest_builder{
    TenantId:       tenant.Bytes(),
    Topic:          jobTopic,
    Kind:           "job.completed",
    SourceId:       jobID.Bytes(),
    IdempotencyKey: completionKey,
}.Build())
if err != nil {
    return err
}
fmt.Printf("published job completion %x\n", done.GetId())
```

`completionKey` identifies this one domain transition and is reused when retrying
it. If a Job can complete more than once, each completion needs a distinct key.

The `Reader` method above uses the authenticated actor as the receipt owner. If
the app authenticates a Robot or explicitly delegates to one, it returns that
Robot's ID. The owner and issuer can instead read under their own IDs and keep
their own independent read state.

## 8. Publish with a domain transaction and invalidate policy changes

A Job completion and its notification should commit together. Begin the domain
transaction, rebind the app stack to its transaction driver, and construct
the notification server on that driver before calling `Add`. The generated
server provides `WithDriver` for this:

```go
func completeAndNotify(
    ctx context.Context,
    drv dialect.Driver,
    changes *watch.Watch,
    method string,
    notifications *pd.NotificationServer,
    complete func(context.Context, dialect.Driver) error,
    publication *app.NotificationAddRequest,
) error {
    return changes.Transaction(ctx, method, func(ctx context.Context) error {
        txDriver, tx, err := dialect.BeginTx(ctx, drv)
        if err != nil {
            return err
        }
        defer tx.Rollback()

        if err := complete(ctx, txDriver); err != nil {
            return err
        }
        txNotifications, err := notifications.WithDriver(txDriver)
        if err != nil {
            return err
        }
        if _, err := txNotifications.Add(ctx, publication); err != nil {
            return err
        }
        return tx.Commit()
    })
}
```

The `complete` callback is your domain operation, using `txDriver` to rebind its
own app stack. `method` identifies that operation, and `changes` is the app's
shared Watch. Import `github.com/protobuf-orm/ent/dialect` and
`github.com/lesomnus/payday/watch` for this example. `Transaction` holds immediate
change hints until the outer commit also succeeds, including work initiated
outside an RPC. Keep the existing audit and change recorders installed. Passing
the same context to an independently constructed service does not join its
database transaction.

Ownership and membership changes can alter visibility without publishing a new
notification. After committing such a change, invalidate the affected Topic:

```go
if err := notifications.Invalidate(ctx, tenant, []notify.Topic{notify.Resource(jobID)}); err != nil {
    return err
}
```

This makes idle subscriptions recheck their reader identity and access. Check
the error and retry a failed invalidation; the domain change has already committed.
This is app code because the framework cannot discover arbitrary dependencies
inside a policy function.

A subscription that loses access ends with `PermissionDenied`; discard cached
data in that subscription scope. To demonstrate this for Job Topics, change the
owner, invalidate the Job Topic after commit, and confirm that the old owner's
stream ends while the new owner can subscribe and query the retained notification.

Use a shared broker when the app runs more than one replica. A notification
created on one replica and a policy invalidation from another must reach the
replica holding the reader's stream. `Listen` remains a live hint mechanism;
durable state and current policy are consulted again after reconnecting.

## 9. Run the examples against the sample app

The sample app's
[notification tests](../../internal/apptest/cmd/notify_test.go) exercise actual
generated RPCs and storage, including the Job/Robot relationships above:

```sh
go test ./notify
go test -C internal/apptest ./cmd -run '^TestNotify' -count=1
```

These cover one publication without eager receipts, independent reader state,
explicit Topic paging and cursors, access through every public path, a business
transaction that commits or rolls back with its notification, and live creation,
receipt, and access-revocation signals. `TestNotifyPolicyReadsTheBusinessTransaction`
also changes a Job's issuer and checks receipt authorization in that same
uncommitted transaction, exercising the policy binder.

Run the same integration cases on PostgreSQL, including two app replicas sharing
the broker:

```sh
./scripts/with-postgres.sh go test -C internal/apptest ./cmd -run '^TestNotify' -count=1
```

`TestNotifyCrossReplicaListen` requires `PDTEST_POSTGRES` and skips without it;
the script supplies a database or uses the one that variable already names.
