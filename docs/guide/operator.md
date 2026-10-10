# What the operator keeps about a tenant

Some of what an app knows about a tenant is not the tenant's to change: its
plan, its contract, how long its history is kept, a quota, a legal hold. The
tenant should be able to **read** it — a settings page that says *your plan
keeps two years of history* — and only the deployment's operator should be able
to **write** it.

This page is where to put that, and how the trail's retention reads it.

- [1. An entity of its own](#1-an-entity-of-its-own)
- [2. Who reads it, and who writes it](#2-who-reads-it-and-who-writes-it)
- [3. The trail asks it](#3-the-trail-asks-it)
- [4. When a window shortens](#4-when-a-window-shortens)

## 1. An entity of its own

Declare an entity for it, tenanted, with a row per tenant:

```sh
go tool pd entity add --tenanted Contract .
```

```proto
message Contract {
  // ...what `pd entity add` wrote: the key, the tenant, the timestamps.

  // How many days this tenant's history exists at all.
  uint32 history_days = 8;

  // From when, so that a change can be dated rather than applied on save.
  google.protobuf.Timestamp date_effective = 9;
}
```

Not fields on `Tenant`, for two reasons.

**A tenant can write its own row.** Its name, its alias and its labels are its
own to change, so an operator's field beside them turns the rule into a check
on *which fields this patch touches* — on every path that patches a tenant, in
a batch as well as alone. A path that forgets is a tenant extending its own
contract. An entity of its own makes the rule one about **an RPC**, which is a
line in your policy and nothing else.

**A contract has a time dimension.** A plan changes on the first of the month,
a downgrade has a grace period, a regulated customer's obligation ends on a
date. A field on `Tenant` holds what is true now and the trail holds what it
used to be; an entity can hold the rows that say from when, which is what the
questions above ask.

And it grows without touching anything else: a quota, a hold, a feature that
only some plans have is a field here, or an entity beside it.

## 2. Who reads it, and who writes it

`--tenanted` puts it behind the wall, so a tenant's caller reads its own
contract and nobody else's — the same rule as everything else of theirs, with
nothing written for it.

What the wall does not decide is **who may call what**. That is your
`gate.Policy` — see [what a caller may see](permissions.md#5-what-a-caller-may-see)
— and the rule is two lines of it:

- the tenant's roles may call `ContractService/Get` and `List`;
- **no** tenant role may call `Add`, `Patch` or `Erase`.

The writes go through the operator's path: a command at a shell on the
ungated server, or a console whose callers are the deployment's own operators.
Not a flag on a tenant's caller — see
[the two servers](server.md#2-the-two-servers-and-why-there-are-two) for why
there is no such thing as an operator inside a tenant.

`Add` is worth a second look. It has no row to narrow, so the wall says nothing
about it; whether a tenant may create its own contract is decided by the policy
alone.

## 3. The trail asks it

The trail's retention is per kind of thing, and per tenant when the app answers
for them: `trail.Policy.Tenants` is asked once a pass about each tenant — see
[the runtime](../runtime.md#and-per-tenant-when-the-app-answers-for-them).
Through ent, because the sweep is the deployment acting on itself and not a
caller:

```go
p, err := c.Audit.Policy()
if err != nil {
	return nil, err
}

p.Tenants = func(ctx context.Context, tenant pdid.Id) (trail.Tenant, error) {
	v, err := db.Contract.Query().
		Where(contract.TenantIdEQ(tenant.Uuid())).
		Only(ctx)
	if ent.IsNotFound(err) {
		// No contract: the deployment's own windows.
		return trail.Tenant{}, nil
	}
	if err != nil {
		// Keeps everything of theirs this pass, and says so in the log.
		return trail.Tenant{}, err
	}

	return trail.Tenant{Keep: &trail.Keep{
		Retain:  90 * 24 * time.Hour,
		Destroy: time.Duration(v.HistoryDays) * 24 * time.Hour,
	}}, nil
}
```

Three things the answer can rely on:

- **The zero answer is the deployment's.** A tenant with no contract keeps what
  `audit:` says.
- **An error keeps everything.** Nothing of that tenant's is moved or destroyed
  until the next pass that gets an answer — not the deployment's window, which
  nobody chose for them.
- **The deployment's floors still hold.** `audit.min` is what the deployment
  owes whatever a contract says, and an answer below it is raised to it.

A tenant that has left still has a trail, so the callback is asked about
tenants that no longer exist. What it answers is what your offboarding says:
the deployment's windows, or a short one once its grace is over.

If the app keeps history of its own — events, time rows — have that read the
same entity, so that the two never disagree about how long this tenant's
history lasts.

## 4. When a window shortens

The trail applies whatever the callback answers from the next pass, so **grace
is the callback's**: a contract whose new window takes effect after a grace
period answers with the old one until it has run out. `date_effective` is what
makes that a lookup rather than a calculation somebody repeats.

Before the change takes effect, `trail.Policy.Preview` says what an answer would
take, kind by kind — rows that would leave the database, rows that would stop
existing — without changing anything:

```go
got, err := p.Preview(ctx, pd.TrailStore(db), tenant, trail.Tenant{Keep: &next})
// got["robot"].Discarded rows of robots would stop existing on the next pass.
```

Which is the warning a downgrade should come with, and the number a dashboard
shows after.

What a tenant may **view** and what is **kept** are two different things, and
they can shrink at different times: a downgraded plan can stop showing history
at once, from your read path, while the trail keeps it until the grace is over
— so a plan that comes back shows it again.
