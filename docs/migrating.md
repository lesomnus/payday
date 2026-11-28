# Migrating

What an app has to change when payday does, newest first, each entry saying how
to tell whether it applies to you.

Until the first app built on payday launches, payday keeps nothing for an
earlier payday -- a store, a format, a lock, configuration or generated code an
earlier payday made is not carried forward -- and so nothing here says how to
bring one forward. What is entered before then is a change in what payday does
that an app's own code may have leaned on. From that launch on, every change
that asks something of an app is entered here.

What holds for every version is how a rename is carried, below.

## A session's deadlines are what a store keeps

**It applies to you** if your session store is a table (`authsession.Store`
over a database) and your code or tests compare the session `Sessions.Mint` or
`Sessions.Read` answered with the row, to the nanosecond.

The deadlines `authsession` computes from the clock -- the `Expires` and `Idle`
that `Mint` fills when it is handed none, and the `Idle` a `Read` moves -- are
cut to the microsecond before they are stored, so what is answered is what a
later read gives on a store that keeps microseconds, as PostgreSQL's
`timestamptz` does. **A time you hand in is left exactly as it is**:
`DateAuthenticated` is when somebody proved who they are and is never touched,
and an `Expires` or `Idle` you set is yours. If your store is a table, hand in
what a table keeps (`t.Truncate(time.Microsecond)`), or cut it where the store
writes it. Nothing stored changes.

## A time written is what the database keeps

**It applies to you** if your app's tests or code compare a time an `Add`
answered, or a time you handed to a write, with the nanosecond -- with what a
clock said rather than with what was stored -- or keep a version taken from an
`Add`'s answer, or look a row up by a time. Regenerate (`go tool pd gen`) with
`protoc-gen-orm-ent` at `v0.0.0-20261128022808-7cf4f3348db4` or later
(protobuf-orm/protoc-gen-orm-ent#28) and this payday; `go get -tool
github.com/protobuf-orm/protoc-gen-orm-ent@main` brings the generator
(payday#324).

Every time the store writes is now cut to the microsecond, on SQLite as on
PostgreSQL, which keeps no more: the stamps (`date_created`, the version, an
erase's mark, whatever a `bare.WithClock` clock says), a time a caller hands to
an `Add` or a `Patch`, and what payday writes of its own -- the trail's archive,
what is owed an erasure, an entitlement's row and its window, the outbox and the
event it carries, the access record. An `Add` answered the row as it built it,
with the clock's nanoseconds, and on PostgreSQL that was a version no read gives
again; it is now the version a read gives, everywhere, and a test on SQLite sees
what PostgreSQL would. A read-back after an `Add` done only to have the stored
version is no longer needed. A version moves in steps of a microsecond, and two
writes of one row within one microsecond leave the same version.

Nothing stored before changes, and nothing is migrated: what is compared -- the
version a `Patch` tests, a filter, a cursor, a reference -- is compared as it is
sent, so a row written earlier, which on SQLite can hold nanoseconds, is read and
patched with the version a read gives of it, as before. The same rule means a
reference by a time names what is stored: a lookup by a time you handed in with
digits below the microsecond finds nothing on SQLite, and on PostgreSQL only
where the driver cuts the parameter. Look a row up by its key or by the time the
store answered. Two rows of a unique index over a time that are less than a
microsecond apart now collide on every database, as they always did on
PostgreSQL.

A test that compared an answer with a clock's nanoseconds compares it with the
time cut: `at.Truncate(time.Microsecond)`.

## A recorder's failure keeps its cause

**It applies to you** if your app reads the error of a write to decide what to
do with it -- tries a transaction again when the store aborted it, tells a
deadlock from a refusal -- or counts the `Internal`s it answers. Regenerate
(`go tool pd gen`) with `protoc-gen-orm-ent` at
`v0.0.0-20261127181046-80ed333182e8` or later
(protobuf-orm/protoc-gen-orm-ent#27); `go get -tool
github.com/protobuf-orm/protoc-gen-orm-ent@main` brings it.

The error a write answers when a recorder fails -- the trail, the outbox, the
access record, or one of yours -- was a status whose words were all that was
left of what the recorder returned. It now unwraps to it: `errors.As` and
`errors.Is` reach the driver's error and the context's, as they do for an
error of the row's own statement, so a deadlock reads the same whichever of
the transaction's statements the database aborted. And a caller who left while
the recorder wrote is answered `Canceled`, or `DeadlineExceeded` for a deadline
that passed, where it was `Internal`; a context's error from a recorder on a
call still live stays `Internal`.

Nothing on the wire moves otherwise: the code is `Internal` and the words are
*record the write by <the RPC>: <the recorder's error>*, byte for byte as
before.

## A server's `Close` closes its `Watch`

**It applies to you** if your server's `Close` does not close its `Watch`. The
scaffold's has since the NATS broker (payday#279, `a2d0d83`):

```go
func (s *Server) Close() error { return errors.Join(s.Watch.Close(), s.Db.Close()) }
```

An app scaffolded before that, or one that wrote its own `Close`, does not --
two apps are in that state as this is written. Add `s.Watch.Close()`, before
the database pool is closed.

`(*watch.Watch).Close` closes the broker it was built on, and the `postgres`
broker can now be closed: it ends its listener and its publisher and closes
their connections. Without that, on `watch.broker: postgres`, every server
built kept two connections and two goroutines until the process exited --
nothing grows in a deployment that builds one server, and a test suite that
builds one a test ran out of PostgreSQL's connections about thirty tests in.
The `memory` broker closes too, and ends the subscriptions it handed out.

## A caller who left is answered `Canceled`, by `auth` too

**It applies to you** if the app has an `auth.TokenStore` of its own (or an
`auth.Resolver` that reads a store) and it returns `auth.ErrUnavailable` for a
read its caller cancelled, so that the caller is not told `Unauthenticated`.
It need not any more, though it may: with the call's context ended, whatever
the store returns ends the call `Canceled` or `DeadlineExceeded`, and returning
the context's error is enough. Nothing breaks if it is left as it is. A
cancelled call writes no record of the server's own; a passed deadline writes
the one it wrote before, which names the store that was slow.

## A service overlay no longer says a generated field again

**It applies to you** if a `proto/ext/<pkg>/*_svc.ext.proto` declares a
message the generated contract has -- an `Add` request, a `List` request --
and says in it a field that message already has: at a number it has, or under
a name it has at another number. `go tool pd gen` now refuses it, naming the
file, the message, the number and both lines:

```
machine_svc.ext.proto: message MachineAddRequest says field 9 again, which the contract's message already has.

    the contract's: int64 hours = 9 [features.field_presence = IMPLICIT];
    the overlay's:  int64 hours = 9 [deprecated = true];

The merge would take the overlay's field whole -- its type, its presence and its options -- and keep nothing the generator said of it: what the generator wrote, and every layer and client written against that, would be reading another field. Nothing would say so.

An overlay **adds**. Add what is wanted at a number MachineAddRequest leaves empty, under a name it does not use; or, if a different request is wanted, give it an RPC of its own under its own name
```

Before, the merge took such a line in place of the generator's field and `pd
gen` said nothing: the field had the overlay's type, presence and options, and
lost whatever the generator had said that the overlay did not.

**What to do:** take the line out. If something was wanted beside the
generated field, add it as a field of its own at a number and a name the
message does not have. If a different request was wanted -- another type,
another meaning -- declare an RPC of your own with its own request, as the
[schema guide](guide/schema.md#an-rpc-of-your-own) shows.

An overlay that only adds -- new fields at new numbers, messages and RPCs of
its own -- is untouched.

## The page package's floor is 0.1.6

**It applies to you** if the app has a page (`ts/package.json`) and its
`@lesomnus/payday` is older than 0.1.6. Until 0.1.6 was out, a caret of
`^0.1.2` through `^0.1.5` installed 0.1.5. `go tool pd doctor` says so:

```
ts has @lesomnus/payday 0.1.5, older than 0.1.6 -- payday's half of the page: on an older one a list opens a Watch the server refuses for every list over a tenant, an edge or a field, and reads each such list twice

    go tool pd upgrade
```

Before 0.1.6, a list query opened a `Watch` over whatever filters the list
had. A generated `Watch` takes only filters that each name a row and refuses
the rest with `InvalidArgument`. So every such list was refused at once, read a
second time, and never live. Nothing broke, but the server logged a refusal for
each list shown. From 0.1.6, a list opens a `Watch` only over the rows it names.

`go tool pd upgrade` moves the page to `^0.1.6`, installs it, and says why as
it does:

```
pd: @lesomnus/payday 0.1.5 -> ^0.1.6: below 0.1.6, payday's half of the page: on an older one a list opens a Watch the server refuses for every list over a tenant, an edge or a field, and reads each such list twice
```

An app that meets this floor coming from a payday before 2026-11-23 meets the
rename of a grant's end in the same run, in its page as in its Go:
[`date_expires` is `date_expired`](#date_expires-is-date_expired), below.

## An RPC of your own no longer answers a secret

**It applies to you** if an entity declares a field `(payday.field) = {secret:
true}` and an RPC of yours answers that entity, itself or inside a message of
yours -- or a generated verb of another entity answers it through an edge.

The Secret layer cleared a secret from the generated verbs of the entity that
declared it, and passed everything else through. It now clears one from every
answer that can hold it, at any depth and in each message a stream sends; see
[a field about a person](guide/schema.md#a-field-about-a-person). So an RPC
that answered a secret through the entity's message no longer does, and a
client that read it there reads an empty field.

- **An RPC meant to hand a secret over once** -- a key, at the moment it is
  made -- answers it in a field of the answer's own, beside the entity rather
  than in the entity's secret field. Only an entity's field can be declared
  `secret`, so the layer leaves that one alone.
- **A handler that cleared the field by hand** before answering may stop: the
  layer clears it on the way past.

## `crud` is gone, and so is an `Apply` nothing asked for

**It applies to you** if an `(orm.message)` of yours, in `proto/` or in an
overlay under `proto/ext/`, says `crud` in its `rpc`; if anything of yours
calls an `Apply` -- Go, a page, a test, a script; or if a layer, a policy or a
test of yours names one. `orm` no longer has `crud`
(protobuf-orm/protobuf-orm#4), and the generators payday builds on write each
verb only where the entity names it (payday `809d6b8`, `38c291d`, `4d273bd`).

- **Each verb is named.** `rpc: {crud: true}` was
  `rpc: {add: {}, get: {}, patch: {}, erase: {}}`, and what a verb takes is
  said inside it as before (`add: {many: {}}`, `erase: {hard: {}}`); a list
  and a watch are declared where they were. `go tool pd gen` and `go tool pd
  doctor` refuse a schema that still says `crud`, before buf runs, naming each
  place and the line to write instead, where buf alone would say only `cannot
  resolve message field name for orm.RpcOptions`:

  ```
  proto/app/thing.proto:12: says `crud` in an (orm.message)'s rpc
  ```
- **A verb not named is not served**, so a `disabled: true` that turned off one
  of `crud`'s four goes with it: name the ones you serve and leave the line out.
  An overlay of payday's `Holder` or `Tenant` is the one place `disabled` still
  means something, since payday names their verbs
  ([switching a verb](guide/schema.md#switching-a-verb-of-paydays-holder-and-tenant)).
- **An `Apply` is there only where the entity says `apply: {}`.** It came with
  every `Patch`; after `pd gen` an entity without the word has no
  `<E>ApplyRequest`, no RPC, no client method and no command. Search the app's
  code, its page and its tests for `Apply(`, `ApplyRequest` and
  `.method.apply`, and give `apply: {}` to the entities something calls it of.
  An `Apply` and no `Patch` is a shape of its own: `rpc: {add: {}, get: {},
  apply: {}}`.
- **payday's own entities serve none.** `Holder`, `Tenant` and a past's `Fact`
  have no `Apply`; `Audit` has `Add` and `Get` and none of `Patch`, `Apply` or
  `Erase`, which its layer refused to everybody. An overlay of `Holder` or
  `Tenant` asks for the `Apply` it calls with `rpc: {apply: {}}`.
- **What was written about an `Apply` nothing serves goes.** A layer's
  override of `Apply` on an entity that has none now overrides nothing and is
  never called, and a refusal of one -- in a layer, a policy rule naming
  `/<pkg>.<E>Service/Apply`, a test that it is refused -- is about an RPC that
  is not there. Delete them; a test that calls one no longer compiles.
- **`buf.build/patch/patch` may be unused.** A contract imports
  `patch/patch.proto` only for an `Apply`'s document, so an app none of whose
  entities says `apply: {}` may drop it from `deps` in its `buf.yaml`.
- **An `Erase` the database refuses** because another row still names the row
  is `FailedPrecondition`, where it was `Unknown` in the driver's words.

[Upgrading](guide/upgrading.md#8-onto-the-payday-where-each-verb-is-named) is
the same move in the order to do it.

## A `select` answers only what it names

**It applies to you** if your code, your page or your tests call a `Get` with a
`select` and read a field of the answer the select did not ask for, or send a
`List` request with a field 6 of their own. A `Get` or `List` with no `select`
answers every field, as before (payday `b678e55`, `4c03f01`, `6eac964`).

A `Get` that selected some fields answered every other field as its zero, read
from a row the query had not read it from: a `string` empty, a timestamp a
message of year one (`"dateUpdated": "0001-01-01T00:00:00Z"`), and sent back as
a `Patch`'s version it was refused. What the select did not ask for is now
**cleared** from the answer -- through the Sink, so on every port and for every
caller in the process: a column whose switch is off, unless `all` is on, and
inside an edge the select fills, what that edge's own select did not name, to
any depth. The identifier, an edge nobody selected (an identifier alone, as
before) and a field the select has no word for are left as they were.

- **A message field reads nil.** `GetDateUpdated()` is nil where it was a
  timestamp of year one, and `GetDateUpdated().AsTime()` is 1970-01-01, the
  zero of a nil timestamp, where it was `0001-01-01`. Over JSON the field is
  absent. A scalar reads its zero, as it did.
- **How to find such a read:** every `Select` your code builds -- in Go a
  `<E>Select` handed to a `Get` or a `List`, in a page the `select` of a query
  -- against the fields read off what it answers; and the tests that compare
  such an answer with `time.Time{}`, `0001-01-01` or a whole message. Select
  what is read, or `all: true`.
- **A `List` takes a `select` too**, at field 6 of every `<E>ListRequest`, and
  each row is what a `Get` of it with that select reads. An overlay that adds a
  field 6 to a list request is refused by `pd gen`; move it to a number the
  request leaves empty.
- **A select goes eight levels deep at most**, the entity's own fields the
  first and each edge one more; a deeper one is refused before anything is
  read, `InvalidArgument`: *select: 9 levels deep, and a select goes 8 at
  most*.

## An error that carries no status is answered `Internal`

**It applies to you** if a handler, a layer or an interceptor of yours returns
an error that is not a gRPC status -- `errors.New`, `fmt.Errorf` without a
status inside it, an error of a store or a library handed up as it came -- and
somebody reads what the caller is told: a page that shows an error's message,
a client or a test that compares the code or the words (payday `df29a8d`).

gRPC answered such an error `Unknown`, in the error's own words, so a sentence
written for the caller reached the caller -- and so did a store's, naming its
database, user and address. The outermost step of every chain `grpcx.Serving`
begins now answers it itself, and nothing of its words reaches the caller:
`Unavailable`, *the server could not reach what it stores in; try again*, where
the store or a connection was not reached; `Canceled` or `DeadlineExceeded`
where the call's context ended; `Internal`, *the server failed to answer this;
it is on its log*, for everything else. The error is logged once at `ERROR`
([what a handler's error becomes](guide/server.md#what-a-handlers-error-becomes)).

- **A sentence meant for the caller is a status.** Give it the code that says
  what kind of answer it is -- `status.Error(codes.FailedPrecondition, …)`, or
  `pderr`'s refusals, `pderr.Invalidf` for a field
  ([refusals](guide/errors.md#1-refusing-on-the-server)). A status wrapped in
  `fmt.Errorf("…: %w", err)` passes, in the wrapper's words.
- **How to find them:** the `return` statements of handlers, layers and
  interceptors whose error is `errors.New(` or `fmt.Errorf(` with no `%w` of a
  status, or a sentinel `var Err… = errors.New(…)` that reaches a caller; and
  the tests and page code that expect `codes.Unknown`, `Code.Unknown` or an
  error's words.

## TLS material is a value, not a file's name

**It applies to you** if a configuration of yours says `server.tls` or a
dial's `tls` (a `config.DialConfig`), or your own configuration types embed or
build one (payday `6365b6e`).

| was | is |
| --- | --- |
| `server.tls.cert_file`, `key_file`, `client_ca_file` | `server.tls.cert`, `key`, `client_ca` |
| a dial's `tls.ca_file`, `cert_file`, `key_file` | `tls.ca`, `cert`, `key` |
| `config.TlsConfig.CertFile`, `KeyFile`, `ClientCAFile` (`string`) | `Cert`, `Key`, `ClientCA` (`ref.String`) |
| `config.DialConfig.CAFile`, `CertFile`, `KeyFile` (`string`) | `Ca`, `Cert`, `Key` (`ref.String`) |

Each new key holds the PEM itself, `${file:/path}` or `${env:NAME}`, so a file
that named a path writes `cert: ${file:/run/tls/tls.crt}`. They are read when
the listener or the connection is built, as the files were.

- **A configuration file** that still says a `_file` key is refused where it is
  read, `nothing reads this key`, before anything is served.
- **An environment variable** of an old name (`<APP>_SERVER_TLS_CERT_FILE`) is
  read by nothing, which the loader reports and does not refuse: a deployment
  that set its certificate only that way comes up without TLS. Rename it with
  the key (`<APP>_SERVER_TLS_CERT`, holding `${file:…}` or the PEM).
- **Your own types** that held a `config.DialConfig` or a `TlsConfig` follow
  the fields: Go that read `CAFile` as a path reads `Ca`, and one that set a
  path sets `Ca.SetFile(path)`.
- **Your docs, charts and examples** that say the old keys say the new ones.

## `date_expires` is `date_expired`

**It applies to you** if anything of yours reads or writes the end of a grant
or of an identity: Go, a page, a test, a script reading JSON, or a deployment
with rows in the app's copy of payday's `Entitlement` (payday `25bdd81`).

A grant's end is named for its event in the past tense, at the same number and
of the same type, so protobuf on the wire is unchanged and every name moves:

- **The messages:** `date_expired` in `EntitlementGrant`,
  `TokenIntrospectResponse` and payday's `Entitlement` entity, so `GetDateExpired()` in Go and
  `dateExpired` in JSON and in a page, where it was `dateExpires`. A script
  that reads `.dateExpires` out of an answer reads nothing.
- **payday's Go:** `auth.Identity.DateExpired`, `entitle.Row.DateExpired`,
  and authoidc's claims of the same moment. `go build` names each place.
- **A page:** its own generated code, `ts/gen/<app>/payday/entitlement_pb.ts`,
  which `pd gen --ts` writes; `npm run check` names each place as a property
  `dateExpires` that does not exist on `Entitlement`. The package's own copies
  of `EntitlementGrant` and `TokenIntrospectResponse` are not among its
  exports.
- **Stored rows:** the column of the app's copy of `Entitlement` is renamed
  and nothing is carried for it (`pd.ColumnRenames` holds none), so a
  migration on this payday makes `date_expired` empty beside the old one. A
  deployment with data renames it once, before it migrates:
  `ALTER TABLE entitlement RENAME COLUMN date_expires TO date_expired;`

A standard's names stay: a JWT's `exp`, `expires_in`, a cookie's `Expires`.

## The HTTP listener serves `server.tls` too

**It applies to you** if your app serves HTTP (`server.http`) and was made by
`pd new` before payday `364852e`, or wrote its own `serveHttp`, and a
deployment of it says `server.tls`. `pd gen` writes no `cmd/serve.go`, so the
change is yours to make.

The gRPC listener used `server.tls` and the HTTP one did not: with a
certificate configured, the browser port served plaintext and asked for no
client certificate. In `serveHttp`, resolve `c.Server.Tls.Server()` before
`net.Listen` and return its error, set what it answers on
`http.Server.TLSConfig`, and serve with `srv.ServeTLS(l, "", "")` when it is not
nil and `srv.Serve(l)` when it is -- what `pd new` writes now, and [serving a
browser](guide/server.md#9-serving-a-browser) says why. With an empty `tls`
block both stay plaintext.

## `pd upgrade` needs the page's generator first

**It applies to you** if your app has a page (`ts/package.json`) and runs `go
tool pd upgrade`, with or without `--payday`, in a checkout where `ts` has not
been installed: a fresh clone, a CI job, a worktree.

The page is generated with the rest, so `pd upgrade` asks for its generator
(`protoc-gen-es`, in `ts/node_modules`) before anything moves, and stops at
once without it:

```
protoc-gen-es is not installed: the page is generated with the rest, so its generator has to be installed first -- npm ci, in ts
```

Run `npm ci` in `ts` first; where `ts/package.json` does not list
`@bufbuild/protoc-gen-es`, the line says to install it. It has been so since
payday `a9a2ebe`, and is entered here because apps moving in November 2026
met it all the same; see
[upgrading payday](guide/upgrading.md#4-upgrading-payday).

## A rename is moved before the migration looks for it

A migration finds a table, a column and an index each by its name, and drops
none of them. So a store an earlier build made holds what was renamed under its old
name, where the migration does not look, and it makes the new one, empty, beside
it: an entity's table with none of its rows, a column with none of its values,
an index built again from every row. Nothing fails, and the schema check a
server makes before serving passes. On SQLite a table whose index names a
renamed column is made again without the old column at all.

`pd gen` writes what was renamed, for an app's `Migrate` to move first:

| | what it holds | moved by |
| --- | --- | --- |
| `pd.TableRenames` | the table of each entity the schema renamed (`(payday.entity).formerly`) | `rename.Tables` |
| `pd.ColumnRenames` | each column of payday's own entities that payday renamed -- none so far | `rename.Columns` |
| `pd.IndexRenames` | each index stored under another name -- none so far | `rename.Indexes` |

`pd new`'s `Migrate` hands each over, in this order, before anything is made:

```go
if err := rename.Tables(ctx, s.Db, s.Dialect, pd.TableRenames); err != nil {
	return err
}
if err := rename.Columns(ctx, s.Db, s.Dialect, pd.ColumnRenames); err != nil {
	return err
}
if err := rename.Indexes(ctx, s.Db, s.Dialect, pd.IndexRenames); err != nil {
	return err
}
if err := valid.Indexes(ctx, s.Db, s.Dialect, entmigrate.Tables); err != nil {
	return err
}
if err := valid.Columns(ctx, s.Db, s.Dialect, entmigrate.Tables); err != nil {
	return err
}
return entmigrate.NewSchema(s.Drv).Create(ctx)
```

- **The tables first**, because the other two look for a column and an index on
  its table by the name the table has now, and find nothing on one still under
  its old name.
- **Each is the same call on any store**: one an earlier build made, one this
  build made, an empty one, and one it has moved already. What is not there
  under its old name is left alone.
- **A table** moves with its rows: on PostgreSQL with its indexes, its primary
  key, the foreign keys and (on 18) the NOT NULL constraints named after it,
  in one transaction of changes to the catalog; on SQLite renamed in place, its
  old indexes dropped for the migration to make again, while a foreign key keeps
  its name there, which nothing reads. A store holding both tables, the new one
  empty, has the empty one dropped; one with rows in both is refused
  (`rename.ErrBoth`).
- **A column** is renamed with what it holds: one change to the catalog on
  PostgreSQL, the table's definition rewritten in place on SQLite. One a
  migration already added beside the old takes what the old one held wherever
  it is empty, and the old one is dropped.
- **An index** is renamed on PostgreSQL, which builds nothing. SQLite renames
  none, so the old one is dropped and the migration makes the new one.
- **`pd doctor` says so** of an app with a renamed table or column whose own Go
  hands it to nothing.

What else an earlier build wrote down under an entity's old name reads as the
entity. `pdid.Formerly` registers each old word and full name, so `DomainOf`
and `Lookup` answer the entity's domain after what is registered now; the trail
reads a chunk of its archive labelled with an old word as the kind it was --
checked with its rows, cut by its window, and counted under its name now; and
`audit.by.<old word>` goes on configuring the kind, though a file that names one
kind under two of its words is refused where the process comes up.

[The schema guide](guide/schema.md#formerly--when-you-rename-an-entity) is how
to declare a rename.
