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
- [5. A hold, and a tenant leaving](#5-a-hold-and-a-tenant-leaving)
- [6. Showing that the archive is what it was](#6-showing-that-the-archive-is-what-it-was)
- [7. A tenant's trail that leaves with it](#7-a-tenants-trail-that-leaves-with-it)

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

## 5. A hold, and a tenant leaving

A legal hold is the operator's too, and it goes in the same entity: a field
saying it is on and why, which the callback answers as `Hold`.

```go
out := trail.Tenant{Keep: &trail.Keep{...}}
if v.HoldReason != "" {
	out.Hold = &trail.Hold{Why: v.HoldReason}
}

return out, nil
```

While it is on, nothing of the tenant's trail stops existing: a pass still
moves its rows into the archive, and destroys nothing. A person asking to be
erased gets back what a hold kept — `trail.Forgotten.Held` — and is erased
again when it lifts, which is the app's to remember. See
[the runtime](../runtime.md#legal-holds).

A tenant leaving is `trail.Policy.PurgeTenant`, called **when** the app decides:
at once, or once the grace an offboarding gives has run out. A contract row
that says when it ends is what makes that a lookup.

```go
plan, err := policy.PlanTenantPurge(ctx, pd.TrailStore(db), tenant)
// The confirmation an operator reads: plan.Removed rows go, plan.Blanked keep
// their events, plan.Chunks of the archive are erased.

got, err := policy.PurgeTenant(ctx, pd.TrailStore(db), tenant)
```

It refuses while a hold is on, and answers what another tenant's hold kept of
the rows the two of them share.

Or it **retires**, `trail.Policy.RetireTenant`, which is the same but for what
the deployment's floors are on: `audit.min` and `audit.by.<kind>.min` are what
the deployment owes whoever it answers to — the access record PIPA has a
processor keep for a year — and a purge takes that with the tenant. A
retirement leaves those rows, and the tenant's key, for as long as each floor
has to run, and the passes destroy them as it does; once the last has passed,
a pass purges what is left. Everything else goes at once, as a purge's does.

Which of the two is the contract's to say, or the law the tenant is under: a
field on the same entity, read where the app offboards.

```go
leave := policy.PurgeTenant
plan := policy.PlanTenantPurge
if v.KeepFloors {
	leave, plan = policy.RetireTenant, policy.PlanTenantRetire
}

ahead, err := plan(ctx, pd.TrailStore(db), tenant)
// ahead.Kept is what stays, by kind, and ahead.Until when the last of it goes.

got, err := leave(ctx, pd.TrailStore(db), tenant)
```

A retirement is recorded in the archive, so it needs one; with floors and no
archive it is refused. The callback goes on being asked about a retired tenant
for its hold — answer one without an error once its contract is gone — and
its windows are not asked: it keeps what its floors are on, and nothing else.

## 6. Showing that the archive is what it was

What a customer or an auditor asks of an archive after *was it deleted* is *has
it changed*. The database keeps an account of every blob of the archive, and
`trail.Policy.Verify` answers the second question:

```go
v, err := policy.Verify(ctx, pd.TrailStore(db), full)
// v.Findings: a chunk missing, one nobody wrote, bytes that are not their
// digest, a checkpoint the manifest no longer adds up to. Empty is the
// archive as the database says it is. v.Digest is the findings as one
// digest, and v.Accepted the latest acceptances: print both.
```

Keep checkpoints apart from the archive, on a store that refuses deletion —
`audit.checkpoints.dir` — so that somebody who can write the database and the
archive both cannot rewrite what the archive held, and give the operator a
command that runs this, `full` on a schedule. They are signed with
`audit.checkpoints.key`, and an archive is refused without one: a key mounted as
a secret, to start with, which `openssl genpkey -algorithm ed25519` or
`trail.GenerateKey` makes; a key the process cannot give away — a TPM, an HSM,
Vault, a cloud KMS — is named there by its URI once the app imports what speaks
to it (see [keys in a device](../runtime.md#keys-in-a-device)), and any other
`crypto.Signer` is handed in as `trail.Policy.Signer`. A deployment that archived
without a key sets one and accepts once (`Policy.Accept`, below).

The live rows can be checked as well. With `audit.digest.key`, each row of the
trail keeps a keyed digest of the row its write left. The key is a file of
`openssl rand -base64 32`, mounted as a secret, as the checkpoint key is. The
generated `pd.Digests`, handed the policy so that it reads the archive too,
finds a row somebody changed in the database past the servers, one somebody
added there and one somebody removed: give the operator a command that runs
it, on a schedule too, and that fails on every list it answers with and prints
the kinds it skipped, and the findings as one digest, `audit.Rows.Digest`. And
one that accepts what an operator has looked at -- rows a migration rewrote,
rows written before the trail could say anything about them -- by recording
them, `pd.Record(ctx, db, contents, action, ids)`, or `pd.RecordErased` for ones
that are gone: given the digest the operator looked at, and refused when a
check now answers another, so that a row found after they looked is not
recorded with the rest. See
[a digest of the row the write left](../runtime.md#a-digest-of-the-row-the-write-left).

A deployment that was given its digest key after its first write says when, in
`audit.digest.since`, and a key it lets go goes into `audit.digest.retired` by
its fingerprint (`audit.FingerprintOf`) rather than out of the configuration:
otherwise every row is a finding, which is what a check with the wrong key is.

The key signs every row of the manifest as well, so that a row changed after
the latest checkpoint is found too, and each checkpoint names the one before
it. On a WORM mount, say so — `audit.checkpoints.write_once: true` — and the
process never erases or relabels a checkpoint either; on S3 with Object Lock,
hand the store in as `trail.WriteOnce(store)`. Rows written before the
deployment had a key are findings until an operator accepts them once, which
signs them as they stand.

Name witnesses for the checkpoints too, `audit.checkpoints.witnesses`: what
hears each checkpoint outside the database and the archive, so that a chain cut
short at its newest is found ([witnesses](../runtime.md#witnesses)). `pd new`
links payday's two in `cli`; another app imports `config/witnesslog` and
`config/witnessstore`. `log:` tells the log and a gauge, and is a witness only
with the alerts the runtime docs give set on it; `store:<dir>` keeps what it
hears on a disk of its own — mount it with credentials neither the database's
nor the archive's, and on a disk that refuses deletion. A verification lists
what each heard last (`Verified.Heard`): print it.

Every act that changes the archive ends in a checkpoint, as a pass does. Once
the rows no longer add up to the latest one, none is taken — every act says so
in its error, after doing what it does — until an operator who has looked
accepts. Have the command that runs `Verify` page somebody rather than log.

An acceptance is of what the operator looked at, by the digest the
verification printed, and says who and why:

```go
v, err := policy.Accept(ctx, pd.TrailStore(db), trail.Acceptance{
	Of:  digest, // what `verify` printed, as the operator gives it back
	By:  who,    // the operator, as your command knows them
	Why: why,
})
```

It verifies again and is refused (`trail.ErrUnreviewed`) when that finds
anything else, so give the operator the digest with the findings and take it
back from them, never from a verification of your command's own. The
acceptance ends in a signed checkpoint that keeps who, why, the digest and what
was accepted, and in a receipt (`accept`). A database restored from a backup
older than the latest checkpoint is accepted only as that: the command takes
the checkpoint the backup holds and passes it as `Restored`, and the operator
looks first at what `Verify` lists as `lost` — what the newest checkpoint says
the archive held, and it no longer does.

An export of a tenant's trail — a tenant leaving, a person asking what is kept
about them — reads the archive the same way:

```go
err := policy.ReadTenant(ctx, pd.TrailStore(db), tenant, pd.TrailOf(func(v *app.Audit) error {
	// only what the manifest accounts for; a chunk whose bytes changed is an error
}))
```

`trail.ReadTenant` reads the archive as it is, for when there is no database
left to check it against. See [the runtime](../runtime.md#the-manifest).

## 7. A tenant's trail that leaves with it

A tenant that leaves is purged from the archive, and not from the archive's
backups, which are kept as long as the archive is. Sealing what the rows say
with a key per tenant is how it leaves those too:

```sh
age-keygen -o master.txt    # kept offline; its public half is printed
```

```yaml
audit:
  archive: /var/lib/acme/audit
  keys:
    dir: /var/lib/acme/audit-keys   # a store of its own, not inside the archive
    master: age1…                   # the public half of master.txt
```

The serving process seals and never reads: it holds the master's public half
and the tenants' public halves, and nothing that opens a row. A command that
reads what the rows say — an export, a subject access request — is given the
master for that run alone, by its file or the key itself:

```sh
ACME_AUDIT_KEYS_IDENTITY=/run/secrets/master.txt acme tenant export --tenant @acme
```

Without it, `policy.ReadTenant` refuses a sealed row with `trail.ErrSealed`,
rather than handing over a row with its contents missing.

`policy.PurgeTenant` destroys the tenant's key before anything else, so a purge
that stops part way has already made what was the tenant's alone unreadable,
and asking again finishes the rows. Back the key store up on a **short**
schedule and keep few copies: how long a departed tenant's rows can still be
read from a backup is how long the key store's backups are kept. And never on
a store that keeps what is erased -- a versioned or Object-Locked bucket keeps
every key it was ever given, and a purge then destroys nothing. See [the
runtime](../runtime.md#sealed-contents).

