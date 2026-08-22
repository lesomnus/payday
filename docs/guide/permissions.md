# Permissions and the wall

payday has one rule about who sees what: **a tenant is a wall, and what is
inside one is not visible from outside.**

Everything else in this document narrows that answer. Nothing widens it. There
is no superuser, no role that means "all", and no identifier compared against a
well-known one — a privilege granted by being a particular row cannot be
revoked, cannot be narrowed, and belongs to whoever finds the row.

This is the part of payday that fails quietly when it is wrong, so it is worth
twenty minutes.

---

## 1. What you get by saying nothing

Declare an entity and say nothing about tenancy:

```proto
message Robot {
  bytes         id     = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant        tenant = 2 [(orm.edge) = {immutable: true}];
  string        alias  = 4;

  // Not about tenancy, and not optional either: every entity has to say what
  // `Erase` does to a row, and carrying this field is how "softly" is said.
  google.protobuf.Timestamp date_erased = 14 [(orm.field) = {erased: {}}];

  option (orm.message)   = {rpc: {crud: true}};
  option (payday.entity) = {domain: 7};
}
```

That is a walled entity. Every `Get`, `Patch`, `Apply` and `Erase` of a Robot
carries a predicate narrowing it to the tenants the caller may see, and `Add`
refuses a tenant the caller cannot see. Add a `list:` and the `List` it
generates is narrowed by the same predicate, as is the `Watch` a `watch:` builds
on top of it.

You wrote no Go for that, and there is no `wall.go` to keep up to date. An
entity added to the schema tomorrow arrives with its wall already on.

Silence means the walled answer because the two ways of being wrong are not
alike: a wall assumed where there is none empties the screen and somebody says
so within minutes, while a wall missing where there should be one breaks nothing
and is first noticed by a caller reading another tenant's data. The whole of
that argument is [TENANCY.md §2](../TENANCY.md#2-the-default-is-the-loud-one).
What it costs you here is one line: entities outside the wall all say
`global: {}`, and grepping for it finds every one of them.

The default also cannot be silently wrong:

| the `tenant` edge | |
| --- | --- |
| points at a real Tenant | the predicate is right |
| points elsewhere | generation refuses |
| is not there | generation refuses, saying the path was assumed |

---

## 2. The four things an entity can say

```proto
option (payday.entity) = {
  domain: 7
  // and one of:
  //   (nothing)                     behind the wall by its `tenant` edge
  //   tenanted: {via: "robot.tenant"}   behind it, reached through another row
  //   tenanted: {field: "tenant_id"}    behind it, by a column
  //   tenant: {}                    it *is* the tenant
  //   global: {}                    not behind it at all
};
```

| | what it means | when |
| --- | --- | --- |
| *silence* | `tenanted: {via: "tenant"}` | the ordinary entity |
| `tenanted: {via: …}` | walk edges to reach the tenant | a child row — `Joint` reaching its tenant through `Robot` |
| `tenanted: {field: …}` | a **column**, not an edge | the row has to outlive the tenant it names; see §8 |
| `tenant: {}` | this entity is the wall | the `Tenant` payday ships, and nothing else |
| `global: {}` | outside the wall | shared reference data with no owner |

`tenanted: {field: …}` may name several columns, and then they are **OR**ed: the
row is behind the wall of *any* tenant it names. That exists for exactly one
reason, and the trail is it. An audit row names three tenants and every one of
them is a party to the record — the tenant whose row changed, the tenant whose
operator changed it, and, when a write was about two of them, the counterpart
(§8). It is not a way to share a row between two tenants; payday does not model
that, because there is then no owner and no answer to who may erase it.

---

## 3. Where it is enforced, and why in two places

| | enforced by |
| --- | --- |
| `Get`, `List`, `Watch` | a **predicate** — a `WHERE` on the query |
| `Patch`, `Apply`, `Erase` | a **predicate** — choosing the target *is* the `WHERE` |
| **`Add`** | a **layer** — an `INSERT` has no `WHERE` |

This split is the single most important thing to understand about the wall,
because reasoning about `Add` as if it were narrowed is how a hole gets left.

Reads and writes-to-existing-rows are queries, so the wall goes **in the query**,
generated onto the innermost server:

```go
sink, err := pd.NewSink(db, bare.WithScope(pd.Wall()))
```

Put in front instead it would be an override of four methods per entity, plus
four more for every entity added later — and the one nobody wrote is the one
that leaks.

**Narrowing is not refusing.** A row outside the wall is a row the query did not
match, so the answer is `NotFound`. That it exists is itself something not to
say.

`Add` has nothing to narrow: the row does not exist yet. So the generated `Gate`
layer reads, *through the wall*, before letting the insert through:

```go
func (s gateRobot) Add(ctx context.Context, req *RobotAddRequest) (*Robot, error) {
	if ref := req.GetTenant(); ref != nil {
		if _, err := s.Gate.Next().Tenant().Get(ctx, /* … */); err != nil {
			// NotFound, for the same reason as above
		}
	}

	if ref := req.GetSite(); ref != nil {
		// … and the same read, when the app declared a set at field 3; see §7
	}

	return s.RobotServiceServer.Add(ctx, req)
}
```

Without it, the identifier in `tenant` becomes a foreign key with nothing
consulted: the row is invisible to whoever planted it and visible to whoever
holds that tenant. That is the shape of the bug, not a mitigation of it.

### What the gate reads, and what it does not

| edge on the new row | read by the gate |
| --- | --- |
| the **first hop** of the row's path to its tenant | **yes**, through the wall |
| field 3, the set (§7) | **yes**, for the same reason one level down |
| any other edge | **no** |

The first hop is the `tenant` edge for the ordinary entity, and for one that
reaches its tenant through another row it is that row. Reading it through the
wall answers the whole path at once, however long the path is: a hop the caller
may not see comes back `NotFound`, and the question was only ever "may this
caller see the thing it says this row belongs to". Field 3 joins it because
payday reads field 3 as an isolation boundary too, so an `Add` into a set the
caller cannot see is the same bug one level down.

Ordinary edges are **not** read. Pointing at somebody else's row is a
referential-integrity question rather than a tenancy one, and asking it would
cost a read per edge on every write.

Which leaves the row with a **second** path to a tenant: the gate does not
compare the two, and an entity that needs them to agree says so with `agrees:`,
which is written up in
[the schema guide](schema.md#agrees--when-a-second-edge-also-reaches-a-tenant).

---

## 4. Who is asking

Two steps, kept apart because they change for different reasons.

```
credential ──[ auth.Handler ]──> Identity ──[ auth.Resolver ]──> frame.Frame
              reads the request   a claim     asks the database   who it is served as
```

**`auth.Handler`** reads whatever the transport carries and says who the caller
*claims* to be. Three come with payday:

| | where the name comes from |
| --- | --- |
| `auth.Plain` | a header, believed as-is — **development and tests only** |
| `auth.MTLS` | the certificate the connection was made with |
| `auth.Bearer` | a token, exchanged for a name via a `TokenStore` you supply |

`auth.Seq(a, b)` takes the first claim any of them finds. A handler that finds
*nothing* is passed over; one that finds something **wrong**, or that cannot
tell, stops the search — so `Seq(Bearer(store), MTLS())` means "the token if
there is one, otherwise the certificate", and never "the token, and if anything
goes wrong, the certificate".

**`auth.Resolver`** is **yours to write.** Looking up "who is `@acme/admin`" is
a query against your own generated servers, whose types payday cannot name. It
answers with the frame the request is served as — and what it answers comes from
the database, never from the caller.

```go
srv := grpc.NewServer(auth.Interceptor(handler, resolver, auth.PublicDefault)...)
```

`PublicDefault` is health and reflection: served to anybody, because neither says
anything about what is inside.

---

## 5. What a caller may see

`gate.Policy` is the seam for everything payday does not decide.

```go
type Policy interface {
	May(ctx context.Context, c Call) error              // may this call happen at all
	Where(ctx context.Context, c Call) (frame.Tenants, error)  // which tenants may it see
}
```

It is asked **once per call**, by `gate.Interceptor`, and the answer lands on
`Frame.Scope` where everything behind reads it. Asking from inside the wall
would ask several times per request — a `Get` reads a row and then an edge, an
`Apply` reads, writes and reads back.

```go
chain = chain.With(gate.Interceptor(s.Policy))   // behind auth, whose frame it reads the actor from
```

`Server.Policy` is where an app sets it, and `cmd/serve.go` hands the same value
to `c.Server.Guard` as well: the interceptor covers the calls gRPC dispatches
and the guard covers the operations inside a batch, which arrive as one method
carrying many. A policy in only the first place authorises `BatchService/Do`
rather than what it asks for; see
[batch](batch.md#3-the-guard-and-why-it-is-refused-rather-than-defaulted).

**A nil policy is not a missing piece.** Without one:

```go
return frame.Only(f.Tenant), nil   // everybody sees their own tenant, and nobody sees more
```

### Cross-tenant access — the headquarters admin

An operator who may see several tenants is a `Where` that answers with several.
An operator who may see all of them answers `frame.Everything`. Both are the
deployment saying so, in code it wrote, rather than a row somebody found.

"Headquarters, but acting as customer X right now" comes free: issue the session
a `frame.Grant` narrowed to X (§6), and the wall does the rest. The audit trail
still records the concrete tenant, which is what an admin scope of `*` would
have thrown away.

### What the deployment does for itself

Some work has to go around the wall — putting the first tenant in, before there
is anyone to be inside one — and payday answers it with a **server instance
built without the wall**, a thing somebody can be handed and have taken away,
rather than with a privilege anybody holds; what each instance is for, and what
the ungated one still keeps, is
[the server guide](server.md#2-the-two-servers-and-why-there-are-two).

---

## 6. What a credential allows

A caller's authority is one question; what the credential in their hand allows
of it is another. `frame.Grant` is the second.

```go
frame.Whole()                                  // narrows nothing
frame.Whole().In(tenantA)                      // this tenant only
frame.Whole().To("/app.RobotService/Get")      // this method only
frame.Whole().To("/app.RobotService/*")        // every method of that service
frame.Whole().In(tenantA).Within(siteX)        // and this site only — see §7
```

Three axes, each narrowed or not. It is deliberately **not** a map of one to the
other — "write here, read there" — because a permission set that varies per
resource is a policy, and a policy is not something a credential should carry
around.

A method is written the way gRPC knows it, and any of its three parts may be
`*`. **A whole part or nothing**: `/app.*/Get` is a pattern and `*Get*` is a
method name that happens to contain asterisks, because a permission check
nobody can evaluate by reading it is one nobody reviews.

Two rules hold everywhere:

- **It only ever narrows.** A token saying "every tenant", held by somebody who
  may see one, still sees one. The credential is met with what the policy
  decided, and the meet is the answer.
- **The zero value allows nothing.** A store that answers with a `Grant` it
  forgot to fill in hands out a credential that can do nothing — noticed
  immediately. The other way round it hands out one that can do everything —
  noticed by nobody.

Only `Bearer` can carry one, because only a token has anywhere to put it. A
header and a certificate name somebody and stop, so `Plain` and `MTLS` answer
`frame.Whole()` and say so.

**payday does not mint credentials.** Who may have one, and on what evidence,
is a decision about an organisation rather than about a server — a password
policy, a provider, a second factor, how long a session lasts. Something else
issues them and `auth` reads them.

For an external identity provider that is [`auth/authoidc`](../../auth/authoidc):
it verifies the token against the issuer's key set and hands the claims to your
`Resolver`. For credentials a deployment issues itself, it is `auth.Bearer` over
a `TokenStore` your app writes, and for a browser it is a session cookie — see
[putting a login in front of an app](signing-in.md).

---

## 7. A second axis, if you need one

Field 3 of every entity is left open **for you**: a set smaller than a tenant —
a site, a cell, a region. payday fixes the number and the shape and will never
spend it.

```proto
message Robot {
  bytes         id     = 1 [/* … */];
  Tenant        tenant = 2 [(orm.edge) = {immutable: true}];
  Site          site   = 3 [(orm.edge) = {nullable: true}];   // ← yours
  string        alias  = 4;
}
```

`Site` is an ordinary entity of your app (`pd entity add --tenanted Site`).
Declaring the edge is the whole declaration — the number is what payday reads,
not the name.

Then a second predicate is generated, one per entity, **including the ones in no
set**:

```go
bare.WithScope(bare.Scopes{pd.Wall(), pd.Grouped(of)})
```

`of` answers which sites this caller may see — a membership table, so it is
yours. It may be `nil`.

It is generated rather than left to you for one reason: written by hand you
would embed `bare.Unscoped` and write only the entities you have something to
say about, and then **an entity added later is not narrowed and nothing says
so.** A second axis with a hole in it is worse than none, because the first one
looks like it is doing the work.

Three things to know:

- **One hop.** A row is in the set its own field 3 names. There is no walk along
  edges the way `via:` has — the tenant is reachable from everything by
  construction and a set is not. Child rows that belong to a set carry their own
  field 3.
- **Both axes apply.** `pd.Grouped` goes *beside* the wall, never instead of it.
  Two tenants may name a site each, so narrowing only by the site is how a
  credential reaches into the other tenant.
- **A nullable field 3 means rows in no set**, and those are invisible to a
  read narrowed to one. That is fail-closed and right — a row nobody put in a
  site is not in every site — but if you add field 3 to a schema that already
  has rows, nobody with a site-scoped credential sees them until you backfill.

The credential narrows here too. `Grant.Within(site)` is what a key hung off a
site needs: such a key is issued where the work is, outlives whoever issued it,
and must not be usable anywhere else — which the tenant axis cannot say, since
every site of a tenant is inside that tenant.

`pd.Grouped` reaches `of` through `frame.NarrowSet`, which meets the two. Do not
call `of` yourself and skip it: `of` answers about the *actor*, and an app that
answers that correctly and never thinks about the credential hands out
site-scoped keys that reach every site, with nothing anywhere saying so.

---

## 8. The audit trail

Every write that changed something writes a trail row, **inside the transaction
that changed it**, so the trail and the data hold or fall together — a write the
recorder could not account for is undone with it. Nothing writes one by hand.
The RPCs exist because the trail is an entity like any other and a test is
plainer for having them, and the layer in front answers every one of them that
writes with `Unimplemented`:

```
the trail is written by what happened, not by anybody asking
```

That layer is on both stacks, the ungated one included. A trail somebody can
edit is evidence of nothing.

What it records: who acted, which tenant held them, what changed, which tenant
held *that*, the RPC the caller asked for, the patch, and the row as it was when
the event was over.

It is filed under the **object's** tenant, not the actor's. A headquarters
operator changing a customer's row leaves a record the customer can read, and
the actor's tenant is a second column so headquarters can read it too — neither
needs a scope wide enough to see the other. A write with two sides — a row
moving from one tenant to another, filed under where it ended up — names the
other party through `audit.Concerning`, and the wall counts that column as well.
It is set server-side and is never a field of a request: a caller who could name
it could grant a stranger a read.

All three are **columns and not edges**, which is the one place in payday's own
schema that is true. A trail row has to outlive the tenant it names, and an edge
is a foreign key: it would mean either that a tenant can never be erased, or
that erasing one erases the record of what was done in it.

**A hard erase is the one exception to the filing.** The row is gone before the
recorder runs, so there is no object tenant left to read: the record goes under
the *actor's* tenant, which is the last thing known about it, and `value` is
empty. A soft erase keeps its row, so its record is filed like every other write
and `value` is the whole of that row with the erase stamp on — everything of the
moment before, plus what the erase wrote: the stamp, and the version column
beside it for an entity that carries one, since an erase is a write like any
other.

A field declared `secret` is **not** in it. The layer that clears those on the
way out is in front of the sink and the recorder is behind it — deliberately, so
a row is recorded as it was written rather than as somebody was allowed to see
it. That is right for every column but a verifier, which in the trail would be a
second copy of itself, in the one table nothing erases.

Reads go through `AuditService.List`, filtered by object, actor, object tenant,
actor tenant, or counterpart tenant.

### What the trail is not

**It records writes.** A read leaves nothing here, and that is not an omission
to fix by widening it: `object_id` is the thing that changed, `patch` is the
delta and `value` is the state, and a `List` has none of the three. Reads also
outnumber writes by orders of magnitude, and mixing them turns "what happened to
this row" into a scan of the one table that never stops growing.

Auditing reads is a real requirement in some deployments — who *looked at* a
record is the event that matters when the harm is disclosure rather than change.
Every system that does it keeps it separate: Kubernetes' audit is a log with a
level per rule, and its recommended policy records reads as metadata only, never
the response; CloudTrail splits management events from data events and bills
them apart.

So if you need it, it is a second stream and not this table. What makes it an
audit record is not where it is stored but three properties:

| | |
| --- | --- |
| it is not gated by a level somebody turns off | **payday's**, and it is why `authenticated` is `Info` |
| it is not dropped under backpressure | **yours** — a synchronous processor and an exporter that blocks |
| its subject cannot delete it | **yours** — where the pipeline lands |

The second is a configuration and not a limitation of logs. OTel's default
processor batches and may drop; a simple one exports each record and waits.
`otel:` is the OpenTelemetry Collector's own configuration language inlined, and
payday adds only what your file did not name — so a deployment that describes a
synchronous pipeline gets that, unmodified.

payday's part is the one you cannot configure your way to: an `authenticated`
line for **every call it serves**, at a level a deployment keeps — who, from
which credential type, in which tenant. That is the read-shaped event most
deployments want, and the delivery guarantee is yours to choose.

---

## 9. Erasure

Every entity has to say what `Erase` does to a row: a field marked `erased:` for
soft, `erase: {hard: {}}` for hard, and silence is refused. The table and why it
is a refusal are in
[the schema guide](schema.md#erasure--required-one-way-or-the-other). What the
choice costs *here* is the trail.

Soft keeps the row, so the erasure is filed under the tenant that held it and
its `value` is that row as the erase left it — which is the bill as much as the
benefit: the trail outlives what it names, so what a softly erased row held is
still readable there, and an obligation to destroy data is answered by a
retention policy rather than by the erase. Hard leaves nothing to read, so the
record goes under the actor's tenant with an empty `value` (§8), and what the
row held survives only in the trail of the writes before it.

---

## 10. Whose names these are

`Tenant`, `Holder`, `Audit` and `Outbox` are payday's entities and they land in
**your** proto package: `pd gen` copies the files in and rewrites their
`package` line, so a caller of your app says `/app.TenantService/Get` beside
`/app.RobotService/Get` and never has to learn the name of the framework to say
who they are. Only the transports keep the `payday.` name, because a transport
describes no domain and has nothing per app in it to rename. What the rewrite
covers, which names survive it, and what two apps sharing a proto package are
claiming about their schemas, is
[the generation contract](../SCHEMA.md#3-whose-names-these-are).

---

## 11. What payday does not do

- **No superuser.** Nothing anywhere compares an identifier against a well-known
  one and answers "everything".
- **No roles, no per-resource ACLs.** `gate.Policy` is where those go if you want
  them. payday supplies the wall and the seam; the rule that "admins may erase
  Robots" is yours.
- **No credential minting.** Something else issues them; `auth` reads them.
- **No IdP.** payday reads credentials and enforces them. OIDC, SAML, LDAP,
  magic links — all of that produces the credential `auth` then reads.

---

## 12. Putting it together

```go
// The innermost servers: one behind the wall, one not.
walled,  err := pd.NewSink(db, bare.WithMinter(pd.Minter()),
	bare.WithRecorder(bare.Recorders{pd.Recorder(), pd.WatchRecorder(w)}),
	bare.WithScope(bare.Scopes{pd.Wall(), pd.Grouped(sitesOf)}))
ungated, err := pd.NewSink(db, bare.WithMinter(pd.Minter()), /* … */)

// The stack a caller reaches. Gate is outermost, so nothing behind it asks again.
stacked, err := app.Build(walled.WithWatch(w), pd.AuditBuild(), pd.GateBuild())

// And what the deployment works through: no wall, no gate.
own, err := app.Build(ungated.WithWatch(w), pd.AuditBuild())

// Who is calling comes first: everything after it reads the frame.
h := auth.Seq(auth.Bearer(sessions), auth.MTLS())
chain := grpcx.Serving(ctx).
	WithUnary(auth.InterceptorUnary(h, resolver, auth.PublicDefault)).
	WithStream(auth.InterceptorStream(h, resolver, auth.PublicDefault)).
	With(gate.Interceptor(policy))

g := grpc.NewServer(chain.ServerOptions()...)
app.RegisterServer(g, stacked)
```

Read top to bottom, that is the whole of it: who is asking (`auth`), what they
may see (`gate.Policy`), what their credential allows of it (`frame.Grant`), and
the predicate that carries the answer into every query (`pd.Wall`).

---

## 13. Checklist

Before a deployment is reachable by anyone:

- [ ] `auth.Plain` is not wired, or is wired only where everyone who can reach it
      is already trusted to tell the truth. It believes whatever it is told.
- [ ] Every entity that is **not** behind the wall says `global: {}`. Grep for
      it and read the list.
- [ ] The server a caller reaches was built with `bare.WithScope(pd.Wall())`,
      and the ungated one is not the one being served.
- [ ] `gate.Interceptor` is installed **behind** `auth.Interceptor` — it reads
      the actor.
- [ ] If you inject a policy, it went to **both** `gate.Interceptor` and
      `c.Server.Guard`. The second is the operations inside a batch, which are
      not the method gRPC dispatched.
- [ ] Credentials your issuer mints have a `Grant` you meant. The zero value
      allows nothing; `frame.Whole()` narrows nothing.
- [ ] If you declared field 3, `pd.Grouped` is in the `bare.Scopes` beside the
      wall.
- [ ] An entity whose second edge must land in the same tenant as its first says
      `agrees:`. The gate does not ask that question and cannot.

---

## Where to go next

| | |
| --- | --- |
| [schema.md](schema.md) | declaring the entity all of this is generated from |
| [server.md](server.md) | the stack each of these rules sits in |
| [client.md](client.md) | reads, writes and the store a page sees this through |
| [signing-in.md](signing-in.md) | putting a login in front of it |
| [TENANCY.md](../TENANCY.md) | the model behind the wall, and deploying per tenant |
| [SCHEMA.md](../SCHEMA.md) | what the schema owns, field 3, identifiers, slugs |
| [RUNTIME.md](../RUNTIME.md) | every package, and how each rule is enforced |
