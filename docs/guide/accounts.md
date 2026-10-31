# One sign-in, many tenants: accounts

In payday a holder is who a request is from, and a holder is inside one tenant.
Some apps have people who sign in once and belong to many tenants: an account of
an identity provider's directory, with a membership -- a holder -- in each
tenant it is in. A session names the account, a call is served as one of its
memberships, and a few calls are the account's own, made before it is anybody's
member or across all of its tenants at once.

This is how to declare that, and what payday then does about it (payday#184).
An app whose people are each one tenant's needs none of it, and an app that says
nothing never sees it.

- [1. Say which holder field names the account](#1-say-which-holder-field-names-the-account)
- [2. Resolve a call to a membership](#2-resolve-a-call-to-a-membership)
- [3. A method an account calls as itself](#3-a-method-an-account-calls-as-itself)
- [4. What the records say](#4-what-the-records-say)
- [5. Testing it](#5-testing-it)
- [What payday does not do](#what-payday-does-not-do)

## What payday keeps, and what it does not

payday keeps the **reference** from a membership to its account, the **frame**
of a call an account makes as itself, and **resolving** a call to one
membership. It does not keep the account. Its names, its addresses, how it signs
in and whether it may are the identity provider's -- roster's, for an app on the
same stack -- and a copy of them in every app is one more place a person has to
be erased from. Here an account is an identifier and nothing else.

## 1. Say which holder field names the account

The field is the app's, on payday's Holder, added with an overlay at a number
of the app's own -- so an app that kept the reference before payday did keeps its
column:

```proto
// proto/ext/payday/holder.ext.proto
message Holder {
  // The account this holder is a membership of: one of the identity
  // provider's directory. Null on a holder that is no account's.
  bytes account = 9 [(orm.field) = {type: TYPE_UUID, nullable: true}];

  // Every call an account makes is resolved by it.
  option (orm.message) = {
    indexes: [{name: "by_account", refs: [{name: "account", number: 9}, {name: "tenant", number: 2}]}]
  };
}
```

and named on the file that says the package is the app's, by both its name and
its number:

```proto
// proto/app/app.proto
option (payday.app) = {accounts: {field: {name: "account", number: 9}}};
```

`pd gen` refuses a declaration that would resolve a call by the wrong column, or
by none:

| | refused because |
| --- | --- |
| a name the number does not have, or only one of the two | a field renamed under the declaration would resolve calls by another column, with nothing failing |
| a field that is not `bytes` of `TYPE_UUID` | an account is named by its identifier |
| an edge | an account is not a row of this app's |
| the holder's own key | that names the membership, not the account |
| a field declared `personal` or `secret` | it is an identifier, as an edge is, and which account a membership was moved to is the record |

It warns of a field no index begins with: every call an account makes is then a
read of every holder there is.

What it writes, in `server/pd`:

| | |
| --- | --- |
| `pd.Memberships(client)` | an account's memberships, out of the deployment's own client: what the resolver resolves a call by, and what an HTTP endpoint asks for the tenants an account is in |
| `pd.HolderAccount(holder)` | the account a holder is a membership of, as the message carries it |
| `pd.AccountResolver(...)` | the resolver, [below](#2-resolve-a-call-to-a-membership) |
| `pd.AccountMethods`, `pd.AccountMethod(m)` | the methods an account calls as itself, [below](#3-a-method-an-account-calls-as-itself) |
| `pd.Reads`, `pd.AccountReads(...)` | what such a method declares it reads |

### Which account a membership is of is the deployment's to say

The generated gate refuses a request that sets the field -- an `Add`, each item
of an `AddMany`, a `Patch` that sets or clears it, an `Apply` whose document
writes it -- with `InvalidArgument` naming the field:

```
account: is the account this membership is of, and is written by the deployment through the server it holds, never by a request
```

It is what a call is resolved by. A request that could set it could make a holder
of its tenant somebody else's membership -- served as that account whenever it
names the tenant, and its "only membership" no longer its only one. One that
could move it could take over a membership another account holds. So a
membership is made where the account agreed to it -- an invitation accepted, a
workspace an account made -- by the app's own code, through the server the wall
was never installed on:

```go
_, err := s.Ungated.Holder().Add(ctx, app.HolderAddRequest_builder{
	Tenant:  app.TenantRef_builder{Id: tenant.Bytes()}.Build(),
	Alias:   alias,
	Account: account.Bytes(),
}.Build())
```

A layer below the gate writes through the servers below it and is not asked.

## 2. Resolve a call to a membership

```go
r := pd.AccountResolver(client, "acme-workspace", namesAnAccount, Resolver(s.Ungated))

chain := grpcx.Serving(ctx, ...).
	WithUnary(auth.InterceptorUnary(handler, r, auth.PublicDefault)).
	WithStream(auth.InterceptorStream(handler, r, auth.PublicDefault)).
	...
```

It goes in front of the resolver the app already has, and answers like this:

- **A credential `namesAnAccount` says is an account's** -- its `Id` is the
  account's identifier -- is served as the account's membership of the tenant
  the call names in the header, or its only one. Which credentials those are is
  the app's to say: payday keeps no session, so it is what the app's sessions
  are.
- **Every other credential** is the resolver behind's, as it was before there
  were accounts.
- **The membership** is asked of the resolver behind too, as `Identity{Id: <the
  holder>, TenantId: <its tenant>}` with the rest of the credential as it came:
  the frame is the one the app's own resolver answers for that holder, with the
  account put on it (`frame.Frame.Account`), so a layer knows which account a
  membership's call came from without reading the holder again. An answer for
  anybody else is refused.

### The tenant a call names

The header's name is the app's -- lower case, as gRPC writes a key -- and its value
is the tenant's alias, with or without its `@`, or its identifier:

| the call | is served as |
| --- | --- |
| names a tenant the account is in | its membership there |
| names a tenant it is not in, **or one there is not** | refused, `PermissionDenied`: `this account is not in @hooli` |
| names none, and the account is in one | that one |
| names none, and the account is in several | refused, `InvalidArgument`: name one, in the header |
| names none, and the account is in none | refused, `FailedPrecondition` |
| names two, or what no tenant could be | refused, `InvalidArgument` |

A tenant the account is not in is **refused, never re-pointed**: the call is not
served as one of the account's memberships somewhere else. And it is one answer
whether or not the tenant exists, so the header is no way to find out which
tenants there are.

A page sends it on every call, with an interceptor of its transport:

```ts
const workspace: Interceptor = (next) => async (req) => {
	req.header.set("acme-workspace", current.alias)
	return next(req)
}
```

### A credential bound to one tenant

One whose `auth.Identity.TenantId` names a tenant -- a session handed to that
tenant's own domain, say -- is good there alone: it is served as the account's
membership of that tenant, a header naming another is refused rather than
followed, and a method an account calls as itself is refused to it, since that
call is outside every tenant.

### Asked outside an RPC

What an account is in, asked before there is a call to serve -- an HTTP endpoint
beside the RPCs, a page drawn before a tenant is chosen -- is the generated
`Memberships`, on the deployment's own client:

```go
ms, err := pd.Memberships(client).Of(ctx, account, account.Selector{}, 0)
```

Hand it nothing a caller reaches: what it answers is every tenant's.

## 3. A method an account calls as itself

A method an account calls before it is anybody's member, or across all of its
tenants at once -- the invitations addressed to it, the cards it pays with -- is
marked so in the schema, on an RPC the app declares:

```proto
// proto/ext/app/invitation_svc.ext.proto
service InvitationService {
  rpc Accept(InvitationAcceptRequest) returns (Invitation) {
    option (payday.method) = {account: {}};
  }
}
```

It is served as the account whatever the call names: a frame with the account
where a holder would be -- in `Actor` as well as `Account` -- and no tenant
(`frame.OfAccount`). `pd gen` refuses the option in an app that declares no
accounts, and an empty `(payday.method)`.

A method the contract is generated with cannot carry it -- an overlay may not
redeclare one -- and that is deliberate: a generated verb is a tenant's rows, and
what an account does outside every tenant is an act of its own, with a name.

### It reaches its own methods and no others

A frame of an account is refused every method but the ones it was made for --
the ones the schema marks -- in two places, so that a resolver that made one for
a method it should not have is refused anyway:

- `gate.Decide`, which the gate's interceptor and a batch's guard ask, refuses
  it before a policy is asked;
- the generated gate refuses it again on every method of every entity's
  service, by the method the caller asked for (`gate.AsAccount`).

A policy is still asked about the methods it may call, and may refuse them --
`gate.Call.AsAccount` and `gate.Call.Account` say what the call is, and
`Call.Row` is nil. `Where` is not asked: such a call sees no tenant, whatever a
policy would have said.

### It reads nothing it did not declare

There is no tenant to narrow by, so the wall answers every entity with nothing
for such a call -- **global ones too**: a row behind no wall is not therefore an
account's. What the method reads, the layer serving it says, before it reads:

```go
func (s invitations) Accept(ctx context.Context, req *app.InvitationAcceptRequest) (*app.Invitation, error) {
	ctx, err := pd.AccountReads(ctx, app.InvitationService_Accept_FullMethodName, pd.Reads{
		Invitation: func(ctx context.Context, account pdid.Id) (predicate.Invitation, error) {
			return invitation.Account(account.Uuid()), nil
		},
	})
	if err != nil {
		return nil, err
	}

	v, err := s.Next().Invitation().Get(ctx, app.InvitationGetRequest_builder{Ref: req.GetRef()}.Build())
	...
}
```

A predicate per entity, given the account, as the wall's are; the entities it
does not name, it reads none of. A declaration can be made again in the same
call -- the holders first, and then the tenants they are in, by the identifiers
those holders hold -- and it applies to a call framed as an account and to
nothing else: a membership reads its tenant's rows whatever its context carries.

A row is read with whatever its edges point at, as under a tenant's frame, so
declare the rows and select of them what the method answers with.

`pd doctor` reports a method an account calls as itself whose layer declares
nothing -- found by the call of `pd.AccountReads` that names it. A method that
reads nothing through the walled servers says so with an empty `pd.Reads{}`, and
the doctor is satisfied.

### What it writes

Through the walled servers a `Patch`, an `Apply` and an `Erase` reach the rows it
declared, and nothing else. An `Add` has no row to narrow, and a layer beneath
the gate is not asked about what it adds -- so what the method writes is the
layer's to hold to what the account may do, as anything a layer writes is. A row
that is the account's own and no tenant's -- a card it pays with -- written
under its frame is on the trail as the account's write.

Such a row has no wall of its own. Declared `global`, it is behind none for
anybody: a membership's call reads it through its generated methods as readily
as the account's own does. Keep those methods closed to every role in your
policy, so that what reads and writes it is the account's own methods, which
read what they declare.

What an account's answer makes **in a tenant** -- the membership an accepted
invitation is -- is written there as the deployment, through the server it
holds, with a frame of that tenant, so that the trail and the past file it in
the tenant it lands in:

```go
in := frame.Into(ctx, frame.New(pdid.Nil, tenant, frame.Whole()).WithScope(frame.Only(tenant)))
_, err := s.Ungated.Holder().Add(in, ...)
```

The past needs it: an event is filed under a tenant, and `past.Begin` refuses a
caller of none.

### Counted against the account

`gate.ByTenant`, the rate limit's key, counts such a call against the account
rather than against no tenant -- one account asking often is not every
account's limit.

## 4. What the records say

- **The access record** names the account as who made a call it makes as itself,
  since there is no holder to name: the row's `actor_id` is the account and its
  `actor_tenant_id` is Nil, which no membership's row has -- a holder is always
  some tenant's -- so a review tells the two apart by that column. Whose data it
  carried is filed as anybody's is: under the tenant the row it was in is held
  by, and what was in no tenant's row under none, the deployment's own record. A
  membership's call names the membership, as it always did.
- **The trail** names it the same way for a write made through the walled servers
  under such a frame.
- **The log** line `authenticated` carries `actor.account` for every call of an
  account, as its membership or as itself.

## 5. Testing it

A frame by hand, for a call made directly against a server:

```go
ctx = frame.Into(ctx, frame.OfAccount(account, frame.Whole(), pd.AccountMethod))
ctx = batch.AsOp(ctx, app.InvitationService_Accept_FullMethodName) // the method a caller asked for
```

The reference app has the whole of it, on SQLite and PostgreSQL:
`internal/apptest/cmd/account_test.go` -- the resolver over the wire, every
method of every service called with an account's frame, the reads with nothing
declared and with something, the access record, and the gate's refusal of the
field.

## What payday does not do

- **Keep the account.** It is the identity provider's.
- **Say how a session names an account.** Sessions stay the app's
  (`namesAnAccount` above); payday has no session of its own.
- **Accounts of two identity providers.** An account is one identifier of one
  directory: a `pdid`, which is what a directory built on payday issues.
- **A wall of an account's own.** An entity whose rows are an account's rather
  than a tenant's is declared `global`, and its generated methods are the
  policy's to close; there is no tenancy that narrows a membership's reads to
  its account's rows.

## Moving an app that built this itself

[Migrating](../migrating.md#accounts-for-an-app-whose-holders-are-memberships-of-one)
has the steps.
