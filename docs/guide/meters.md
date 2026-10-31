# Measuring what a tenant holds

An app paid by what its tenants hold -- assets, seats, stored bytes -- declares
those numbers in its schema, and payday measures them: tenant by tenant, on a
schedule, from the app's own tables, and hands each reading to a sink the app
wires, which is the billing service's client (payday#186).

The two other ways to get these numbers both fail without a word. A billing
service that reads the app's database is tied to its schema: rename a column or
add a status, and it counts the wrong rows while nothing fails. Monitoring
metrics are not a billing source: a sample lost cannot be told from one doubled,
and nothing can compute them again. A declaration is held to the schema by `pd
gen` every time it runs, so a field renamed under a meter, or a value an enum no
longer has, is a generation that stops rather than a bill that is wrong.

Nothing outside the app reads its tables, and the app writes no rows for
metering: a reading is handed over and kept by whoever it is handed to.

What this measures is what a tenant **holds**, now. What happens over time --
bytes sent, calls made -- has no row to count; [§7](#7-flows) is the convention
for those.

The runtime is the [`meter`](../../meter) package; its comments are the detail.
The snippets use `pd` for your generated `server/pd` package, as the
[server guide](server.md) does.

## 1. Declare what is measured

In the file that says `option (payday.app)` -- the one whose package is your
app's -- beside whatever else it says:

```proto
option (payday.app) = {
  past: {event: 17, fact: 16}
  meters: [
    {
      key: "acme.assets.items"
      unit: "{asset}"
      count: {
        of: "Asset"
        where: [
          {field: {name: "kind", number: 8}, in: ["KIND_ITEM"]},
          {field: {name: "date_disposed", number: 17}, set: false}
        ]
      }
    },
    {
      key: "acme.seats.admin"
      unit: "{seat}"
      count: {
        of: "Holder"
        where: [{field: {name: "role", number: 8}, in: ["owner", "admin"]}]
      }
    },
    {
      key: "acme.attachments.bytes"
      unit: "By"
      sum: {of: "Attachment", field: {name: "size_bytes", number: 10}}
    }
  ]
};
```

Each meter is:

- **`key`**, what a reading is filed under and what the billing service prices:
  lower case words of letters, digits and `_`, joined by dots, the app's name
  first -- as a loop's name is. At most 255 bytes, and one key to a meter.
  It is chosen once. A meter that comes to count something else is a **new
  key**, and the old one stops being read: a key that kept its name and changed
  what it counts would bill one period by two rules.
- **`unit`**, what a value counts in, as OpenTelemetry writes one (UCUM): `By`
  for bytes, `{asset}` for a count of assets, `d` for days. Required, since a
  number with no unit is one nobody can price.
- **`count`** of an entity's rows, or **`sum`** of one integer field over them.
  `of` is the entity by its message name in your package -- `Asset` -- or by its
  full name for one in another. It may be one of payday's own: a seat is a
  `Holder`, which is why meters are said here and not on the entity, where an
  overlay could not add one.

What is counted is the rows of that entity that belong to the tenant being
measured -- by the same path its wall reads, a `via:`, a stamp or a column --
and that are **not erased**: an erased row is not one a tenant holds. `where`
narrows them further, every condition at once.

### `where`

A condition names one field or edge, by its name **and** its number, and says
exactly one of three things:

| | what a row must hold | for |
| --- | --- | --- |
| `in: [...]` | one of these values | a string, an enum, a bool, an integer |
| `not_in: [...]` | none of these values -- and a field that may be null and is, counts | the same |
| `set: true` or `set: false` | a value at all, or none | a field or an edge that may be null |

Values are written as the text format writes the field's kind: a string as it
is, an enum by the name of its value, a bool as `true` or `false`, an integer in
decimal. `pd gen` holds an enum's to the enum, and an integer's to its range. A
string's it cannot -- nothing in the schema says what a string may hold -- so a
meter counting a closed set of values is better served by an enum.

Both the name and the number, where the rest of a schema may say either alone:
the number is what a field is, so a field renamed is still found, and the name
has to agree with it, so a meter never follows a rename onto a new field that
took the old name. A meter quietly counting another column is a bill quietly
wrong.

That is the whole language, and it is small on purpose: equality with one of a
few values, and whether there is a value. Both are the same statement on SQLite
and on PostgreSQL, whatever the collation or the zone, and both are typed Go in
the generated query -- a value is never text spliced into SQL. What is left out
is a meter you write ([§3](#3-meters-the-schema-cannot-say)):

| left out | why |
| --- | --- |
| a range of numbers or times | a window, and a meter of what is held now has no use for one |
| a pattern, a prefix | each database matches one its own way |
| a field of another entity | a join |
| a float, bytes, an identifier, a time's value, a list, a map | a float compared for equality is never quite there; the rest are one tenant's values and not a schema's |
| an edge's value | the rows it points at are identifiers; say `set:` of it, or count what it points at |
| a sum of anything but an integer | a bill is whole numbers: an amount in its currency's smallest unit, a size in bytes |

### What `pd gen` refuses

| | |
| --- | --- |
| a key that is not one, two of one key, or meters said in two files | two answers to what a reading is filed under |
| no `unit`, or one that is not printable ASCII of at most 63 bytes | a number nobody can price |
| an `of` that is no entity, is `global`, or names two tenants in a row -- the trail's shape | somebody's bill made of everybody's rows |
| a field by its name or its number alone, the two disagreeing, or nothing at the number | a meter that counts another column, or nothing |
| a value its enum does not have, an integer out of range, a bool that is not, a value twice | a meter that counts nothing, and says so to nobody |
| a condition on the erased field or on what says whose a row is | a meter that is already narrowed by both |
| `set:` of a field that is never null, two conditions on one field, or none or two of `in`, `not_in` and `set` | a condition that says nothing, or two things |

And one warning: a meter of an entity with no index that begins with what says
whose a row is. A pass reads one tenant's rows at a time, so without one, every
tenant's reading is a scan of the whole table.

Regenerate:

```sh
go tool pd gen .
```

An app pinned to an older `buf.build/payday/payday` runs `buf dep update` first,
for an option that has `meters`.

## 2. What `pd gen` writes

Two functions in `server/pd`, and nothing at all for an app that declares no
meters:

- **`pd.Meters(client)`**, a `meter.Meter` for each declaration, in the order
  declared, measured on the client it is given. Each is one statement:

  ```go
  q := db.Asset.Query().Where(
  	entAsset.TenantIdIn(vs...),          // the tenant's, as its wall reads it
  	entAsset.DateErasedIsNil(),          // and held
  	entAsset.KindIn(int32(1) /* KIND_ITEM */),
  	entAsset.DateDisposedIsNil(),
  )
  n, err := q.Count(ctx)
  ```

  A sum is `COALESCE(SUM(...), 0)` cast to `BIGINT`, so a tenant with no rows
  holds zero and a sum past what a `bigint` holds is refused rather than
  rounded.
- **`pd.MeterTenants(client)`**, every tenant there is, a page at a time in the
  order of their identifiers. Closed ones too: a tenant holds what it holds
  until it is destroyed.

## 3. Meters the schema cannot say

A meter is a value, and one you write is the same value with a function of your
own: how much of it one tenant holds, now.

```go
// kept is how long a workspace keeps its history, by the window in force now:
// a shorter one waits out the grace before it is.
kept := meter.Meter{
	Key:  "acme.history.keep",
	Unit: "d",
	Measure: func(ctx context.Context, tenant pdid.Id) (int64, error) {
		w, err := windows.Of(ctx, tenant)
		if err != nil {
			return 0, err
		}

		return int64(w.Keep / (24 * time.Hour)), nil
	},
}

// stored is what the storage holds of a tenant, from its inventory.
stored := meter.Meter{Key: "acme.storage.bytes", Unit: "By", Measure: inventory.BytesOf}
```

`Measure` is called for every tenant on every pass, and whenever the app asks
for one tenant ([§5](#5-before-a-tenant-is-destroyed)), so it is safe to call
from two goroutines at once. An error is that one reading missing; the tenant's
others go.

## 4. Reading them

`meter.Reader` is the loop, and the one place the meters, the tenants and the
sink meet:

```go
s.Meters = &meter.Reader{
	Tenants: pd.MeterTenants(client),
	Meters:  append(pd.Meters(client), kept, stored),
	Sink:    sink, // the billing service's client
}
s.Spin = append(s.Spin, s.Meters)
```

It is a `spin.Spinner` called `payday.meter.read`, so under `spin.lease` one
process of the deployment runs it -- and, as with every loop under a lease, for
a moment around a failover two may. That is safe: a reading is what it is, and
one made twice is two readings of what was held ([§6](#6-the-sink)).

It gives up as it starts, ending the process, when it is missing what it is
made of: a sink, its meters, a key that is not one, two meters of one key. A
deployment that bills by these and reads none of them would otherwise be one
nothing mentions.

### When

`Every`, a day when it says nothing: a pass as the reader starts, and then one
at each multiple of `Every` since the Unix epoch -- each midnight UTC for a day,
on the hour for an hour. On the clock, and not an interval after the last pass,
which would drift later each day until a day had none; a pass that runs past a
boundary does not double up, and the one it ran past is not made up.

One schedule for every meter, and not one each. A tenant is measured at one
moment, every meter of it, so its readings are one picture of it; a meter that
wants to be read more often is a reading too many of the rest, which a sink
keeps and a bill does not mind.

How often is the billing service's question. Daily is enough for holdings
priced by the month. A deployment that restarts reads at each start as well,
and a reading too many is never a reading wrong.

### What fails

A pass logs what fails and goes on, and then tries it again -- a minute later,
then longer each time, at most an hour apart -- until it is done or the next
pass begins:

- readings the sink did not take are handed it again **as they were**, so that
  one which did reach it -- a call that timed out after it was kept -- counts
  once;
- a tenant one of whose meters could not be measured is measured again, every
  meter of it, at a moment of its own;
- a pass that could not list the rest of the tenants goes on from the last it
  measured.

What is still not done when the next pass begins is let go, with an error in
the log naming how many tenants and some of them: those tenants have no reading
from that pass. Nothing is written down meanwhile, so a process that stops
loses what it had not handed over, and the next pass -- in whichever process
holds the lease then -- measures again.

### From a replica

It reads what it is built on. A deployment with a replica of its database
builds the meters and the tenants on a client of the replica, and every pass
reads there instead of where requests are answered:

```go
s.Meters = &meter.Reader{
	Tenants: pd.MeterTenants(replica),
	Meters:  pd.Meters(replica),
	Sink:    sink,
}
```

A reading is then as old as the replica is behind, and stamped when it was
read, which is what a reading is. A meter you write reads wherever it reads;
build it on the same replica if it reads the database.

## 5. Before a tenant is destroyed

Destroying a tenant takes its rows, and with them the source of its last
period. So whatever destroys one reads it first, and does not go on until that
has worked:

```go
if err := s.Meters.Read(ctx, tenant); err != nil {
	return fmt.Errorf("%s: its meters were not read, so it is not destroyed yet: %w", tenant, err)
}
```

`Read` measures every meter of the one tenant now and hands the readings to the
sink, and answers an error for a meter it could not measure and for a sink that
did not take the rest. Trying again is safe: what it did hand over is the
sink's, and handed over again counts once. It reads where the reader was built
to; a tenant that has been closed for its restore window has written nothing
for a replica to be behind on, and an app that wants the primary here builds a
second reader on it.

## 6. The sink

```go
type Sink interface {
	Send(ctx context.Context, rs []meter.Reading) error
}
```

One tenant's readings at a time, all from one measuring. Each is:

| | |
| --- | --- |
| `Tenant` | whose |
| `Meter` | the key |
| `MeasuredAt` | when, in UTC, to the microsecond |
| `Value` | how much |
| `Unit` | the meter's, from the declaration |

**The first three are what a reading is.** A sink keeps one reading of each
tenant, meter and moment, and takes a second as the first: that is what makes a
retry, or two processes around a failover, count once. The value goes with the
moment -- a measuring is stamped once -- so the same three never come with two
values. `MeasuredAt` is to the microsecond because that is what PostgreSQL keeps
of a time: a sink that keeps it there keeps exactly what it was handed, and a
reading handed over again is the same reading to it.

It is the moment the reading was taken and nothing rounder. It is in UTC and of
no tenant's zone, because a reading is not of a day or a month: which period it
falls in is the billing service's to say, in the zone the bill is in.

`Unit` is carried so that a sink can refuse a reading whose unit is not the one
it prices that meter in -- the one change a key does not stop. An error from
`Send` is the sink not having the readings, which the reader hands it again;
the context ends only when the app stops, so a sink that waits on the network
sets a deadline of its own. `meter.SinkFunc` makes one of a function, for a
test.

## 7. Flows

Something that happens over time -- bytes sent, calls made, minutes of compute
-- has no row to count, so payday does not measure it. It is counted where it
happens, and the convention is OpenTelemetry's own data model, used for a bill
rather than for a dashboard:

- **A Sum, monotonic and cumulative**: an `Int64Counter`, exported with
  cumulative temporality, which is the OTLP exporters' default. Never delta.
- **Named by its meter key**, `acme.transfer.bytes`, with its unit -- one
  namespace for what a tenant holds and what it does.
- **The tenant as an attribute**, `payday.tenant` (`meter.TenantAttribute`),
  holding the canonical text of its identifier.
- **Exported to the billing service's own receiver, and never through the
  monitoring pipeline**: a `MeterProvider` of its own, which is not the one
  `otel:` configures for the app's telemetry. A monitoring pipeline samples,
  drops and aggregates as it sees fit, and a tenant attribute is cardinality it
  was never sized for.

```go
exp, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(c.Billing.Receiver))
if err != nil {
	return err
}
billing := sdkmetric.NewMeterProvider(
	sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)),
	sdkmetric.WithResource(res), // service.name, and service.instance.id per process
)
defer billing.Shutdown(context.WithoutCancel(ctx))

sent, err := billing.Meter("acme").Int64Counter("acme.transfer.bytes", metric.WithUnit("By"))
if err != nil {
	return err
}

// Where it happens:
sent.Add(ctx, n, metric.WithAttributes(attribute.String(meter.TenantAttribute, tenant.String())))
```

Why it is shaped so: a cumulative point is the whole count since the series
began, so one sent twice changes nothing and one lost is covered by the next.
Each process is a series of its own, started at zero with a start time of its
own, and the receiver adds up the increase of each -- a series that restarts is
told apart from one that went down by its start time. The one thing lost is
what a process counted after its last export, if it dies before the next: that
bills less, never more. `Shutdown` exports what is left, so a process that is
stopped rather than killed loses nothing.

## 8. What `pd doctor` asks

From your own Go, outside its tests:

- **Meters the schema declares and nothing reads**: no call of `pd.Meters`
  anywhere, so no reading of them reaches a sink -- the app serves, and bills
  for none of it.
- **A `meter.Reader` with no `Sink`**, nor one set on it afterwards: it gives up
  as it starts, and with it the process.
- **A `meter.Reader` that is never in a spin list** -- handed to no `append`,
  in no slice, itself or what it was put in or the function that builds it. It
  reads a tenant when the app asks for one and on no schedule at all.

## Where to go next

| | |
| --- | --- |
| [server.md](server.md#6-background-work) | the spin list, and the lease the reader runs under |
| [schema.md](schema.md) | what an entity declares, which is what a meter is held to |
| [runtime.md](../runtime.md#6-background-work) | the loops payday runs, and what each promises |
