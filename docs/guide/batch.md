# Several writes at once

`payday.BatchService` runs a list of operations as one transaction. They hold or
they fall together.

```proto
service BatchService {
  rpc Do(BatchRequest) returns (BatchResponse);
}

message Op {
  string              method  = 1;   // "/app.RobotService/Add"
  google.protobuf.Any request = 2;
}
```

It is a server RPC rather than a client convenience, so everything that talks to
your app has it — a browser, a CLI, another service.

- [1. Why this does not undo the doctrine](#1-why-this-does-not-undo-the-doctrine)
- [2. Wiring it](#2-wiring-it)
- [3. The guard, and why it is refused rather than defaulted](#3-the-guard-and-why-it-is-refused-rather-than-defaulted)
- [4. Referring to a row you are creating in the same batch](#4-referring-to-a-row-you-are-creating-in-the-same-batch)
- [5. What it does not guarantee](#5-what-it-does-not-guarantee)
- [6. The name stays `payday.BatchService`](#6-the-name-stays-paydaybatchservice)

## 1. Why this does not undo the doctrine

payday's position is that a server exposes the RPC it **means** rather than a
general write, which is why `Patch` and `Apply` are closed by default. A batch
looks like the opposite of that, and the difference is worth being exact about.

What the doctrine is against is an **untyped** write — one where the caller
names a field and a value and the server has no rule about the pair. Every
operation of a batch is a real RPC with its own validation, its own layers and
its own audit. What a batch adds is that they commit together.

The trail and `Watch` come out right for free: the recorder is told each
operation by the name the caller used, and a batch is published as **one** event
— which is correct, since a UI should see a transaction whole.

## 2. Wiring it

`pd gen` writes the dispatcher — it knows every RPC your app has — and
`cmd/serve.go` registers it:

```go
if b, err := pd.Batch(s.Walled, s.Drv, c.Server.Guard(s.Policy)); err == nil {
	pdpb.RegisterBatchServiceServer(g, b)
} else {
	log.From(ctx).WarnContext(ctx, "no batch", slog.String("why", err.Error()))
}
```

`s.Drv` is the driver rather than the `*ent.Client`, because a transaction is
begun on a driver and a client does not hand out the one it holds. That driver
is what the whole stack gets rebound onto — which is why every layer has to
answer `WithDriver`; see [the server guide](server.md#the-one-thing-that-is-easy-to-forget).

## 3. The guard, and why it is refused rather than defaulted

This is the part worth understanding before you turn it on.

payday enforces four things by looking at **the method gRPC dispatched**. A
batch arrives as one method carrying many, so left alone, all four would be
enforced against `BatchService/Do` and none against what is actually being
asked for:

| | What it normally does | What a batch does to it |
| --- | --- | --- |
| `grpcx.Closed` | makes `Patch` and `Apply` unreachable | the outer method is `Do`. **They are smuggled in** |
| `frame.Grant.Allows` | attenuates a credential to a few methods | same |
| `gate.Policy.May` | says whether this actor may do this | authorizes the batch, not the operations |
| `grpcx.Limit` | counts calls | a 1000-op batch counts as one |

A caller whose token allows one method could wrap any method they like in a
batch and have it allowed — silently, with a trail saying it was. That is not a
gap to close later. It is the security model with a hole in it, and the hole is
exactly as wide as the batch RPC is useful.

`batch.Guard` is those four as functions, applied per operation:

```go
type Guard struct {
	Closed  func(method string) bool
	Policy  gate.Policy
	Limiter grpcx.Limiter
	By      func(ctx context.Context, method string) string
	Max     int   // zero is batch.MaxOps, which is 128
}
```

### Build it from the configuration, not by hand

```go
c.Server.Guard(s.Policy)
```

Two places deciding what is closed will eventually disagree, and **the direction
they disagree in is the one where the batch allows more**. So the guard is read
off the same `ServerConfig` the interceptors were built from, and there is no
other supported way to make one.

The policy is the one argument, because it is the one thing that is not in the
configuration: it is a field on the server (`Server.Policy`), and it is the same
value `gate.Interceptor` was given a few lines above. Passing `nil` here while
the interceptor has one is the hole in the third row of the table, left open —
the policy would authorise every call except the ones inside a batch.

The generated `pd.Batch` refuses a guard nobody filled in — `guard.IsZero()`
answers `batch.ErrNoGuard`. That is not a judgement about how open the
deployment is: `ServerConfig.Guard` always sets how a caller is counted, so a
deployment that closes nothing and limits nothing still comes through with
something in it. Only a guard that was never built is zero.

Refused rather than served open, because served open is a privilege escalation
and the way it would be found is somebody reading the wiring and noticing what
is not there. The snippet above logs and serves no batch.

### The limit counts operations

A batch of a thousand operations counts as a thousand. An unguarded batch is
otherwise a way to do a thousand calls' worth of work for the price of one.

`Max` caps the list itself: an unbounded batch is an unbounded transaction, and
that holds locks for a long time.

## 4. Referring to a row you are creating in the same batch

The classic problem: create a tenant, then create a holder inside it — and the
second operation needs the first one's identifier.

The usual answer is a placeholder language (`$0.id`), and placeholder languages
grow.

**payday does not need one.** The minter accepts an identifier the caller
supplies and checks only that its domain is right, and `pdid` is published to
npm. So the client mints both identifiers up front and writes them into both
operations:

```ts
import { create } from '@bufbuild/protobuf'
import { anyPack } from '@bufbuild/protobuf/wkt'
import { pdid } from '@lesomnus/payday'

import { RobotDomain, JointDomain } from './gen/domains.js'
import { RobotAddRequestSchema, JointAddRequestSchema } from './gen/app/robot_svc_pb.js'

const robot = pdid.newId(RobotDomain)
const joint = pdid.newId(JointDomain)

await c.batch.do({
	ops: [
		{
			method: '/app.RobotService/Add',
			request: anyPack(RobotAddRequestSchema, create(RobotAddRequestSchema, {
				id: robot.bytes,
				tenant: { key: { case: 'id', value: tenant.id } },
				alias: 'arm-01',
			})),
		},
		{
			method: '/app.JointService/Add',
			request: anyPack(JointAddRequestSchema, create(JointAddRequestSchema, {
				id: joint.bytes,
				robot: { key: { case: 'id', value: robot.bytes } },
				alias: 'elbow',
			})),
		},
	],
})
```

The domain constants come from `gen/domains.ts`, which `pd gen --ts` writes from
the same schema the Go side reads — the two halves cannot drift into different
numbers.

`anyPack` writes the type URL, and `anypb` in Go resolves it: an `Op` carrying a
message that is not what `method` takes is refused rather than coerced. That
agreement is the one thing about a batch that only a call from TypeScript can
demonstrate, so it has one — `internal/apptest/ts/src/client.test.ts`, which
sends exactly this against the running server and reads the rows back.

The server does not need to know it is in a batch.

This also makes optimistic updates line up: the client already knows the
identifiers, so it can write the rows into the local store before the call
returns, and `Watch` pushes the real state under the same identifiers when it
lands.

## 5. What it does not guarantee

**A batch guarantees atomicity. It does not guarantee that the operations make
sense together.**

Each operation can be individually legitimate and the combination can still
produce a state your app never allowed. If you have a cross-row invariant, you
still write the RPC that means it. The doctrine narrowed; it did not disappear.

## 6. The name stays `payday.BatchService`

Every other payday message is rewritten into your proto package — your app has
`app.Tenant`, not `payday.Tenant`. `BatchService` is the exception, and
deliberately: it is not a message describing your domain, it is a **transport**.
Anything that speaks batch to any payday app speaks the same one, and a generic
client should not need a per-app name for it.

## Where to go next

- [The server](server.md) — the stack a batch rebinds, and where the guard's
  configuration comes from.
- [Permissions and the wall](permissions.md) — the four rules in their normal
  home.
