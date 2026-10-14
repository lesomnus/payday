# Operations across resources

`payday.ResourceService` groups `Batch` and `Watch`. `Batch` accepts the existing
`BatchRequest` and returns `BatchResponse`; it runs the same dispatcher and checks
as the deprecated `BatchService.Do`, which remains registered for existing clients.
`TokenService` remains separate because it concerns credentials.

## Subscribe with CEL

```go
client := pdpb.NewResourceServiceClient(conn)
stream, err := client.Watch(ctx, pdpb.WatchRequest_builder{Filter: `
  has(event.value) && type(event.value) == app.Manifest &&
  event.kind == payday.CHANGE_KIND_CREATED &&
  event.value.artifact_type == "application/vnd.dev.sigstore.bundle.v0.3+json"
`}.Build())
if err != nil { return err }
if _, err := stream.Header(); err != nil { return err }
// The subscription is now installed. List may be used to obtain initial state.
for {
    event, err := stream.Recv()
    if err != nil { return err }
    // Consume the event, deduplicating by event.GetId() when needed.
    _ = event
}
```

An empty filter accepts every authorized event. `event.method` names the operation
that caused the mutation, including an operation inside Batch. `event.call_method`
names the outer RPC, such as `/payday.ResourceService/Batch`. Direct writes use the
generated write method unless their transaction names another origin.

`value` is the mutation's postimage, absent on deletion. `previous` is its preimage,
absent on creation. Both contain the same resource ID and type on update. The
`Any` payload supplies the resource type; CEL unwraps it, so use `type(event.value)
== app.Robot` and guard presence before reading dynamic fields. Relationships in
images contain keys, without expanded related resources.

`id` identifies the event, not the resource. `date_created` is assigned while
recording the change in its write transaction. It is neither the commit time nor
the delivery time, and does not establish a total order across transactions.
Repeated mutations remain separate. A Batch commits before publication, but its
events are independently authorized and filtered; atomic receipt is not promised.

This is a live stream with no initial snapshot, replay cursor or exactly-once
contract. Reconcile state with List after reconnecting. Outbox retries retain the
same ID, timestamp and payload; they do not recover a disconnected subscriber's
history. Existing resource-specific Watch RPCs retain their current-state behavior.

## Wire the server

The generated application template registers both service endpoints. For an
application with custom wiring:

```go
guard := c.Server.Guard(policy)
project := pd.ResourceProjector(db, scope, guard)
resources, err := pd.Resources(stack, driver, guard, w, project)
if err != nil { return err }
pdpb.RegisterResourceServiceServer(grpcServer, resources)
legacy, err := pd.Batch(stack, driver, guard)
if err != nil { return err }
pdpb.RegisterBatchServiceServer(grpcServer, legacy)
```

Pass the same `bare.Scope` used by ordinary reads, including any grouped or custom
scope. `ResourceProjector` applies Get restrictions in the subscription credential and
the current application method policy,
then evaluates that scope against a derived SQL row containing the recorded image.
It does not depend on the current root row existing. Related-resource predicates
use current related rows; removing a required parent denies access. Both sides of
an update must be readable, including a move between tenants.

A custom Get layer that adds read rules or field projections must supply their
historical equivalent by wrapping the projector. `Resources` requires this
projector explicitly. It cannot infer business rules from an arbitrary server
wrapper. Declared secret fields are removed recursively before storage and before
CEL evaluation. Secret SQL columns are absent from the derived row: predicates
that require them fail closed and need an application-specific historical policy.
Collection and JSON columns, and nullable scalars without protobuf presence,
are also omitted from SQL authorization: protobuf cannot preserve their SQL NULL
or unmodeled JSON distinctions. Scopes using those columns require a custom
historical projector. This does not remove these public fields from events or
from CEL. CEL sees exactly the public event delivered to the subscriber.

## Choose which changes to publish

Install one combined recorder in place of the old WatchRecorder/OutboxRecorder
pair, leaving audit recording independent:

```go
selectEvent := func(ctx context.Context, event *pdpb.WatchEvent) (bool, error) {
    return event.GetMethod() != "/app.RobotService/Touch", nil
}
rec := bare.Recorders{
    pd.Recorder(),
    pd.ResourceRecorder(w, c.Watch.Outbox, selectEvent),
}
```

A nil selector selects changes to every resource with a generated Get API,
regardless of its resource-specific `watch` option. Selection happens once inside
the write transaction and controls both paths. A selector error rolls back the
write. There is no client-controlled suppression flag. The combined recorder
constructs and redacts the event once; the outbox stores that exact envelope.
Migrate the database before deploying the new recorder (`Outbox.event` is nullable
for existing rows). Old outbox rows still feed legacy resource watches. Upgrade
all publishers, listeners and drainers before relying on the common event stream:
an old publisher has no images, and an old drainer cannot forward the new envelope.

Install `w.Interceptor()` for ordinary unary RPCs; generated handlers and Batch
commit before returning success. Custom transactions, including retries whose
errors are caught by a handler, must isolate their notifications:

```go
err := w.Transaction(ctx, "/jobs/Import", func(ctx context.Context) error {
    // Begin a transaction, rebind the generated stack to its driver, and pass
    // this context to every write. Roll back on any error.
    // Return nil only after Commit succeeds.
    return runAndCommit(ctx)
})
```

A failed attempt is discarded. A successful attempt joins its enclosing unary
collection, or publishes immediately when called without an interceptor. An
application issuing SQL directly must construct the mutation images and arrange
transactional outbox storage itself; an ORM recorder cannot observe SQL that
bypasses it. Do not call Broker.Publish from inside an uncommitted transaction.

## Bounds and failures

Filters are compiled once per subscription and must return bool. The default
bounds are 16 KiB of expression text, parser depth 64 and 100,000 evaluation cost
units per event. Invalid expressions and dynamic-field evaluation failures return
`InvalidArgument`; evaluation-budget exhaustion and subscriber backlog/connection
loss return `ResourceExhausted`. Evaluation errors do not include resource values.
A disabled broker returns `Unavailable`. Resource envelopes are limited to 4 MiB.

The PostgreSQL broker fragments large public payloads into notifications below
8,000 bytes, sends each publication in a transaction, and reassembles with bounded
memory and a 30-second timeout. It establishes LISTEN before startup returns.
Incomplete or malformed transmissions end subscriptions rather than silently
skipping an event. Publications and queued payload bytes are bounded at 32 MiB.

The outbox drainer uses `watch.ConfirmedBroker` when implemented. The provided
PostgreSQL broker acknowledges notification-transaction commit, allowing failed
sends to be retried without deleting rows. This is broker acceptance, not subscriber
receipt. Legacy custom brokers retain their Publish behavior and must implement
that interface to acknowledge asynchronous sends.

Go contracts live in `pdpb`. TypeScript contracts, including both Batch endpoints,
are exported from `@lesomnus/payday/pdpb`.
