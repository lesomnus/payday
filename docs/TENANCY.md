# Tenancy

The model behind the wall: what it is made of, what it cannot express, and the
decisions an app has to make around it.

[The permissions guide](guide/permissions.md) is how to use it. This is why it
is shaped this way, and it is the document to read before designing something
the wall does not obviously cover.

- [1. The wall is two halves](#1-the-wall-is-two-halves)
- [2. The default is the loud one](#2-the-default-is-the-loud-one)
- [3. How many deployments](#3-how-many-deployments)
- [4. Public rows are a projection, not a policy](#4-public-rows-are-a-projection-not-a-policy)
- [5. Adding something to the wall](#5-adding-something-to-the-wall)
- [6. When a credential names a tenant](#6-when-a-credential-names-a-tenant)

## 1. The wall is two halves

Mixing them up is what makes tenancy discussions go in circles.

| | What it knows | Where it comes from | Why there |
| --- | --- | --- | --- |
| **The path** | **how** this entity reaches a tenant — a `tenant` edge, a `tenant_id` column, two hops through `joint → robot → tenant` | the schema, **generated** | a fact about the schema, not something an operator picks |
| **The set** | **which** tenants this caller sees | `gate.Policy`, **injected** | an organisational decision a framework cannot know |

`frame.Narrow(ctx)` answers the set. The generated `<E>Scope(ctx)` uses the path
to turn it into a predicate.

### The path cannot move into policy

To return a predicate you have to return a `predicate.Robot`, and that type is
generated per app per entity. **A "policy" that returns predicates is exactly
`bare.Scope`** — which is already an interface with one method per entity. There
is no escaping the generated world here; the only real choice is whether that
implementation is written or generated.

### And the wall is already injected

`pd.Wall()` is a value handed to `bare.WithScope(...)`. An app can hand
something else. What is generated is the **default** implementation, and the
reason to generate it is one thing: written by hand, the entity you add next
month has no method, so it falls outside the wall, and **nothing breaks**.

`bare.Unscoped` describes that leak in its own comment:

> Embed it and write out the entities there is something to say about; **the rest
> go on seeing every row, and so does an entity added to the schema afterwards.**

## 2. The default is the loud one

An entity that says nothing about its tenancy is `tenanted:` — behind the wall.

Get that wrong and every row disappears, and you know within minutes. Get the
other default wrong and every row is visible to everyone, and nobody finds out.

**An assumption should fail loudly, and the dangerous answer should be the one
somebody had to write down.** `global: {}` exists, and it has to be typed.

## 3. How many deployments

Policy is a mechanism. **How many deployments you run is an attack-surface
decision.** They are not in competition, and conflating them is the usual
mistake.

Take a headquarters admin who should see every tenant:

| | Wall | Policy | Reachable from | A "see everything" path in the public binary |
| --- | --- | --- | --- | --- |
| **A** — one server, policy grants all | on | HQ = all | the internet | **yes** |
| **B** — two deployments, both walled | on | different per deployment | admin is internal-only | **no** |
| **C** — two deployments, admin uses `Ungated` | off for admin | — | admin is internal-only | no, but nothing can be narrowed either |

**B is the one to reach for.** In A, a code path that returns everything
**exists inside the public binary**; whether it runs depends on a credential
check being right everywhere it matters. In B that path is not at that address
at all — a forged credential cannot call code that is not there.

payday already has this shape: `cmd` and `wasm` are two entry points that
assemble the same parts differently, which is the whole reason the wiring is
left visible in an app rather than hidden behind a `Serve(cfg)`.

Two `main`s rather than a flag, for the same reason: **a configuration mistake
must not be able to turn the public server into the admin one.**

```go
// cmd/<app>        gate.Interceptor(nil)          own tenant only. There is no "all" answer
// cmd/<app>-admin  gate.Interceptor(app.Hq{…})    and it refuses anyone who is not HQ
```

And the structural claim becomes a test: one test asserting that *the public
stack does not return another tenant's row even to an HQ credential* is evidence
there is no hole.

### What two deployments drag in

| | What | payday's answer |
| --- | --- | --- |
| `watch.broker` | two processes, so `memory` sees half the writes | **refuses** — `config.WatchConfig` requires a name |
| schema version | upgrade one and the other will not start | **refuses** — `migrate.Check` |
| the outbox drainer | two of them publish twice (not wrong, but wasteful) | your wiring's choice |

## 4. Public rows are a projection, not a policy

Some apps want to publish rows across the wall — a catalogue, a directory, a
public register. `gate.Policy` cannot reach it, and it is worth being precise
about why.

| | What it asks | Shape of the answer |
| --- | --- | --- |
| `Policy.Where` | which tenants does this **caller** see | `frame.Tenants` |
| a public row | who sees this **row** | — |

It is a transpose. **The policy has never seen a row.** However clever it gets,
it cannot say "this one is for everybody".

`global: {}` is not it either: a global entity has no owner and **its writes are
not narrowed**. A public catalogue entry has an owner, and only that owner may
change it.

### The requirement points at its own answer

Making an entity public at the schema level makes **every field** public. The
asset number can be public; the location and the manager cannot. So what is
published is a *different shape of the same row* — and a different shape is a
different message, which is a different RPC.

```proto
service CatalogueService {
  // Public. Asset number and name only, and only rows marked public.
  rpc Search(SearchRequest) returns (SearchResponse);
}
```

- the walled path gets no hole in it
- the projection is explicit, so `location` cannot leak by accident
- "is this row public" is your predicate, in your code

**One condition would change this.** If the public read wants the *same fields
and the same filters* as the private one, then it is the same RPC, and the
schema-level answer is the right one.

## 5. Adding something to the wall

The set a caller sees is `gate.Policy`'s to answer, and an app can widen or
narrow it. What it must not do is reach around the path.

The second axis is the supported way to narrow **within** a tenant — an edge to
a set smaller than a tenant, declared on field 3, with `pd.Grouped(of)` and
`frame.NarrowSet(ctx, of)`. See
[the permissions guide](guide/permissions.md#7-a-second-axis-if-you-need-one).

A predicate applies to **queries**. `Add` is gated in the generated `Gate` layer
instead, because there is no row yet to narrow.

## 6. When a credential names a tenant

A credential can carry a tenant — an mTLS certificate with two SAN URIs, say.
That is a **claim to be disagreed with**, never a way to resolve.

`Identity.TenantId` is read, and then the resolver looks the actor up on its own
and the two are compared. If they disagree, the call is refused:

```
this credential names a tenant that does not hold the actor it names
```

The alternative — using the claimed tenant to scope the lookup — means a
certificate can pick which tenant it is resolved in, which is the wall answering
to the caller.

## See also

- [guide/permissions.md](guide/permissions.md) — how to use all of this
- [SCHEMA.md](SCHEMA.md) — how a path is declared
- [RUNTIME.md](RUNTIME.md) — where each half is enforced
