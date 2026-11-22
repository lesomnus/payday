# Route live subscriptions with NATS

Use this when multiple payday replicas serve live clients and broadcasting
every notification to every replica is unnecessary work. Keep PostgreSQL as
your database; NATS carries live changes between servers. Your notification
RPCs, arbitrary topic names, payloads and read policies stay the same.

## Connect the deployment

Link the optional configuration adapter in the app's command package:

```go
import _ "github.com/lesomnus/payday/config/brokernats"
```

Run a Core NATS server and configure every payday replica:

```yaml
watch:
  broker: nats
  dsn: nats://nats:4222
  namespace: robot-production
  outbox: true
  drain_every: 1s
```

Use the same namespace for replicas sharing a database, and a different one
for another environment or application. A namespace is a deployment boundary;
it is unrelated to a notification topic or tenant. Keep client devices on
payday's authorized RPCs. Only trusted application servers connect to these
NATS subjects. Configure NATS accounts, authentication and TLS for your deployment.
URLs may name multiple servers, separated by commas. For credentials or TLS
options beyond a URL, register your own configuration builder:

```go
config.RegisterBroker("secured-nats", func(ctx context.Context, c config.WatchConfig, _ config.DbConfig) (watch.Broker, error) {
    return watchnats.New(ctx, c.Dsn, c.Namespace,
        nats.UserCredentials("/run/secrets/nats.creds"),
        nats.RootCAs("/run/secrets/nats-ca.pem"))
})
```

Import `context`, `github.com/nats-io/nats.go`, and payday's `config`, `watch`,
and `watch/watchnats` packages. Select `broker: secured-nats` for that builder.
Do not replace the adapter's connection/error handlers or enable reconnect
buffering; the adapter owns them to make delivery gaps observable.

For an existing app, regenerate and migrate the database before starting
servers or drainers on this payday. The outbox now retains routes and signal
metadata in nullable columns. New app templates close the Watch at server
shutdown and on failed construction. Existing hand-written servers should
likewise call `changes.Close()` before closing their database. Code using
`watchnats.New` directly owns its returned broker and must close it.

## A Job completes

The app performs its business write and Notification Add in the transaction
shown in the [notification tutorial](notifications.md). The generated recorder
captures the notification's tenant and topic there; publication follows commit.
A rollback publishes nothing. With the outbox enabled, a committed publication
can be retried if the process stops or NATS is unavailable.

Suppose A and C serve readers of Job X, while B serves Job Y. Listen registers
X's interest on A and C. Core NATS sends X's notification to both replicas and
sends no packet to B for X. Each receiving replica interprets the hint once,
then separately runs each listener's current policy. A Robot, its owner and a
Job issuer can all be readers if the app's policy permits them.

An app-wide topic such as `maintenance.eu.*` works the same way. The name is
encoded as an opaque route; `.` and `*` have no wildcard meaning in that name.
Resource-ID topics and named topics remain different even with equal text.
Custom payload fields still belong in the notification payload.

## One reader marks a notification

Mark produces a receipt hint naming that reader. A replica serving only other
readers does not need that receipt hint. Multiple streams for the same reader
share the native subscription, while each stream's permissions remain its own.
No shared cache contains a user's read decision or projected notification.

If a Job's issuer or Robot ownership changes, call NotificationServer.Invalidate
after commit, as in the notification tutorial. Include every topic affected by
the policy change, including the old scope when a relationship moves. This
shared topic hint reaches existing readers, so a revoked stream can end with
PermissionDenied even when no new notification is published.

## Recover after a gap

Wait for Listen's READY, then Read to reconcile. On invalidation, Read again.
On disconnection, Unavailable or ResourceExhausted, reconnect, wait for READY and Read again.
The adapter installs native interests and flushes them before Open returns;
Notification Listen rechecks policy before emitting READY.

This barrier confirms subscription registration on the connected NATS server.
It does not promise instantaneous propagation through a NATS cluster, gateway
or leaf-node topology. Core NATS has no offline replay. Outbox confirmation
means the connected server processed the publication, not that every reader
received it. Applications requiring durable consumer replay need a separate
consumer protocol, such as JetStream; this adapter does not install one.

A transport disconnection closes the old streams, even when the NATS client
reconnects automatically. Slow local listeners are closed individually. A
transport backlog, malformed packet or incomplete fragmented publication ends
the affected replica's streams so that clients reconcile rather than silently
continue with missing state. Failed confirmed publishes leave outbox rows for
retry. If a receipt or invalidation has no durable outbox row, reconciliation
still relies on persistent notification/receipt state and current policy.

## Other Watch APIs and capacity

Generated entity Watch routes by service and resolved IDs. Generic
ResourceService.Watch keeps a broad subscription because arbitrary CEL and
per-reader projection cannot safely be translated into NATS subjects. A replica
with a broad Watch consequently still receives every change in its deployment.
Resource mutation envelopes retain each mutation and its ID; updates to the
same row are not coalesced by the adapter.

Metadata-free custom publishers conservatively reach all potentially interested
subscribers. To gain selective delivery, implement the
[routing contract](../watch-routing.md). PostgreSQL brokers keep broadcast
transport and local filtering; changing to NATS is an explicit deployment choice.

The adapter bounds queued calls at 256 and encoded queued traffic at 32 MiB.
A replica supports at most 8,192 native patterns; identical local interests
share them, and the last local reader releases them. Use NATS monitoring to
observe subscription counts, incoming/outgoing messages, bytes and slow
consumers. Large envelopes use 64 KiB chunks (smaller when the server requires);
`max_payload` must allow at least 1,048 bytes. Missing fragments expire after
30 seconds; reassembly is bounded to 64 incomplete publications and 32 MiB.
