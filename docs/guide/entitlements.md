# What a tenant is entitled to

An app sold by a billing service -- a plan with a feature, a plan with a number
of something -- keeps a copy of what each of its tenants is entitled to in its
own database, and reads it inside the transaction of the write it decides
(payday#185). payday writes the table, keeps it current from the billing
service's grants, and gives the app the reads.

There are two obvious ways to check what a tenant may have, and neither works.
**Asking the billing service on every call** makes every write depend on that
service, and is slow. **Checking a cached answer outside the transaction**
lets two concurrent writes both pass a limit of one: each reads that there is
room, and each takes it. What is both fast and exact is a copy in the app's
own database, read in the same transaction as the write that uses it -- and,
for a limit, locked there, so that the second write waits for the first and
then counts what the first wrote.

The billing service is a **source of grants**, and it speaks payday's
contract, `payday.GrantService` (`proto/payday/grant.proto`): payday depends on
no billing service, and any source that speaks the two calls can be followed.

The runtime is the [`entitle`](../../entitle) package; its comments are the
detail. The snippets use `pd` for your generated `server/pd` package, as the
[server guide](server.md) does.

- [1. Opt in](#1-opt-in)
- [2. What a grant is](#2-what-a-grant-is)
- [3. Reading it in the write's transaction](#3-reading-it-in-the-writes-transaction)
- [4. Keeping it: the follower](#4-keeping-it-the-follower)
- [5. When the source cannot be reached](#5-when-the-source-cannot-be-reached)
- [6. On the trail](#6-on-the-trail)
- [7. It is the tenant's](#7-it-is-the-tenants)
- [8. What `pd doctor` asks](#8-what-pd-doctor-asks)
- [9. Serving it: what a source does](#9-serving-it-what-a-source-does)

## 1. Opt in

In the file that says `option (payday.app)` -- the one whose package is your
app's -- beside whatever else it says, and at a domain of your app's own:

```proto
option (payday.app) = {
  past: {event: 17, fact: 16}
  entitlements: {domain: 43}
};
```

`pd gen` then writes payday's `Entitlement` into the app, at
`proto/<pkg>/payday/entitlement.proto`, numbered as declared, and takes it out
again when the declaration goes. The domain is the app's for the reason the
past's two are: a table an app opts into would otherwise take a number from
every app that had already spent it. It is chosen once and never changed.

Nothing else is declared. A key is a string the source grants and the app asks
for, and a key nobody granted is a tenant not entitled to it -- which is the
answer it should be. A list of keys in the schema would be a second copy of
the billing service's price book in every app it sells for.

`pd gen` refuses:

| | |
| --- | --- |
| a domain of 0, past 255, or in payday's 1..6 or 248..255 | an identifier that says nothing, or one payday gives something else |
| a domain the past, the notifications, `domains:` or another entity holds | two kinds of row under one number |
| a message, an enum or a service called `Entitlement`, `EntitlementValue` or `EntitlementService`, or a file called `entitlement.proto`, in the app's own schema | two of one name in one Go package |
| entitlements declared in two files | two answers to where the copy is numbered |

An overlay at `proto/ext/payday/entitlement.ext.proto` may add indexes, and
fields at the numbers the copy leaves empty -- 3 to 7 -- as for any of payday's
entities.

## 2. What a grant is

A **grant** is one thing a source gave a tenant, for a window of time:

```proto
message EntitlementGrant {
  bytes  id        = 1; // the source's, unique, never given to another
  bytes  tenant_id = 2; // the app's tenant
  string key       = 3; // `acme.export`, `acme.robots`
  uint64 version   = 4; // greater with every change of this grant
  oneof value {
    bool      on        = 5; // a feature, on or off
    int64     limit     = 6; // how much of something, zero or more
    Unlimited unlimited = 7; // no bound -- a value, and not none
  }
  google.protobuf.Timestamp valid_from  = 8; // unset is from always
  google.protobuf.Timestamp valid_until = 9; // every grant says one
}
```

The copy holds one row for each grant, `Entitlement`: the key, the value, the
window, and the grant it copies by identifier and version.

It is **state, and never a delta**. Every message about a grant is all of it,
at a version, so a grant heard twice is applied once, two heard out of order
converge on the newer, and a copy that missed some is put right by the next
one it hears. Versions are compared between states of one grant and never
between two grants. A grant's tenant and key are its own: a source that
sends one under another is not believed (see [§4](#4-keeping-it-the-follower)).

A grant is changed by sending it again at a higher version, and taken away by
**ending** it: sent again with `valid_until` at the moment it ends. A grant
that ended may then go from the source's list -- after a day, say, so that a
comparison begun before it ended never finds it gone -- and the copy forgets
it the next time it compares. A grant the source stops listing **that has not
ended** is kept until it does: the copy takes nothing away on a guess, and a
source that forgot a row by mistake, or answered an empty list, takes nothing
from anybody.

When more than one grant of a key is in force at once, **the one that began
last decides** -- and of two that began at once, the one whose identifier is
greater. That is what lets a source say the future: this period's grant runs
on through its grace while next period's has begun, and from that moment the
next one holds, a downgrade included. It is not a sum: a grant is the whole of
what a tenant has of its key while it decides, so a source that sells a tenant
more of something sends the new total.

## 3. Reading it in the write's transaction

Four reads, each for the tenant the context is framed as:

| | |
| --- | --- |
| `entitle.Has(ctx, r, key)` | whether a grant in force is on, a limit above zero, or unlimited |
| `entitle.Limit(ctx, r, key)` | how much may be held: `Limited(n)`, `Unlimited`, or `None` -- **locked**, in a transaction |
| `entitle.Get(ctx, r, key)` | what is held of a key and the grant that says so, for showing it |
| `entitle.All(ctx, r)` | every grant the copy holds of the tenant, in force or not |

`r` is `pd.Entitlements(drv)`, on the driver the write is on. A write a limit
decides reads it in its own transaction, counts **after** it, and writes:

```go
func (s Core) addRobot(ctx context.Context, req *app.RobotAddRequest) (*app.Robot, error) {
	var out *app.Robot
	err := s.tx(ctx, func(ctx context.Context, drv dialect.Driver, next app.Server) error {
		limit, err := entitle.Limit(ctx, pd.Entitlements(drv), "acme.robots")
		if err != nil {
			return err
		}

		held, err := countRobots(ctx, drv) // after the lock: a write that won the race is counted
		if err != nil {
			return err
		}
		if !limit.Allows(held + 1) {
			return status.Error(codes.FailedPrecondition, "this plan holds no more robots")
		}

		out, err = next.Robot().Add(ctx, req)
		return err
	})

	return out, err
}
```

`Limit` locks the key's rows until the transaction ends -- `FOR UPDATE` on
PostgreSQL; SQLite begins a write one at a time, so a transaction that writes
already holds what a lock would take. A second write deciding by the same
limit waits for the first, and counts what it wrote. It is exact under READ
COMMITTED, PostgreSQL's default and what a payday transaction runs in, and
under SERIALIZABLE; a transaction begun REPEATABLE READ counts from a snapshot
older than the lock, and is not. Outside a transaction it refuses
(`entitle.ErrNotInTx`), since a lock let go at once is the race it is there to
close.

`Has` takes no lock: a switch is not used up, so a write it lets through does
not change what the next one is told, and two of them racing are both right.
`Get` and `All` take none either, and are for showing: *3 of 5 robots*.

**A key no grant gives is not entitled, and that is never an error**: `Has` is
false, `Limit` is `None`, whose `Allows` is nothing above zero. The errors are
the context framed as no tenant (`entitle.ErrNoTenant`), a key that is not one
-- `CheckKey`: lower case words joined by dots, the app's name first, as a
meter's key -- and a key granted as a switch read as a limit, which is the
source and the app disagreeing on what it is.

## 4. Keeping it: the follower

```go
s.Follower = &entitle.Follower{
	Source: pdpb.NewGrantServiceClient(conn), // the billing service, dialled as the app dials anything
	Copy:   pd.EntitlementCopy(drv, content), // content: the trail's, as the recorder is given it
}
s.Spin = append(s.Spin, s.Follower)
```

`entitle.Follower` is a loop called `payday.entitle.follow`, so a lease runs
it in one process of the deployment. It watches the source's stream and
applies each grant as it is heard of; once the stream says it is ready, it
compares the copy with the whole of the source's list (`Reconcile`) -- at
once, and then every `Every`, an hour by default, for whatever the stream
missed without ending.

- **By identifier and version**: a grant the copy does not have is written, a
  newer version of one it has is written over it, and one at a version it
  holds, or below, changes nothing. So the stream and the list, or two
  processes around a failover, write each grant once.
- **A tenant the app does not have** -- not made yet, or destroyed -- is given
  nothing. One just made can be compared now, from any process:
  `s.Follower.Sync(ctx, tenant)`.
- **A grant that is no grant** -- no end, no value, no version, a key that is
  not one, a tenant that is not one -- is said in the log and left out, and so
  is one **moved** to another tenant or key: the row it moved from stays as it
  was until it ends. None of them stops anything.
- **A stream that ends**, or cannot be opened, is opened again a second later
  and longer each time, up to a minute, and begins with a comparison; a grant
  heard and not written ends the stream for the same reason. A comparison that
  fails is tried again while the stream carries on, and one whose list the
  source could not finish takes nothing out.
- **Every opening reads the whole list**, since nothing is replayed. So dial the
  source with keepalive (`grpc.WithKeepaliveParams`), and a proxy's idle
  timeout does not end a stream that is only quiet; one that keeps ending as
  soon as it is ready is waited on longer each time.

Each tenant's comparison is one transaction, which locks the tenant's rows in
the order a limit's read does, so the two never wait on each other the other
way round.

A follower with no source or no copy gives up as it starts, which ends the
process.

## 5. When the source cannot be reached

The copy goes on answering, as it was. Nothing is taken out of it for want of
hearing from the source -- not by a stream that ended, a list that failed, or
a list that came back short.

What bounds how stale it can be is `valid_until`, which every grant says: a
copy that stops hearing keeps a grant until then and not forever. So a source
says a horizon it renews -- the end of the paid period and a grace after it,
say -- and a grant meant to last for good says a far one, where somebody
reading the source can see it.

What happens once a grant has ended while the source was away is the app's
policy, and payday does not decide it. `entitle.All` answers the grants that
ended with the rest, and `entitle.Decide` says which is in force at any
moment, so an app that would rather keep a tenant on for a day says so in its
own read:

```go
rows, err := entitle.All(ctx, pd.Entitlements(drv))
got := entitle.Decide("acme.export", rows, time.Now().Add(-24*time.Hour))
```

## 6. On the trail

The copy declares `trail: {recorded: {}}`: each write of it is on the trail as
the act it is, in its transaction -- `payday.entitle.apply` for a grant
written, `payday.entitle.forget` for a row taken out -- by the deployment, and
filed under the tenant.

Recorded rather than kept off because the copy decides what a tenant may do,
and a check of the rows (`pd.Digests`) reads only the kinds that are recorded.
A row put into the copy past the follower -- a tenant entitled by a hand in the
database -- is then what the check finds, as a row nothing on the trail
accounts for. The writes are few: a grant when its version moves, a row when
the source forgets it, and nothing at all for a comparison that finds the copy
right.

## 7. It is the tenant's

The copy's rows are behind the wall by their edge to the tenant, and the
database keeps none past it: a tenant cannot be deleted while its copy is
there. An offboarding that takes a tenant's rows table by table takes these
with the rest, and the trail's tenant purge takes their lines, which are filed
under the tenant. The source keeps the grants themselves; a grant it still
says of a tenant the app no longer has is applied to nothing, so the copy does
not come back.

An export need not carry the copy, since the source is where it comes from.
One that does, imported again, is compared with the source like any other
copy: the newer version decides, and a grant the source has ended and forgets
goes.

## 8. What `pd doctor` asks

Read off your app's own Go, outside its tests:

- the schema declares entitlements and **no `entitle.Follower`** is built --
  no source of grants is configured, so every tenant is entitled to what the
  copy last held: for a new deployment, nothing;
- a follower with **no `Source`**, or no `Copy`, and none set later -- which
  gives up as it starts;
- a follower **never handed to an `append` or put in a slice** -- itself, what
  it was put in, or the function that builds it -- which keeps nothing.

What it does not ask is whether anything reads the copy. Which writes a grant
decides is the app's own judgement, written where each write is.

## 9. Serving it: what a source does

A source -- levyan, or any other -- implements `payday.GrantService`:

- **Who hears what** is the caller's credential, as a wall narrows a read: an
  app hears its own tenants' grants, and the request names no app.
- **`Watch`** sends `ready` first, once the stream will carry every change from
  then on, and then each grant whose state changes, as it is after the change.
  Nothing is replayed; a stream may be ended whenever it must be -- a consumer
  too slow -- and the follower reconnects and compares.
- **`List`** answers every grant that has not ended, and those that have for as
  long as the source keeps them -- a day after they end at least -- a page at a
  time with `cursor` and `next`, or one tenant's when `tenant_id` says which.
- **Every change is a greater version** of the grant, and a grant's tenant and
  key never change. A grant is taken away by ending it.
- **Every grant says when it ends**, and one in force is renewed before its
  horizon comes.
- **One grant decides a key** at a time; a source that sells more of something
  sends the new total, and a plan changing at a period's end is next period's
  grant beginning while this one's grace runs.

## Not here

Quotas that are spent -- reserving an amount per request with an idempotency
key, and committing or releasing it, for a metered resource that must stop
exactly at its limit. What is here is what a tenant may **hold**, counted by
the app in the transaction that adds to it. What a tenant holds, measured for a
bill, is [meters](meters.md); what a deployment's operator decides about a
tenant by hand is an [entity of the operator's](operator.md).

## Where to go next

| | |
| --- | --- |
| [`entitle`](../../entitle) | the reads, the follower, and what each refuses |
| [`grant.proto`](../../proto/payday/grant.proto) | the contract a source serves |
| [server.md](server.md) | the spin list the follower goes on, and a lease |
| [meters.md](meters.md) | what each tenant holds, measured for the billing service |
