# Refuse a write using its previous row

Use admission when a write is already permitted but the row's state makes it
invalid: a released revision cannot change, an author cannot approve their own
work, or an operation cannot be recorded at this station. Reads still use the
wall and read policy. This is opt-in, per named entity.

## Install it on the sink

Implement `admission.Policy`, or use `admission.Func`. Here `app.Revision` is an
application entity with a `released` boolean:

```go
rules := admission.Func(func(ctx context.Context, c admission.Call) error {
    if before, ok := c.Previous.(*app.Revision); ok && before.GetReleased() {
        return status.Error(codes.PermissionDenied, "released revisions are immutable")
    }
    return nil
})

sink, err := pd.NewSink(client,
    bare.WithMinter(pd.Minter()),
    bare.WithRecorder(pd.Recorder(content)),
    pd.Admission(rules, admission.Options{
        Entities: []string{"app.Revision"},
        Timeout: time.Second,
    }),
)
if err != nil {
    return err
}

// Install admission once on the shared client. Copy the sink for its wall;
// creating another admission option on the same client installs another guard.
walled := sink
if err := bare.WithScope(pd.Wall())(&walled.Server); err != nil {
    return err
}
served, err := app.Build(walled, pd.AuditBuild(), pd.SecretBuild(), pd.GateBuild())
```

Keep the app's existing recorders and layers. A copied sink retains admission,
and `WithDriver` retains both its policy and ent guard when a layer joins a
transaction. Do not replace the gate or wall with admission: admission can
refuse what reached it, and cannot reopen a request either already refused.

`Entities` uses full protobuf names. `app.Job`, `app.*`, `*.Job`, and `*.*` name
one entity, one package, that name in any package, and all eligible entities.
Each `*` occupies a whole component, as role method patterns do. Empty and
unmatched patterns fail startup. Payday's internal recording tables, including
Audit, Outbox, Event, Fact and notification storage, are excluded; their writes
are consequences of operations and must not recursively require a decision.

## Capture the original RPC

Add `pd.AdmissionUnary` to the existing gRPC unary interceptor chain, after
identity and method authorization and before the handler. It copies the outer
request before app layers normalize it or turn it into several writes:

```go
unary = append(unary, pd.AdmissionUnary)
g := grpc.NewServer(grpc.ChainUnaryInterceptor(unary...))
app.RegisterServer(g, served)
```

For a custom in-process operation, or a message of a client stream, capture it
at its application boundary:

```go
ctx = bare.Origin(ctx, app.JobService_Complete_FullMethodName, request)
// Inner generated writes now carry Complete and its request as their origin.
```

Without this capture, a generated write still supplies its own request. Its
`Method` comes from gRPC when available and otherwise from its own operation;
`By` always names the generated operation that changed this row. Capture the
outer request when a rule depends on a custom RPC's inputs. Stream handlers
choose which incoming message constitutes an operation rather than sharing a
mutable last-message variable between writes.

## What the policy sees

The sequence is:

1. Authentication establishes the frame; method authorization and the generated
   gate reject disallowed calls. The wall narrows the row and ordinary server
   checks validate the write.
2. Inside the transaction, the server locks and captures the previous row,
   performs the SQL, and captures the result. PostgreSQL holds the row lock
   until completion; SQLite serializes writes at the database level.
3. Admission receives the original request, previous and resulting images,
   entity, row identifier, outer method, generated operation and frame.
4. A refusal rolls back the whole operation. Acceptance lets the recorder and
   transaction commit continue. An accepted policy decision alone is not proof
   that a later recorder or commit succeeded.

`Previous` is nil for Add, and `Value` is nil for Erase. A no-op does not ask
policy. AddMany asks once per row, in request order, with the complete batch as
its original request. Any refusal undoes the batch. A caller-owned transaction
is marked refused too: ignoring a failed inner call cannot commit it.

`Frame.Actor` identifies the represented person; `Frame.Caller`, when present,
is the verified device or agent. Both have already passed authorization, or
only the person has when `Frame.Delegated` says the agent acts by a delegation
the verifier checked. See
[Work at a shared device](representing.md) for presence verification and late
queued writes. Admission adds the rule about this operation's row and station.

All messages and authority slices are copies. Declared secrets are removed
recursively, including from holder/device rows, Add/Patch requests and
references. Unknown protobuf fields, encoded patch contents and `Any.value`
are withheld. Compare `Previous` with `Value` for an Apply's resulting changes;
do not depend on a serialized patch to expose values to policy.

For custom RPC messages, declare additional secret fields at installation:

```go
Secrets: map[protoreflect.FullName][]protoreflect.FieldNumber{
    "app.CompleteRequest": {7}, // an application-defined proof
},
```

Unknown message names and field numbers fail startup. Declare an entire opaque
bytes or JSON field if it can contain secrets. These declarations add to the
schema's secret fields. The decision context carries the sanitized frame,
cancellation and deadline; it withholds other context values and gRPC metadata,
including bearer credentials. Use the Call's explicit data in your adapter.

## Three application rules

**Released revisions.** The first example refuses Patch, Apply and Erase when
the previous revision is released. Creating a new revision has no previous row
and remains possible. The row lock makes the release check and change part of
one transaction.

**Approval by somebody else.** In an app whose Job records `author_id` and
`approved`, detect the transition to approved:

```go
before, old := c.Previous.(*app.Job)
after, next := c.Value.(*app.Job)
if old && next && !before.GetApproved() && after.GetApproved() {
    if bytes.Equal(before.GetAuthorId(), c.Frame.Actor.Bytes()) {
        return status.Error(codes.PermissionDenied, "an author cannot approve their own work")
    }
    // The app's relationship client uses its own server credential.
    allowed, err := relations.Approver(ctx, c.Frame.Actor, c.Object)
    if err != nil {
        return status.Error(codes.Unavailable, "approval policy is unavailable")
    }
    if !allowed {
        return status.Error(codes.PermissionDenied, "the job names another approver")
    }
}
```

Here `relations.Approver` is an app adapter, for example to its own membership
store or a relationship engine. It must use this context's deadline. An external
relationship read is not in the SQL transaction; applications needing a stronger
cross-system guarantee must choose a consistency protocol or local fact they
can verify atomically.

**Qualified person at a qualified station.** For a new operation record, check
both `c.Frame.Actor` and `c.Frame.Caller.Actor` against the operation and line in
`c.Value`. Require `Caller != nil` when the act must be recorded by a station.
The app decides whether a queued operation uses current qualifications or
verified historical facts. A timestamp or presence reference supplied by the
client does not establish either by itself.

## Deadlines, records and trusted writes

The default deadline is two seconds, limited further by the caller's deadline.
A timeout, panic or unavailable policy refuses the write. At most 32 policy
calls run per installed checker by default; `Limit` changes that bound. A
policy ignoring cancellation retains its slot until it returns, and exhaustion
refuses new decisions. Give network clients their own timeouts too. Policies
must avoid side effects: accepted writes can still fail afterwards.

A policy that is asked about some methods and not others says which, with
`Asked`, so that a policy that is down holds up only what it was asked about:

```go
asked := map[string]bool{"/app.RevisionService/Release": true, app.RevisionService_Patch_FullMethodName: true}
pd.Admission(rules, admission.Options{
    Entities: []string{"app.Revision"},
    Asked:    func(method string) bool { return asked[method] },
})
```

It is asked with the call's outer method, `Call.Method`: the RPC the transport
captured (`pd.AdmissionUnary`, or `bare.Origin`), or, where none was, the
generated RPC that wrote -- the same string the policy would have been given. A
call it answers false for is admitted at once: the policy is not called,
nothing is copied for it, and it takes no slot, while a policy that hangs holds
every slot and refuses everything else it is asked about. It is otherwise a
call the policy let through: its write is counted against the guard on direct
ent writes, and a transaction another write already had refused stays refused.
`Asked` runs first and unbounded, inside the write's transaction: keep it a
lookup, never a call. Nil asks about every call.

Refusals produce a structured `admission refused` log with entity, object,
method, operation, actor, caller when represented, and result code. Request
values and policy error text are omitted. Configure `Options.Log` to route
these records to the deployment's log sink. No Audit row is inserted for the
refused change, since its transaction rolls back.

The shared ent client and its clones reject protected direct mutations without
a kept transaction. An unaccounted mutation inside one prevents commit, even
if followed by an admitted write of the same entity. Start shared transactions
with `dialect.BeginTx` and rebind the stack with `enttx.Rebind`. Ordinary
`ent.Tx` transactions lack this completion ledger and are refused while
admission is enabled.

For deployment-owned bootstrap or maintenance, make the bypass explicit:

```go
_, err := sink.Revision().Add(bare.TrustAdmission(ctx), initialRevision)
```

This bypasses admission and its ent guard; normal recording still runs. Never
select it from caller metadata or expose that sink as a public service. Newly
constructed ent clients and raw SQL are outside the installed guard. Keep those
capabilities in trusted deployment code.
