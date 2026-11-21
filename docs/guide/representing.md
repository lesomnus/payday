# Work at a shared device, or through an agent

A station authenticates with its own credential. An app verifies which person
it may represent. Payday then keeps both identities: `Frame.Actor` is the
person; `Frame.Caller` is the credential holder. A direct call has no `Caller`.
A person identifier in a request is a claim, never proof of delegation.

## Verify a presence

Compose your ordinary holder resolver with `auth.Represent`, then install it in
the usual auth interceptors before `gate.Interceptor(policy)`:

```go
resolver := auth.Represent(holderResolver, func(ctx context.Context, c auth.PresenceCall) (*frame.Frame, error) {
    // App-owned lookup. The row was established by a trusted sign-in or roster
    // verification, and binds this reference to a device and a person.
    presence, err := presences.Verify(ctx, c.Reference, c.Caller.Actor, c.Subject)
    if err != nil {
        return nil, err
    }
    if err := presence.Accept(c.Method, c.Request, c.Recorded); err != nil {
        return nil, err
    }
    person, err := holders.ResolvePresence(ctx, presence)
    if err != nil {
        return nil, err
    }
    person.PresenceExpires = presence.AcceptanceDeadline()
    person.PresenceChanged = presence.Revoked()
    return person, nil
})
```

`presences` and `holders` above are app code. Verification must check the device
binding, person, validity and revocation. For an agent, the same hook verifies an
app-owned delegation rather than physical presence. `Credential` is the verified
device identity and grant; `Caller` is its resolved principal. `Request` is the
read-only protobuf request for a unary call, and nil for a stream. This lets an
app also check a body field such as `session_id` against the named presence.

When a rule needs recent authentication, set `person.Authentication` from the
verified presence's original proof. `c.Caller.Authentication` describes the
device, and `c.Recorded` is a request claim, so neither supplies the person's
proof time. See [requiring a recent sign-in](fresh-authentication.md).

Return `PermissionDenied` for a presence the app refuses, and `auth.ErrUnavailable`
for a verifier that cannot currently answer. A temporary outage leaves queued
work waiting; a refusal stays visible for resolution and does not block other
operators' work. Errors never fall back to acting as the device.

Wire metadata carries `payday-presence` (a non-secret reference),
`payday-subject` (the person's payday ID), and, for queued writes,
`payday-recorded` (RFC3339). The server verifies the subject against the resolver's
answer. It rejects these headers when no representation verifier is installed.
Keep proof and secrets in your trusted store or authentication protocol, not in
these metadata fields, logs, audit references, or the durable client queue.

The verifier can bound a stream with `PresenceExpires` and cancel it immediately
by closing `PresenceChanged`. Credential expiry/revocation also cuts it. Set a
finite deadline when no revocation signal is available; reconnect verifies the
presence again. An already expired or revoked presence cannot open a call.

## Apply both parties' permissions

The policy's `May` and `Where` are asked for the person and the credential holder.
On each call, `gate.Call.Actor` and its row identify the party being checked;
`Caller`, `Subject`, and `Presence` retain the pair. Either refusal rejects the
operation. The resulting tenant scope intersects both answers and the credential
grant. Set membership is intersected the same way through `frame.NarrowSet`.
The verifier cannot widen the device credential by returning a broader grant.

Use the ordinary policy for a station's permitted line or operation and a
person's qualification. Do not implement a policy that unconditionally allows
one party whenever a representation exists.

Writes through the recorder keep the person in `actor_id`, the device or agent
in `caller_id`, its tenant in `caller_tenant_id`, and the reference in `presence`.
The latter three are NULL on a direct call. These recorded facts do not add a
new tenant to the trail's readers. Keep the referenced app presence record if an
investigation needs how and when the person was verified.

## Keep device work through a shift change

The device queue and the person's cache have different lifetimes:

```ts
import { DeviceQueue, Store } from '@lesomnus/payday/store'
import { openQueue, openDisk, deleteOtherDisks } from '@lesomnus/payday/store/idb'
import { Queries } from '@lesomnus/payday/query'

const at = { name: 'factory', identity: deviceIdentity }
const device = new DeviceQueue({ ...at, disk: await openQueue(at) })
await device.hydrate()

// These values come from your app's successful presence establishment.
const representation = await device.represent({ reference: presenceId, subject: personId })
const person = { name: at.name, identity: representation.identity }
const store = Store.open(entities, { ...person, disk: await openDisk(entities, person) })
await store.hydrate()
void deleteOtherDisks(person)
const queries = new Queries(store, deviceTransport, entities, { representation })

await queries.send(JobService.method.finish, { id: jobId, sessionId: presenceId })

// When A leaves, stop A's queries/watches, then drop A's read cache.
queries.close()
await store.forget()
await store.close()
// Keep device. Create a new representation, Store and Queries for B.
```

`JobService.Finish` is an example app RPC. Make queued operations idempotent:
use client-minted IDs for Add, or an app-owned operation ID for custom RPCs.
Delivery may repeat when the server commits and its response is lost.

The cache identity includes the device credential scope, person and presence.
`Queries` checks that its store was opened with that identity. Its wire identity
is still the device's, so the ordinary session identity check continues to work.
Every queued write stores its original presence, person and recording time.
B's query instance can flush A's writes after a reload, but A's responses never
populate B's cache, including the Get used to resolve an Add retry. `writes()`
shows only the current presence's work. A device administration screen may inspect
`device.writes` explicitly; those requests can contain other operators' data.

`deleteOtherDisks` sweeps person caches, leaving the separate device queue alone.
Only device sign-out calls `device.forget()`. Await outstanding sends before
`device.close()`. Use the same device credential scope identity across reloads;
credential rotation or changed scope needs an explicit app decision about any
queue under the old identity. Queue storage contains no credential.

## Decide how late work is accepted

Suppose A records a job at 14:00 while offline, leaves at 14:10, and B reconnects
at 14:20. The queued write still names A's presence and 14:00. The app may accept
an ended presence for that write while refusing a new read or Watch under it.
Implement that distinction in `PresenceCall.Method` and `Request`; the server
imposes no grace period or default session duration. An app can check a recorded
work time against its retained presence interval and an allowed lateness window.
The device-supplied time alone does not prove when physical work happened.

A live stream ends when its presence expires or is revoked. A late unary write
can receive a new acceptance deadline after the verifier checks its policy.
The current operator is never substituted for the original one.

## Require a person to call directly

For an approval, disposal or other action that may not be represented:

```proto
import "payday.proto";

rpc Approve(ApprovalRequest) returns (Approval) {
  option (payday.method) = {direct: {}};
}
```

The transport gate and generated Gate both enforce this before application
policy, including an in-process call from another method. A permissive policy
cannot override it. Hand-written services use `gate.Decide` (or `gate.Direct`)
for in-process dispatch. Keep the generated Gate outside your app layers.
This rejects verified representation; it cannot identify an agent that was
given the person's own credential. Use distinct agent credentials and require
appropriate authentication evidence for sensitive direct calls.
