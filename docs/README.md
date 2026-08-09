# Documentation

Two kinds of page. **Guides** are how to do something; **references** are what a
part is and why it is shaped that way. Everything past that is the package
comments, which are where the detail actually lives.

## Guides

| | |
| --- | --- |
| [Permissions and the wall](guide/permissions.md) | who sees what, and where each part of that answer is enforced. **Start here** |
| [Declaring an entity](guide/schema.md) | the options, the field numbers, `list:` and `watch:`, and everything generation refuses |
| [The server](guide/server.md) | the stack, writing a layer, the interceptors, configuration, the commands |
| [The page](guide/client.md) | reads that keep themselves current, writes that need no invalidation rule |
| [Several writes at once](guide/batch.md) | one transaction, and the four rules re-applied per operation |
| [Refusals](guide/errors.md) | a field-level error, from the server to a form field |
| [Testing](guide/testing.md) | the harness, the two seams, and golden files |

## References

| | |
| --- | --- |
| [The runtime](RUNTIME.md) | every package, what is generated, and how each rule is enforced |
| [Tenancy](TENANCY.md) | the model behind the wall, and the decisions around it |
| [The generation contract](SCHEMA.md) | what payday owns, overlays, identifiers, slugs |
| [The browser](CLIENT.md) | the transports, the sandbox, and the client replica |

---

## What payday is

A framework for gRPC applications: declare an entity once, and what follows from
it — the messages, the database schema, the CRUD server, the wall around it, the
paging, the change stream, the TypeScript client — is generated rather than
remembered.

Most of that already existed as a well-made **template**: copy it, and after
copying it is yours. The problem with a template is that a bug fixed on one side
stays on the other. payday is the attempt to take the parts that do not vary from
app to app and make them a dependency, while giving the parts that do vary a
shape they have to fit.

### What it stands on

Most of a framework already exists, and payday deliberately owns very little of
it.

| Layer | What | Where | payday |
| --- | --- | --- | --- |
| contract | `orm.field`, `orm.message` | `protobuf-orm` | **does not touch it** |
| generators | service contracts, messages, ent schema, CRUD servers | `protoc-gen-orm-{go,ent,service}` | adds hooks |
| runtime for generated code | `enttx`, `entpatch`, `entpage` | `protoc-gen-orm-ent/runtime` | uses as-is |
| cross-cutting runtime | `otx`, `xli`, `mkot`, `z` | their own repositories | uses as-is |
| **app schema and runtime** | Tenant/Holder/Audit, `grpcx`, `config`, `frame`, `auth`, `pdid` | **was empty** | **this** |

Not touching `protobuf-orm` matters more than it looks. That schema is published
to a remote registry and pinned by `buf.lock`, so adding one field there means
**waiting for a publish** — which is where the predecessor app actually stalled,
on soft delete. payday defining its own extension in its own proto stays out of
that rhythm.

`protoc-gen-orm-ent` does get changed for payday's needs, but the changes are
kept **payday-unaware** — so they are upstream contributions rather than a fork
to maintain.

## What it costs

Worth reading before adopting it. These are real and none of them are going
away.

**Less of it is readable in your repository.** The best property of the template
payday came from was that every decision was readable next to the code. Now the
gate is a *generated file* in your app — open it and you see delegation rather
than judgement. Three things keep that bounded:

- the runtime holds itself to the same comment discipline; a decision that moved
  did not get summarised on the way
- generated layers stay thin, because the thicker they get the more of the
  reading leaks into generated code
- **the wiring stays in your repository** — the ten lines that stack the
  layers are hand-written on purpose

What is genuinely lost is that you now learn some of this by reading payday
rather than by reading your own app.

**The identity model is fixed.** You can add fields, but `id`, `tenant`, `alias`
and `date_erased` are read by auth and by the wall and cannot change shape. An
app whose *holder* is not "an actor belonging to a tenant" has no reason to use
payday — that is the condition that decides whether this fits at all.

**Enforcement is hard to walk back.** Making an entity declare its tenancy was
the first case of the framework breaking an app's build, and that kind of rule
only accumulates. It is why layer ordering is *not* enforced.

**Two repositories move in step.** Generated code cannot use a runtime that has
not been published yet. Leaving `protobuf-orm` alone, and keeping the generator
changes payday-unaware, is what keeps that cost small.
