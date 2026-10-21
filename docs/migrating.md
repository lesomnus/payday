# Migrating

What an app has to change when payday does. Newest first, and each entry says
how to tell whether it applies to you.

## An order is over columns a cursor can carry

**Applies if** an entity's `list:` orders by a column that may be null, a
`secret:` field, a list, a map, JSON or bytes -- in `order` or in a named
order. `pd gen` refuses it now, naming the column; nothing changes for any
other list.

- **A nullable column** skipped rows: a null is neither past nor before a
  cursor. Order by a `default`ed column, or leave it out of the order.
- **A `secret:` field** went out in every page's `next`.
- **A cursor is read back as the column's type.** It was read into `any` for
  every column but a string, a time and an identifier, so an int64 past 2^53
  came back as a neighbour and a page repeated its last row. A cursor a client
  holds carries on: its bytes are what they were.
## The gate asks an `Apply` about the edges it moves

**Applies if** an entity has an edge that can move onto a row behind the wall
-- one that is not `immutable`. Regenerate; nothing else changes.

- **The generated `Gate` has an `Apply`** beside its `Patch` for each such
  entity, payday's holder among them when an overlay gave it one. It compiles
  the document as the server behind does and reads, through the wall, the row
  each moved edge is given; one the caller cannot see is `NotFound`, as at
  `Patch`. Before, a document moved an edge onto any row by its key, and the
  row was read back through the caller's own one hop later.
- **A document the gate cannot compile is refused there**, with the code the
  server behind gives it: `InvalidArgument`, or `Unimplemented` for what this
  engine does not do.
- `Apply` is still closed at the transport unless `allow_general_writes` opens
  it; this is the rule about the row that holds wherever it is called from.
## A broker and a lease are built on the process's context, and `PlainInProcess` believes only this process

**Applies if** your `cmd.Build` or your serve command builds a broker or a
lease -- every app `pd new` made does -- if you register a broker or a lease of
your own, or if you serve a server with `auth.PlainInProcess()`.

- **`c.Watch.Build(ctx, c.Db)` and `c.Spin.Build(ctx, c.Db)`.** Both take the
  context first: `cmd.Build`'s for the broker and for the trail's lease
  (`p.Lease`, the entry below), the serve command's -- after `Telemetry` -- for
  the lease the loops run under. What they say later, a listener that stopped
  or a hold lost with its connection, is said in the log that context carries;
  it went to the OpenTelemetry global, which nothing installs.
- **A registration is handed that context first**:
  `config.RegisterBroker(name, func(ctx context.Context, c config.WatchConfig, db config.DbConfig) (watch.Broker, error))`,
  and the same for `config.RegisterLease`. Dial under it, and keep what it
  carries rather than its cancellation (`context.WithoutCancel`).
- **`auth.PlainInProcess()` refuses a name told from outside this process.** A
  call is believed when its peer is `bufconn`; one over a port, a socket or the
  HTTP transcoder is Unauthenticated (`auth.ErrFromOutside`). A server served on
  `bufconn`, which is what it was for, changes nothing.
- **A loop you rename can say what it was called**: `spin.Formerly(old, s)`.
  Under a lease it holds that name as well as its own, so a rolling upgrade
  that runs the build before beside this one runs the loop in one of them.
  Nothing to do for a loop you have not renamed.

## A pass and what erases from the trail take turns, and a tenant that left has no key made again

**Applies if** you keep the trail's archive and erase from it -- `Policy.Forget`,
`Policy.PurgeTenant`, `Policy.Collect` -- or seal it (`audit.keys`). Part of
payday#115.

- **Hand the trail's policy the deployment's lease**: `p.Lease =` the
  `spin.Lease` that `spin.lease` names, built beside the policy -- `pd new`'s
  `Build` does, unless it is `none`. A pass then holds `trail.Turn` for each
  chunk's worth of rows it moves, and the erasures change the database's rows
  in their own turn, so a pass can no longer write back what one blanked or
  removed a moment before. Without it nothing takes turns, which `p.String()`
  says. Never hand it `spin.Nowhere()`: an act would wait for ever.
- **An erasure called inside a transaction of yours waits for a pass's chunk**
  while it holds that transaction. On SQLite that can make the pass's chunk
  fail once (`database is locked`), to be moved again by the next pass.
- **A tenant that leaves leaves a mark in the key store** where its keys were.
  A row written before it is sealed to the tenants it names that are still
  there; one that names nobody still there stays in the database, for the
  tenant's purge, and is not archived. Nothing to change; a key store's
  listing shows one more blob per tenant that left.

## The trail's archive takes in only what it can account for

**Applies if** you keep the trail's archive (`audit.archive`, or
`trail.Policy.Archive`). Nothing in your code changes shape; four things the
archive did change (part of payday#115).

- **Files of the directory format are adopted by the first pass alone**: the
  one before the archive's first checkpoint. A file of that format put in
  `audit.archive` after it is `unaccounted` to `trail verify`, and is read,
  filed and signed by nothing until an operator accepts it
  (`policy.Accept`). A deployment that upgraded from the directory format has
  had its first pass; one that restores an old file into the directory now
  accepts it once.
- **A chunk whose bytes are not its digest is written again by nothing.** A
  fold, `Policy.Forget`, `Policy.PurgeTenant` and the filing of an old file
  read what they copy through its digest, and stop at a chunk that is not it:
  `trail verify --full` says `altered`. `Forget`, `PurgeTenant` and `Purge` go
  on with the rest, write their receipts, and answer an error naming each such
  chunk, which is `trail.ErrAltered` -- what a verified read answers for one.
  An erasure that answers it has done everything else: look at the chunk, and
  erase again once it is settled.
- **A discard window destroys what the archive holds of it**, once it is as
  old as `retain`: what a legal hold sent there instead of discarding it, once
  the hold lifts, and what a longer window kept there before it was shortened
  to a discard. It used to be kept for ever. `policy.Preview` counts it.
- **`pd gen` refuses a field of `Audit` or `Access` named or JSON-named
  `sealed`**, the member a sealed row keeps what it said in
  (`trail.SealedMember`). An overlay with one renames it.

## A page's store is named by what it sees, and every call it makes says so

**Applies if** a page of yours uses `@lesomnus/payday`'s store or query layer,
or your server serves `auth/authsession` -- `Sessions.Handler`, `Serve`, or
`IdentityOf`.

- **`authsession.IdentityOf(v Session)`** digests what the session lets its
  caller see -- the actor, the tenant, the grant -- where it took the key.
  `GET /session`'s `identity` is that, so one person's sign-ins with one grant
  share a store. Mirrors named the old way are other names now, and go with the
  rest below.
- **Every call `Queries` makes carries `payday-identity`**
  (`authsession.IdentityHeader`, `IDENTITY_HEADER`), and `Sessions.Handler`
  refuses a call whose session that store is not named by, as
  `ErrOtherIdentity` -- `Unauthenticated` on the wire, with the cookie kept. A
  call that names no store is served as before. `web.Cors` allows the header;
  a CORS list of your own has to.
- **A write refused `Unauthenticated` waits** rather than being refused, and
  goes once its caller signs in again. So a page that forgets its store on a
  401 throws away what this keeps for it: let go of the store and ask again who
  is signed in, and forget only on a sign-out, in every tab. `pd new`'s
  `ts/src/session.tsx` is the shape.
- **`deleteOtherDisks(at)`** in `@lesomnus/payday/store/idb` deletes every mirror
  of the app but `at`'s. Ask for it once the caller's own is open, and do not
  wait on it.
- **A mirror lets go of itself** when another page deletes it or moves it to a
  later version, and what then needs the disk fails saying so. `openDisk` and
  `deleteDisk` reject, rather than wait, while a page from before this holds one
  open.
- **`send` refuses a patch** -- a request carrying a `patch.Patch`, as `Apply`
  does -- to an entity that declares a secret. Send those with `call`.
- **`Sessions.KeyOf`** answers nothing for two cookies of its name, and reads a
  cookie line a pair at a time. The `GET` and the `DELETE` read through it too.

## The access record: a write's row in its transaction, a download alone, a read's request only

**Applies if** you wired the access record (the entry below). Regenerate; what
changes is what it does, and one thing of your `serve` (payday#111).

- **A write's row is in its transaction even where nothing waits for a
  commit** -- a write the servers make in a transaction of their own. A write
  whose row cannot be written is refused, rather than made and recorded
  nowhere.
- **A download is refused as an operation of a batch.** It says what it is
  for and is recorded before its answer leaves, which an operation cannot do.
  Call it by itself.
- **A row keeps a read's or a download's request, and nothing of a call that
  wrote or was refused.** What the request's message does not declare is
  dropped, and one larger than `access.MaxRequest` is not kept.
- **A reason is text**, at most `access.MaxReason` bytes; anything else is
  refused.
- **An `Any` is read only where payday packs one** -- a batch's operations and
  answers, a watch's events -- and once. One your rows hold is not read for
  whose data it carries.
- **The records payday keeps are no operation of a batch**: the trail, the
  outbox, the archive's account, the access record.
- **Write the queue once more after the server has stopped.** `pd new`'s
  `Serve` waits for `GracefulStop` and calls `s.Access.Flush`: what the server
  answers while it drains is queued after the writer's own last write.

## An access record beside the trail

**Applies if** you generate with `pd gen`: `Access` is a new payday entity, copied
in like `Audit`, so migrate. Nothing is recorded until it is wired (payday#111).

- **The recorder**: `pd.AccessRecorder()` in the `bare.Recorders` the servers
  are built with, after the trail's. It writes a write's row at its commit.
- **The writer**: `w := &access.Writer{Sink: pd.AccessSink(client)}`, appended
  to what `spin.Run` is handed -- it is each process's own, and names itself.
  A read's row waits in it for a second.
- **The interceptor**: `access.Config{Subjects: pd.Subjects, Refs:
  pd.SubjectRefs, Address: ..., Downloads: ..., Writer: w}.Interceptor()` in the
  chain, after `auth` and the limiter and before `gate.Interceptor`.
- **`Address`** is where a call came from. `access.Peer` is the connection's,
  which is the caller's only when nothing stands in front of the app; behind a
  proxy, answer with what the deployment trusts of its headers.
- **`Downloads`** are your methods that hand personal data out to be kept
  somewhere else, by full name. Each is refused without a `payday-reason-bin`
  header, so a client of one sends it: a gRPC client sets the text, and a
  page or `curl` writes it base64, since it is a `-bin` key (connect-es has
  `encodeBinaryHeader`).
- **`access.Saw(ctx, ...)`** in whatever shows a person's data where no
  `personal` field carries it: a name rendered into text, a column of a file.
- **Retention**: rows are a kind of the trail's, `access`, on `audit.by.access`.
  A deployment under PIPA says `profile: pipa` or `pipa-sensitive` there.
- **`pd new`'s `cmd/serve.go` has all of this**, to compare against.

## An entity no server writes says how its writes reach the trail

**Applies if** an entity of yours has no `Add`, `AddMany` or `Patch`: `pd gen`
refuses it until it says (payday#108).

- **`trail: {recorded: {}}`**: the app writes it through ent and records each
  write with `pd.Record`, in the transaction that wrote it. A check of the rows
  reads it as any kind, and a write that was not recorded is a row nothing on
  the trail is about.
- **`trail: {off: {why: "..."}}`**: the app writes it and keeps it off the trail
  -- a session's idle clock, a lock, a queue a loop empties. A check does not
  read it, `pd.Record` passes over it, and `why` is required.
- **On an entity a server writes**, `trail` is refused: the recorder puts its
  writes on the trail already. A write of yours through ent on such an entity
  is still recorded with `pd.Record`.
- **This replaces the rule the entry below came with** -- a kind with no `Add`
  or `Patch` was left out of the check for having none, which left out a kind
  kept to prove something, such as a consent. Whether an entity has RPCs is now
  only what its API offers.

## A check of the rows reads the archive, and what nothing accounts for is a finding

**Applies if** you call the generated `pd.Digests`, or write rows beneath your
servers -- a restore, a migration, anything through ent on a kind that has a
generated `Add` or `Patch` (payday#108).

- **`pd.Digests(ctx, db, digest, policy)`** takes the trail's policy, and reads
  its archive for a row whose newest line of the trail has left the database.
  `trail.Policy{}` reads none. `db` is the database: each page is read in a
  read-only transaction of its own.
- **`Rows.NotOnTrail` is the rows, not a count**, and `Rows.Ok()` is false while
  there are any: a row nothing on the trail is about is what a row added past
  the servers looks like. `Rows.Archived` says how many were checked against the
  archive.
- **`pd.Record(ctx, db, contents, action, ids)`** puts rows you wrote beneath
  the servers on the trail as the act that wrote them: call it in the
  transaction that wrote them. Give the operator a command that records what
  `pd.Digests` found once somebody has looked.
- **An entity may declare no RPC** -- `option (orm.message) = {}`, with no
  `rpc` -- and no layer wraps a method it does not have (the gate, the secret
  layer, the recorder's reads), which is new; before this such an entity did
  not compile. How its writes reach the trail is the entry above.
- **`audit.CheckRows` and `audit.Newest` are gone**; `audit.CheckKind` is what
  the generated code calls.

## A row of the trail keeps a digest of the row its write left

**Applies if** you generate with `pd gen`. `Audit` has two new columns,
`digest` and `digest_fields`, so migrate. Rows written before keep neither, and
nothing is digested until `audit.digest.key` names a key.

- **`audit.digest.key`** (`<APP>_AUDIT_DIGEST_KEY`) is base64 of at least 32
  random bytes (`openssl rand -base64 32`). Give it as the file it is in, or as
  `base64:` and the key. Keys rotated out go in `audit.digest.previous`.
- **The generated `pd.Digests(ctx, db, digest)`** checks the live rows against
  the trail. Give the operator a command that runs it.
- **An overlay on `Audit`** is written a row at a time, as before.

## `pd doctor` asks about a field that may hold a person

**Applies if** an entity of yours has a field declared `personal`, or a field
whose name is about somebody (`email`, `phone`, `address`, `birthday`,
`first_name`, ...). Nothing refuses anything; `pd doctor` says more than it did.

- **`(payday.field).personal` has presence**, so `personal: false` says a field
  was looked at and is about no one in particular -- a role, a state, a kind --
  and is a different statement from saying nothing. The trail treats the two
  alike.
- **`pd doctor` asks of every field of free text on an entity that holds people**
  -- one with a personal field -- and of any field named for something about a
  person, whether it is one. Answer each, beside it: `personal: true` or
  `personal: false`. The finding names the file and line.

**Ordering**: an app compiles its schema against `buf.build/payday/payday`, so
`personal: false` means anything only once the module it pins has the presence.
Until then it compiles, says nothing, and `pd doctor` keeps asking. `buf dep
update` moves the pin. (An `impersonal` field said this for a few hours before
it; nothing wrote it, and it is gone.)

## The page is held to payday's floors, and `pd upgrade` meets them

**Applies if** `go tool pd versions` marks `@bufbuild/protobuf` or
`@bufbuild/protoc-gen-es` with `!`: `pd gen --ts` refuses to write until they
are at payday's floor (`pdcli.ErrBelowFloor`).

- `go tool pd upgrade` moves every page package that is below its floor to the
  version `pd new` writes, along with the packages it moves with. It runs
  `npm` and commits nothing. `pd doctor` lists the other floors, and those
  refuse nothing ([upgrading](guide/upgrading.md)).
- Upgrading payday is `go tool pd upgrade --payday <commit>` now. It runs
  `go get` and `go mod tidy`, then the new payday's own upgrade and
  generation.
- The template's lower bounds are real releases now. The old `vite: ^7.1.14`
  named a version npm never published, so an app still declaring it with
  nothing installed is read as below Vite's floor (7.2.0).

## An index is in the order it declares, and a tenanted list's begins with the tenant

**Applies if** you generate with `pd gen`, which is every app: the ent schema
of any index that declared an edge before a field changes, and so does the
database the migration makes (lesomnus/rove#122).

- **An index's columns are in the order its refs are.** protoc-gen-orm-ent wrote
  every edge after every field, so an index declared `(party, issued_at)` was
  `(issued_at, party_id)` -- the wrong one for the reads it was declared for.
  After `pd gen` it is `(party_id, issued_at)`, under the name of those columns,
  and the next migration makes it beside the old one -- which it leaves:
  ent's migration drops no index, and the old one goes on being written on
  every insert and read by nothing. Drop each old name once the migration has
  made its replacement; on PostgreSQL `DROP INDEX CONCURRENTLY IF EXISTS
  <name>`, one statement at a time and outside a transaction, which blocks no
  read or write while it waits out the transactions already using the table --
  a plain `DROP INDEX` takes the table's strongest lock and queues every query
  behind it. rove's and roster's `Replaced` lists are what that looks like. An index whose fields all came before its edges -- `(alias,
  tenant)`, say -- is as it was.
- **`pd gen` prints the warnings it was given.** The generation's own warnings
  were dropped whenever it succeeded, so an app may see a list of them the
  first time. The one that matters here is the list index:

  ```
  protoc-gen-pd: app.Robot: list: no index begins with (tenant, date_created, id), ...
  ```

  An entity behind the wall is read narrowed to its tenants, so the index its
  list walks begins with the tenant. Put `tenant` first in its `page` index --
  `refs: [{name: "tenant", number: 2}, {name: "date_created", number: 15}, {name:
  "id", number: 1}]` -- and do the same for the index of each named order.
- **payday's own Holder and Tenant have a list index now**: `(tenant,
  date_created, id)` and `(date_created, id)`. They come with the copy `pd gen`
  makes; the migration adds them.

On a database with rows, each of these is an index built while the table is
locked for writes. PostgreSQL builds one on a million rows in seconds; a
deployment that cannot pause writes for that long builds them `CONCURRENTLY` by
hand first, under the names the migration expects.

## The trail keeps what a write said, and no value about a person

**Applies if** you generate with `pd gen`, which is every app: `pd.Recorder`
takes an argument now, so `cmd/serve.go` does not compile until it is given one
(#92).

- **`pd.Recorder` takes what the trail keeps of each kind's writes**, which is
  the deployment's to say (`audit.content`). It is asked for rather than
  defaulted, so that a setting is not something an app can read and then forget
  to hand over:

  ```diff
  -rec := bare.Recorders{pd.Recorder(), pd.ResourceRecorder(w, c.Watch.Outbox, nil, resource.RecorderOptions{})}
  +content, err := c.Audit.Contents()
  +if err != nil {
  +	return nil, err
  +}
  +rec := bare.Recorders{pd.Recorder(content), pd.ResourceRecorder(w, c.Watch.Outbox, nil, resource.RecorderOptions{})}
  ```

  `audit.Contents{}` is the default, for a test that builds a server of its own.
- **By default a row of the trail is the write's patch, and not its row.**
  `Audit.patch` is the document the write was compiled from, and an Add -- which
  was compiled from none -- is recorded as the row it made, written as a patch
  from the empty row; `Audit.value` is empty. A deployment that wants the row
  asks for it, for the whole trail or for one kind:

  ```yaml
  audit:
    content: patch            # fields | patch | value; empty is patch
    by:
      reading:
        content: value        # a machine's record, kept whole
  ```

  `fields` keeps which fields a write set and nothing they were set to. Anything
  of yours that read `Audit.value` for what an Add made reads `Audit.patch` now:
  applied to an empty row, it is that row less its key.
- **`Audit.fields`** is a new column, the numbers of the fields each write set.
  Migrate. Rows written before it have none.
- **`(payday.field).personal`** marks a field about a person -- a name, an
  address, a phone number. It is answered with like any other field, and on the
  trail as a value at **no** level: what it was set to is taken out of `patch`
  and cleared out of `value`, and its number is in `fields`. Declare yours. An
  entity that holds people and things in one table declares the shared field:
  there is no declaration that depends on the row. It is refused on an edge, on
  the key, and beside `secret`, which already says everything it would.
- **payday's `Holder` declares `alias`, `name`, `desc` and `labels` personal**,
  so after regenerating, a holder's trail says what was set and not to what. Who
  a line is about is still on it: `actor_id` and `object_id`.
- **A `secret` is matched by its name as well as its number.** A document an
  `Apply` was handed that named a secret field by its name or its JSON name
  carried the value onto the trail, the outbox and the in-process publish; it
  does not now.
- **`trail.Policy.Forget` is still what takes out what no declaration reaches**:
  free text, rows written before this, and a field nobody declared on a kind
  kept at `value`.

**Ordering**: `personal` is in payday's buf module, like `secret` was, so an app
depending on `buf.build/payday/payday` needs a version of it that has this
before its copy of `Holder` compiles. Until then the compile says the field
`personal` is not in `payday.Field`.

## An entity may ask for AddMany

**Applies if** you generate with `pd gen`. Regenerate; nothing else has to
change -- unless a layer of yours overrides `Add` on an entity you then give
an `AddMany`.

- **`rpc: {crud: true, add: {many: {}}}` adds `AddMany`** (#84): up to a
  thousand of an entity's `Add` requests in one call, written in one
  transaction, a statement for every few hundred rows. Each item is the row an
  `Add` of it would have been, and each is asked what an `Add` is: the gate,
  the Sink's name and tenancy, the Secret layer on the answer, a line of the
  trail. It comes from protobuf-orm (`orm`'s `rpc.add.many`), so the module's
  pin moves: `buf dep update` once `buf.build/orm/orm` has it.
- **What payday's layers say of an `Add` is a rule now**, put on the server
  behind with `bare.<E>WithAddRule`, so an `AddMany` asks it of every item. The
  generated `gate<E>.Add` and `sink<E>.Add` are `admit`; an `Add` that tries
  again for a name it made up is still an override, and so is an `AddMany`'s.
- **A layer of yours that overrides `Add`** says nothing about an `AddMany`,
  which comes from the server behind it. Write the rule with
  `bare.<E>WithAddRule` ([server guide](guide/server.md#a-rule-about-the-rows-an-add-writes)),
  or override `AddMany` as well.
- **Alone, an `AddMany` asks of each item**, as that many `Add`s would, and
  writes the trail a row at a time; in a transaction begun with
  `dialect.BeginTx` -- an operation of your own, or a batch -- each row named
  is asked once and the trail is written by the commit. A batch counts it as
  one operation.
- **An `AddMany` tries again for a name it made up**, as an `Add` does, but as
  a whole call, so a call of many is refused more often than an `Add` of one;
  see `slug.Tries`.
- **`pd gen` refuses an RPC `protoc-gen-orm-service` writes that payday does
  not know**: a layer inherits what it does not override, so a new one would
  have been served past the gate. And none of payday's own entities may ask for
  `AddMany`.
- **An entity with both an alias and a `stamp:` or `agrees:`** had only its
  name decided at `Add`, and its tenancy never asked. Both are now. Nothing
  this repository knows of declared both.

## A transaction remembers what it asked without being told to

**Applies if** a layer of yours calls `seen.For`. Delete the line and
regenerate.

- **`seen.For` is gone** (#84). What the gate and the sink were told about a
  row is kept by the transaction `dialect.BeginTx` began (`dialect.Keep`), so
  every such transaction asks each question once for the rows it names. Nothing
  is said beside `BeginTx` for it -- and a layer that left the line out asked
  everything again, with nothing to say it had.
- **The generated `Gate` keeps the driver it was put on**, in `WithDriver`:
  that is how it finds the transaction. A gate that was not put on one asks
  every time, as before.
- **`seen.Visible` and `seen.Tenant` take that driver**, and `Tenant` takes no
  context. Only generated code calls them.

## A transaction's trail is written by its commit

**Applies if** a layer of yours writes several rows in a transaction begun with
`dialect.BeginTx`, or reads or changes the trail inside one. Regenerate;
nothing else has to change.

- **The rows of the trail a transaction begun with `dialect.BeginTx` records
  are written together** (#84): five hundred to a statement as they are
  recorded, and the rest by its commit, where each was a statement of its own.
  In rove's import, with the row each write hands over (below), that was a
  third of what a row sent: 27.8 statements a row, and 18.8 now.
- **They hold or fall with the transaction as before.** The commit is what
  writes them, so a transaction cannot be ended with any left out, and one
  rolled back takes them along. What a row says is decided when the write is
  recorded, its `date_created` included.
- **A statement about the trail inside that transaction finds every row it
  recorded**, since what is kept is written ahead of it: a count, a list, the
  blanking an erasure does to a person's rows in the transaction that wrote
  them. What is asked is the statement's SQL, for the trail table's name as a
  word. A statement that reaches the table some other way -- through a view, or
  a function of your own -- finds only what has been written.
- **A trail an overlay added a column to is written a row at a time**, as it
  was: the recorder fills only payday's own columns, and a column it cannot
  fill is one only the generated server can.

## A write hands the trail the row it left

**Applies if** you generate with `pd gen`. Regenerate; nothing else has to
change. An app that runs `ent generate` itself adds one feature.

- **A line of the trail holds the row its write handed over, rather than one
  it read again** (#84). The recorder read the row after every write, which the
  write had just read itself -- a statement for every write. An Add's INSERT
  answers with the row now, in the same statement where the dialect has
  `RETURNING` (PostgreSQL and SQLite; on MySQL it is still a query), and a
  Patch or an Apply hands over the row it read back to answer with. Only an
  Erase is read as before. An import into rove sent 37 statements a row and
  sends 28.
- **ent generates with `sql/returning`**: the generated servers ask a create
  builder for the row it stored (`Stored`), which only a client generated with
  that feature has. `pd gen` passes it. Anything that runs `ent generate` over
  payday's schema itself adds `--feature sql/returning` beside `sql/modifier`.
- **A recorder of yours beside the trail is handed the row too**, since a
  `bare.Recorders` asks for every image any of its recorders asks for: an Add's
  and an Apply's `Change.Value` is set whether or not yours asked. It is the
  row whole -- the trail keeps a `secret:` field out of what it holds on a
  copy -- and the ORM's word holds: treat it as read-only.

## A transaction asks about each row it names once

**Applies if** a layer of yours begins a transaction and writes several rows in
it. Regenerate; nothing has to change, and one line makes it cheaper.

- **The gate's read of a reference, and the sink's read of its tenant, are
  asked once in a transaction begun with `seen.For`** (#84). Each was asked for
  every reference of every write -- an import into rove sent 68 statements a
  row, 33 of them those two questions, and sends 37. A generated batch
  begins its transaction that way already. A layer of your own adds the line
  beside its `dialect.BeginTx`, with the driver it began **on**:

  ```go
  drv, tx, err := dialect.BeginTx(ctx, s.drv)
  if err != nil {
  	return nil, err
  }
  defer tx.Rollback()

  ctx = seen.For(ctx, s.drv)
  ```

  Without it every question is asked as before. `seen` says what is remembered
  and why a write does not make it forget.

## An acceptance carries a restored database on, and is clean at once

**Applies if** you restore an app's database from a backup, or accept what
`trail verify` found. Regenerate; a `trail.Store` of your own changes as
below.

- **A database restored from before the latest checkpoint goes on once
  accepted.** It is `behind` -- it has numbered fewer checkpoints than the store
  keeps -- and no act takes a checkpoint while it is, as before. The acceptance
  that was to end that was refused for the same reason, so nothing took one
  again (#70). `Policy.Accept` now numbers its checkpoint after the newest the
  store keeps, by what each says, and names that one as the one before: the
  checkpoints from after the backup stay what the archive held, and the
  database goes on from there.
- **`Verify` is clean as soon as an acceptance is the latest checkpoint.** It
  compared the rows with the one before as well, which is what the operator had
  accepted they no longer add up to, so it went on finding that one `rewritten`
  until the next act took a checkpoint.
- **`Manifest.Checkpoint` takes `after`**: the next number is after the latest
  and after `after` as well, in the same transaction that takes it. The
  generated store has it once regenerated. One of your own moves its counter
  past `after` when that is higher:

  ```go
  func (s store) Checkpoint(ctx context.Context) (int, []trail.Archived, error)            // was
  func (s store) Checkpoint(ctx context.Context, after int) (int, []trail.Archived, error) // is
  ```

## A list reads the other way round, and in orders the schema names

**Applies if** an entity declares `list:`. Regenerate, and nothing else has to
change.

- **Every List request has `reverse`** (field 4), which reads the order every
  column the other way round. The index the order walks walks from the other
  end, so nothing has to be declared for it. The generated commands' `ls`
  (`pdcmd`) takes it as `--reverse`.
- **`list.orders`** declares more orders a request may name, each by a name
  and each ending in the key. A list that declares any has an `order` enum in
  its request (field 5); `--order NAME` names one at a terminal. Declare an
  index for each, in its directions; nothing checks that one is there.
- **A cursor is of the order and the way it was read**, and one carried on in
  another is refused. The cursor of the list's own order read forwards keeps
  the shape it had, so clients paging across the upgrade carry on.
- `list.orders` is a field of `payday.proto`. An app that takes payday's buf
  module from the registry (`buf.build/payday/payday`) can declare named
  orders once the registry has a version with it, and `buf dep update`
  brings it in. `reverse` needs nothing from the registry: it is a field of
  the generated contract.

## Every act ends in a checkpoint, and `trail.Archive` takes the policy

**Applies if** you call `trail.Archive`, or any act on the trail's archive —
`Policy.Forget`, `Policy.Purge`, `Policy.PurgeTenant`, `Policy.Collect`,
`Policy.Accept` — and read what it answers.

- **`trail.Archive(ctx, s, p, of, before)`** takes the policy where it took an
  archive, and writes into `p.Archive` as a pass does: sealed with `p.Keys`, its
  manifest's rows signed by `p.Signer`, ending in a checkpoint. It used to seal
  nothing and sign nothing, so in a deployment with a key its rows were
  `unsigned` until accepted. It still asks nothing of the windows or the holds.

  ```go
  n, err := trail.Archive(ctx, store, of, before, flob.NewOsStores(dir)) // was
  p.Archive = flob.NewOsStores(dir)                                      // is
  n, err := trail.Archive(ctx, store, p, of, before)
  ```

  Hand it the policy the deployment runs with, so that the checkpoint it ends
  in goes where the others do. A directory of the operator's own for `p.Archive`
  is a manifest that says the archive holds chunks it does not, and, with no
  `p.Checkpoints`, checkpoints in that directory and not beside the others.
- **Every act that changed the archive ends in a checkpoint**, as a pass does;
  one that changed nothing takes none, and nor does a question (`Doomed`,
  `PlanTenantPurge`, `Preview`, `Read`). What an act answers stands; when the
  checkpoint after it could not be taken, its error says so as well, joined to
  whatever else it says. A device that signs is asked once more per act.
- **No checkpoint is taken over rows that no longer add up to the latest one.**
  Once somebody has changed them, every act's error says so, until an operator
  accepts: `Policy.Accept` ends in a checkpoint of its own that says why
  (`Checkpoint.Accepted`). Nothing is settled meanwhile.
- **A loop that forgets one person per call** takes a checkpoint per person.
  Hand `Policy.Forget` everybody who is due at once where it can.

A `trail.Store` of your own changes nothing.

## A key in a device is a line of configuration

**Applies if** you hand in a `crypto.Signer` for a TPM, an HSM, Vault or a
cloud KMS as `trail.Policy.Signer`, or would rather not have to.

Nothing changes for a key in a file or the PEM document. `audit.checkpoints.key`
also names a key in a device now, by its URI, once the app imports what speaks
to the device — see [the runtime](runtime.md#keys-in-a-device):

- **Import the one you use**, in `cmd` beside the database driver:
  `config/signerpkcs11` (which needs cgo), `config/signertpm` or
  `config/signervault`. AWS KMS is a module of its own: require
  `github.com/lesomnus/payday/trail/trailawskms` and register it,
  `config.RegisterSigner(trailawskms.Scheme, trailawskms.Open)`.
- **A signer of your own** is registered the same way, under a scheme of its
  own, and is then configuration too; the code that set `Policy.Signer` can go.
- **A key the device would hand out is refused at start**: a PKCS #11 key that
  is extractable or not sensitive, a TPM key that is not `fixedtpm` and
  `fixedparent`, a Transit key that is exportable or allows a plaintext
  backup. Make one that is not; what the old key signed stays verifiable by
  its public half in `audit.checkpoints.trust`.
- **A key in a device signs every row of the manifest an act writes**, as the
  key in a file does, and not only a checkpoint: one across the network is a
  request a row, which a pass that moves many chunks makes many of.
- **A value that begins with a scheme an imported package registered** is that
  device's key, and not a file of that name.
- **`audit.keys.master` and `audit.keys.identity` read an age plugin's** too —
  `age1yubikey1…` and `AGE-PLUGIN-YUBIKEY-1…` — for a master in a device, run
  as `age-plugin-<name>` from PATH. The master is sealed to once at start now,
  which also refuses recipients age will not seal to together.

## The trail's manifest is signed, and its checkpoints chained

**Applies if** you sign checkpoints — `audit.checkpoints.key` or
`trail.Policy.Signer` — keep them on a store of your own, call `trail.Archive`,
or wrote a `trail.Store` of your own (a test double counts).

Every row of the manifest is now signed by the key that signs checkpoints, so
that a row changed after the latest checkpoint is found too, and each
checkpoint names the one before it — see
[the runtime](runtime.md#after-the-latest-checkpoint). For an app:

- **Regenerate, and migrate once.** `Archived` has two more columns,
  `signature` and `signed_by`. They are nullable, so a table that has rows
  takes them, and they are empty on every row there.
- **With a key, accept those rows once.** A row with no signature is `unsigned`
  until an operator accepts it: `policy.Accept(ctx, store, why)` signs each as
  it stands, and needs the key to. Until then a pass says so, and does not act
  on their chunks — nothing of theirs is destroyed or folded, and
  `Policy.Read` leaves them out. Without a key nothing changes.
- **A `trail.Store` of your own** is handed a `trail.Sign` with every write —
  `Adding`, `Added`, `Mark`, `Label` — and asks it for the signature to write
  beside each row, a row at a time, with every field the signature is over in
  the same write. `Mark` from a state to itself signs rows again as they are.
  The generated store does all of it.
- **`trail.Archive` signs nothing**, since it is handed no policy: with a key,
  the rows it writes are findings until accepted. A pass moves that
  deployment's rows. (It is handed the policy since
  [the entry above](#every-act-ends-in-a-checkpoint-and-trailarchive-takes-the-policy).)
- **On a store that refuses deletion, say so**: hand it in as
  `trail.WriteOnce(store)`, or set `audit.checkpoints.write_once` beside
  `audit.checkpoints.dir` on a WORM mount.
- **The findings** `unsigned` and `forged` are about rows too; `reordered` is
  new; `unkept`, `rewritten` and `relabelled` are about the chain as well.

Checkpoints written before name nothing, and the chain starts after them.

## The trail's archive can seal what its rows say

**Applies if** you want a tenant that leaves to take its trail out of the
archive's backups too, or you read the archive with `trail.Read`,
`trail.ReadTenant` or `trail.ReadFiles`.

Nothing changes without it. With `audit.keys` (or `trail.Policy.Keys`), what
each row says is sealed on its way into the archive, a key per tenant, and the
master's private half opens it — see [the runtime](runtime.md#sealed-contents).
For an app that turns it on:

- **Give the commands that read the master, and nothing else.** An export or a
  subject access request reads through `policy.ReadTenant`, which opens what
  it reads with `Keys.Identities` — `audit.keys.identity`, so
  `<APP>_AUDIT_KEYS_IDENTITY` for one run — and refuses a sealed row without it
  (`trail.ErrSealed`). The serving process is never given it.
- **`trail.Read`, `trail.ReadTenant` and `trail.ReadFiles` refuse a sealed
  row**, since they take no key. Read through the policy, or open a row with
  `Keys.Open`.
- **`TenantPurge.Keys` and `Receipt.Keys`** say how many keys a tenant's purge
  destroyed, which it does first, with a receipt `where: keys`.
- **Back the key store up briefly**, apart from the archive.

Chunks written before stay as they are and stay readable; rows a pass files from
`_legacy` are sealed on the way.

## The database keeps an account of the trail's archive

**Applies if** you set `audit.archive` or `trail.Policy.Archive`, call
`Policy.Purge`, `Policy.Doomed` or `trail.ReadTenant`, or wrote a `trail.Store`
of your own (a test double counts).

Every change to the archive is now written into a manifest in the database
first, and `Policy.Verify` compares the two — see
[the runtime](runtime.md#the-manifest). For an app:

- **Regenerate, and migrate once.** `pd gen` copies in one more entity of
  payday's, `Archived` (domain 5, which payday kept for this), and `trail.Store`
  gains the manifest's methods, which the generated store implements on it.
  The migration is one new table.
- **`Policy.Purge` and `Policy.Doomed` take the store**,
  `policy.Purge(ctx, store, cut)`, because what they destroy is decided by the
  manifest, which is in the database.
- **Export through the policy.** `policy.ReadTenant(ctx, store, tenant, fn)` and
  `policy.Read(ctx, store, fn)` read only what the manifest accounts for and
  check each chunk's bytes; anything that hands a tenant's trail to somebody as
  the trail — an offboarding export, a subject access request — wants those.
  `trail.ReadTenant` and `trail.Read` are unchanged, and read the archive as it
  is.
- **Decide where checkpoints go**, optionally: `audit.checkpoints.dir` for a
  directory of their own (`trail.Policy.Checkpoints` for anything else), and
  `audit.checkpoints.key` to sign them — Ed25519, ECDSA or RSA, a file or the PEM
  document — or a `crypto.Signer` of your own as `trail.Policy.Signer`, for a
  TPM, an HSM or a cloud KMS. With neither, they are kept in the archive and
  unsigned.
- **Give an operator a way to verify**: a command that calls
  `policy.Verify(ctx, store, full)` and prints the findings, and one that calls
  `policy.Accept(ctx, store, why)` once they have looked.

Nothing has to be run on the upgrade: the first pass fills the manifest from
what the archive holds, and files what is in `_legacy` by tenant.

## Erasing from the trail answers to a legal hold

**Applies if** you call `pd.ForgetInTrail`, `trail.Forget`, `trail.Collect`,
`trail.Leave`, `trail.Doomed` or `trail.Purge`, or wrote a `trail.Store` of
your own.

A tenant's answer can now carry a legal hold, `trail.Tenant.Hold`, and nothing a
held tenant may read stops existing while it is on — see
[the runtime](runtime.md#legal-holds). Every act that destroys something of the
trail had to be able to see the hold, so they are methods of `trail.Policy` now,
and the functions they replace are gone rather than kept beside them: a caller
still written against those would erase through a hold without knowing it.

| Was | Is |
| --- | --- |
| `pd.ForgetInTrail(ctx, db, objects)` and `trail.Forget(ctx, archive, encoded)` | `policy.Forget(ctx, pd.TrailStore(db), objects)`: the database and the archive in one call, answering `trail.Forgotten` with what a hold kept |
| `trail.Collect(ctx, s, of, before)` | `policy.Collect(ctx, s, of, before)`, answering what a hold kept as well |
| `trail.Leave(…)` | `trail.Archive`, or `policy.Collect` |
| `trail.Doomed(ctx, archive, cut)`, `trail.Purge(ctx, archive, cut)` | `policy.Doomed(ctx, cut)`, `policy.Purge(ctx, cut)` |
| `trail.Store` | gains `Heads` and `Blank`; `trail.Scope` gains `Objects`, and `trail.Whose` gains `Sharing` |

A caller with no policy of its own — a test, a command that was handed an
archive — means `trail.Policy{Archive: archive}`, which holds nothing. The
store is generated, so `pd gen` writes the new one.

And one thing is new: `policy.PurgeTenant` takes a leaving tenant's trail out,
and `policy.PlanTenantPurge` says what it would take. See
[the runtime](runtime.md#a-tenant-leaving).

## The trail's archive is a flob store, kept per tenant

**Applies if** you set `audit.archive`, call anything in `trail` beyond `Sweep`
and `Policy.Pass`, call the generated `pd.ReadTrail`, or wrote a `trail.Store`
of your own (a test double counts).

`trail.Policy` can now answer per tenant — `Policy.Tenants`, and floors in
`audit.min` — and the archive moved to a [flob](https://github.com/lesomnus/flob)
store with a namespace per tenant so that a tenant's window can be applied to
it. See [the runtime](runtime.md#and-per-tenant-when-the-app-answers-for-them).
With nothing new configured, what is kept and for how long does not change.

- **Nothing to run for the archive.** `audit.archive` is the same directory. The
  first pass adopts the files a version before wrote into it, by hard link, into
  a namespace of their own, and removes them from the directory; they are
  destroyed by the month in their name, as before. A copy taken before the
  upgrade is still readable with `trail.ReadFiles`.
- **One migration**, an index on `Audit (tenant_id, domain, date_created)`. Plan
  it as you plan any other.
- **The code:**

  | Was | Is |
  | --- | --- |
  | `trail.Policy.Archive` a directory | a `flob.Stores`; `config.AuditConfig.Policy()` makes one from `audit.archive` |
  | `trail.Archive(ctx, s, of, before, dir)`, `trail.Leave(…, dir)` | the same with the archive in place of `dir` |
  | `trail.Files(dir)` | `trail.Chunks(ctx, archive)` |
  | `trail.Read(paths, fn)` | `trail.Read(ctx, archive, fn)`, or `trail.ReadTenant` for one tenant's; `trail.ReadFiles(paths, fn)` for files on a disk |
  | `pd.ReadTrail(paths, fn)` | `trail.Read(ctx, archive, pd.TrailOf(fn))` |
  | `trail.Doomed(dir, cut)`, `trail.Purge(ctx, dir, cut)` | `(ctx, archive, cut)`, answering `[]trail.Chunk`; `Purge` writes a receipt |
  | `trail.Forget(dir, objects)` | `trail.Forget(ctx, archive, objects)` |
  | `trail.Named`, `trail.Writer`, `trail.NewWriter` | gone: a pass writes chunks |
  | `trail.Store`: `Older(ctx, kinds, at, limit)`, `Count(ctx, kinds, at)` | `Older(ctx, scope, at, cursor, limit)`, `Count(ctx, scope, at)`, and `Tenants(ctx, after, limit)` |

  The store is generated, so `pd gen` writes the new one; a scope of
  `trail.Scope{Kinds: kinds}` is what the old calls meant.
- **An archive that cannot list itself is refused** where the process comes up,
  since nothing could ever be destroyed from it. flob's stores on a disk, in
  memory and on S3 all can.

`pd.ForgetInTrail` and `trail.Forget` erase as they did. What they will do
about a tenant under a legal hold is the next change to them.

## What the ent schema cannot state has a directory of its own

**Applies if** you keep hand-written DDL (an exclusion constraint, a foreign key
that carries the tenant) in the planned migration directory, or in a second
directory that shares its history.

`entschema.Migrations.Extra` is a directory beside the planned one with a
history of its own, applied after it and never planned. See
[the server guide](guide/server.md#what-the-ent-schema-cannot-state). To move to
it:

- **Out of the planned directory.** Those statements are what each plan proposed
  to drop. Move them into new files under `migrations/extra`.
- **Out of a shared history.** Their versions are recorded in `schema_revisions`,
  so the new history starts without them, and on a database that already ran
  them they run again. Write the moved files so that running twice is harmless
  (PostgreSQL has no `ADD CONSTRAINT IF NOT EXISTS`, so guard each one with a
  check of `pg_constraint` in a `DO` block).

## `entschema.Check` leaves a foreign key or a check it does not know about alone

**Applies if** your database holds constraints the ent schema does not
declare, such as a foreign key that carries the tenant or a `CHECK`, or if you
kept them out because the server would not start with them.

`entschema.Check` used to refuse a database that held a foreign key or a check
constraint the schema does not declare: they came back from the diff as drops
and were counted as drift. They are now left alone, the same as an extra column
or index. A foreign key the schema declares and the database lacks is still
refused. So a composite foreign key that keeps a reference inside its tenant can
be a constraint rather than a trigger. payday requires `protobuf-orm/ent`
`v0.0.0-20261010071722-cf7a87c9af59`, so upgrading payday raises an app's pin
with it.

`Plan` has not changed. It still proposes dropping anything in the ent migration
directory that the schema does not declare, so keep those statements in a
directory of your own and apply it after the ent one.

## An overlay may add an index to one of payday's entities

**Applies if** a file in `proto/ext/payday/` declares an `option`, of any kind.

An option an overlay put on one of payday's entities used to be dropped
entirely, without a word, when payday's entity declared the same option. That
is always the case for `(orm.message)` and `(payday.entity)`. protobuf-merge now
merges the two (payday requires
`v0.0.0-20261009205601-8502fad9e678`, so upgrading payday raises an app's pin
with it), and `pd gen` compares the result against what payday shipped:

- An **index** the overlay adds to `(orm.message)` is generated. An overlay
  that carried one all along gets it now, and the next migration creates it. A
  `unique` one fails to create on a table that already holds rows it forbids, so
  look before you deploy.
- **Anything else** an overlay adds to `(orm.message)` or `(payday.entity)` is
  refused, where it used to be dropped. It never did anything, so take it out.

## The configuration is read by xli's `cfg`

**Applies if** anything says `config.For`, `pdcmd.Load`, `pdcmd.ConfigFlag` or
`pdcmd.NewCmdConfig` — every app `pd new` wrote does, in `cmd/config.go` and
`cli/cli.go`.

What read an app's struct from a file and the environment moved to
[`github.com/lesomnus/xli/cfg`](https://github.com/lesomnus/xli/blob/main/cfg/DESIGN.md),
beside the flags it can now bind. payday keeps the blocks (`config.ServerConfig`
and the rest), which read the same.

| was | is |
| --- | --- |
| `var Loader = config.For(Name)` in `cmd` | `l := cfg.New(cmd.Name, c)` in `cli.Cmd` |
| `pdcmd.ConfigFlag()` | `cfg.ConfigFlag()` |
| `pdcmd.NewCmdConfig(cmd.Loader, c)` | `cfg.NewCmdConfig(l)` |
| `pdcmd.Load(cmd.Loader, c)` | `cfg.Load(l, version)`, with `version := pdcmd.NewCmdVersion()` |
| `pdcmd.Reads("KEY_")` | `cfg.New(cmd.Name, c, cfg.Reads("KEY_"))` |
| `config.ReadFile(path, &v)` | `cfg.NewFile[V](path).Read()` |
| `cmd.Loader.EnvNames(&c)` | `l.EnvNames()` |
| `if v, _ := flg.Find[string](cl, "x"); v != "" { c.X = v }` after the load | `cfg.Bind(l, &c.X, &flg.String{Name: "x"})` |

`cfg.Load(l, version)` loads for every command but `version`, so a deployment
whose configuration is wrong can still be asked what build it is.

What a deployment will notice:

- **A key in the file that nothing reads is an error**, with the line it is on.
  It was ignored, which is how a misspelt key became a setting that silently was
  not there. A key starting with `x-` is ignored, for anchors.
- A value from the environment is taken as it is. A list is `a,b` or YAML flow,
  `[a, b]`; it was only the latter.
- In the file, a reference inside `[...]` or `{...}` must be quoted:
  `["${env:A}"]`. The parse error says so.
- `${file:/path}` is for a secret only, in a field of type `cfg.Secret`, which
  reads the file again when it is rotated. A secret written `file:/path`, the
  way roster and shale wrote one, is an error that says how to write it.
- A field with no tag is named in snake_case: `ListenAddr` is `listen_addr`,
  where it was `listenaddr`. Every block payday ships is tagged.
- `010` is ten, as YAML 1.2 reads it. It was eight.
- `config env` lists in the order of the struct rather than sorted; `config`
  prints what was set, with where it came from, and no secrets — by name, as it
  did, and every `cfg.Secret`.

## A UUID is the standard library's

`uuid.UUID` now comes from Go 1.27's `uuid` package rather than from
`github.com/google/uuid`. An app that names the type does so from the new
import path, and drops the dependency.

Nothing moves in the database or on the wire. `database/sql` writes a
`uuid.UUID` out in the canonical form and reads one back, the way it has always
done a `time.Time`, which is the same text `github.com/google/uuid` wrote for
itself; and a UUID has always crossed the wire as sixteen bytes.

What the standard library does not have, `entuuid` does:

| was | is |
| --- | --- |
| `uuid.FromBytes(b)` | `entuuid.FromBytes(b)` |
| `uuid.Nil` | `uuid.Nil()` |
| `uuid.NewString()` | `uuid.New().String()` |
| `uuid.Must(uuid.NewV7())` | `uuid.NewV7()` |
| `v.Version()`, `v.Variant()` | `v[6]>>4`, `v[8]>>6` |

`entuuid` is `github.com/protobuf-orm/protoc-gen-orm-ent/runtime/entuuid`, which
a generated server already depends on. Its `FromBytes` is the one that matters:
a UUID arrives as protobuf `bytes`, where nothing enforces the length.

A predicate written by hand against a UUID column binds the `uuid.UUID`; there
is nothing to convert. The one place that still wants a `driver.Valuer` is
`entql`, which asks for one at the call rather than at the bind:
`driver.DefaultParameterConverter.ConvertValue` is what to wrap it in.

## Every table is named after its entity, and none of them is pluralized

**Applies if** you have a database. It is a rename of every table an app has.

payday builds on a fork of ent, and the fork stopped putting entity names
through an English inflector to decide what to call a table. `User` was `users`
and is `user`; `Person` was `people` and is `person`; `Category` was
`categories` and is `category`. The name is the `snake_case` of the entity now,
so it can be worked out from the schema rather than looked up.

Join tables of a plain many-to-many edge are unchanged -- they are named after
the edge, not the type -- and so are index names, which were already built from
the singular. Foreign key constraint symbols carry two table names and so move
with them: `cards_users_card` is `card_user_card`.

**What to change.** Nothing in your schema or your Go code: the generated
constants follow, and `pd gen` writes them. The database is the work. `pd gen`
plans the rename like any other schema change, and `entschema.Check` refuses to
serve a database that does not match, so a deployment that has not run it will
say so rather than read an empty table.

Anything that names a table itself has to move by hand: raw SQL in your code,
a view, a trigger, a dashboard query, an entry in this file. A query written
against `roles` wants `role` now.

## A message-typed field is stored as protojson, and was storing nothing

**Applies if** an entity of yours has a field whose type is a message you
declared without `orm.message` — a value carried whole by the row that holds it,
with no table and no service of its own. If none does, nothing here applies, and
that is why this went unnoticed: payday's own reference app had no such field
until it was given one.

What was generated was an `encoding/json` column. Every message payday generates
uses the protobuf opaque API, whose fields are all unexported — so
`encoding/json` saw none of them, and every value round-tripped as `{}`. Written
whole, read back empty, with no error on either side.

It is `entpb.ValueScanner` now, which is protojson, in a `jsonb` column on
PostgreSQL and `json` on MySQL.

**What to do about the rows you have.** Nothing, almost certainly: they all hold
`{}`, because that is what the bug wrote. What they do *not* hold is anything to
migrate, so the column can be rewritten in place. If your app worked around this
— storing the same value a second way, or reading it from somewhere else — that
workaround is now the thing that disagrees with the column.

**And the field names travel.** protojson writes `displayName` where the schema
says `display_name`, so a SQL predicate reaching inside the column has to spell
it that way, and renaming a field of such a message is a silent migration
nothing checks.

## A reconnecting watch takes the snapshot again, whatever it asked for first

**Applies if** you run `<app> <entity> watch --retry` and set `skip_snapshot` on
the request, or you wrote a client that does the same thing.

`skip_snapshot` says *I know the current state*. After a gap that is no longer
true: whatever changed while the connection was down was sent to nobody, and a
watch has no backlog to catch up from. So a reconnect clears the flag and asks
for the snapshot, which is the only thing that says what was missed.

What changes for you is volume rather than correctness: a client that sized its
buffers for a delta-only stream gets a full snapshot on every reconnect. The
alternative is holding a row that is wrong until the next write happens to
correct it, which may be never.

## A batch operation is on the trail under its own name

**Applies if** you read the audit trail by `action` — a report, a retention
rule, a filter on a dashboard — and something in your app writes through
`payday.BatchService`. It applies as well to anything that groups writes by the
watch event they arrived in, which the outbox answers differently; see below.

The recorder does not know what the caller asked for, so it asks gRPC. Inside a
batch, gRPC's answer is the envelope — `/payday.BatchService/Do`, for every
operation of every batch — so a hundred different writes were filed as one
action, and "who renamed this" answered with a method that renames nothing.

Each operation is dispatched now in a context where gRPC answers with the
operation's own method, which is the name the caller wrote into the op. A batch
of two Adds is two rows whose `action` is `/app.RobotService/Add`, exactly what
the same two calls made one at a time would leave. The envelope files no row of
its own, because it writes nothing.

**A watch hears it differently down the two publishing paths**, and an app with
the outbox on has both. The in-process publish is one event per call — the
interceptor, once the handler has answered — so the batch arrives **whole**:
`Event.Method` is the batch, and each `Change.Method` inside it is the
operation's. The queue is a row per write and the drainer publishes rows, so the
same batch comes back out of it as one event per operation, each holding its
single change and each carrying the operation's method as `Event.Method`. The
grouping was never in the queue to hand on — a row is written inside the
transaction, and the call it belongs to only ends after the commit — so a
subscriber looking for the batch in `Event.Method` finds it on the immediate
event and never on the drained one.

**What to change.** A query or a retention rule matching
`/payday.BatchService/Do` matches nothing written from here on — those rows sit
under the operations' own methods now, beside the ones the same writes leave
outside a batch. Rows already written keep the action they were written with;
nothing rewrites the trail.

The seam is [`batch.AsOp`](../batch/batch.go), which the generated dispatch
calls once per operation, and which anything dispatching operations of its own
calls the same way. The queue's half is `Drain` in the generated `server/pd`,
which publishes a queued row at a time and has no call to group them by.

## A soft erase is on the trail under the row it erased

**Applies if** anything reads the trail for erasures, and especially if the
tenant whose row was erased is one of the readers.

`Audit.value` is the row as it was when the event was over, an Erase included,
and `Audit.tenant_id` is whose row it was. A soft erase was the one write where
neither held. The recorder reads the row back through the bare server, whose
every read narrows to the rows still here, so the row the erase had just stamped
answered NotFound and the record took the hard-erase path: the actor's tenant,
an empty value. The wall on the trail is the OR of `tenant_id`,
`actor_tenant_id` and `counterpart_tenant_id`, and an erase names the erased
tenant in none of them — the first two were the actor's and the third is unset
unless the operation said otherwise — so that made the record of an erasure the
one row the erased side could not read.

That read is retried past the erased filter now, inside the same transaction.
A soft erase is filed under the **erased row's** tenant, and its value is the
whole row with the stamp on — the only account of what a row held at the moment
it went.

**A hard erase is unchanged**, and is the exception on purpose: the recorder
runs inside the transaction after the delete, so there is nothing left to read.
It is filed under the actor's tenant with an empty value, which is the last
thing known about it.

**What to check.** Two things move. A reader that found erasures by an empty
`value` finds only hard ones from here on. And a tenant reading its own history
sees the erasures of its own rows, which it could not before — that is the
point of the change, and it is a widening of what that tenant may read.

## An overlay may not change what the file is

**Applies if** an entity overlay of yours — `proto/ext/payday/holder.ext.proto`
and the rest — carries a `features.` option at the top of the file, outside
any message.

The merge unions the file's options, so `option features.field_presence =
IMPLICIT;` written there lands on the **whole** copied entity: every field of
every message in it, payday's own included. What that costs is not obvious,
which is why it is refused rather than documented. On a field something calls
`Has` on, it stops the build, which is the lucky version; on a field nothing
calls `Has` on, it changes only what "not set" means on the wire, and nothing
says so.

The same refusal already covered contract overlays
(`proto/ext/<pkg>/*_svc.ext.proto`); it covers entity overlays now. The line is
easier to carry into one of those: the entity file an overlay is written against
begins with one, so copying its head to get started brings it along, and the
merge used to take it without a word.

**What to change.** Delete the line — `pd gen` refuses and names the file. An
option that is about one message goes **inside** that message, which is where it
is the app's to say and where generation leaves it alone.

## `ls --next` carries on from where it says it does

**Applies if** anything pages through a list with the commands `pdcmd` builds,
or if you mounted a hand-written list RPC into the tree.

The two halves have different names. An answer calls the cursor `next` and a
generated list request calls the same thing `after`, for where it goes rather
than where it came from — and the flag's value never reached the request at
all, so every page was the first page. A first page repeated is exactly what an
honest one-page list looks like, so nothing said so.

`--next` writes `after` now, and falls back to `next` for a hand-written service
that named the field the way the flag is named. A list request with neither is
refused by name rather than dropped:

```
acme.ThingListRequest: has no cursor for --next to carry on from
```

**What to change.** Nothing, for a generated list. A hand-written list RPC
needs a `string` field called `after` or `next` to page at all, and now says so
instead of answering the first page again.

## A grant's methods are patterns, so `*` in one now means something

**Applies if** anything you store and hand to `frame.Grant.To` — a role's
methods, an API key's, whatever your app calls them — could already contain a
string with `*` as a whole part, like `/app.RobotService/*`.

`Grant.Allows` was a membership test. A stored `/app.RobotService/*` matched
only a method named exactly `*`, which cannot exist, so such a string allowed
**nothing**. It is a pattern now and allows that whole service.

That is a widening, and it is the only backwards-incompatible thing here: a
grant of whole method names behaves exactly as it did, because `frame.Covers`
answers on equality first.

**What to check.** One query, against wherever you keep them:

```sql
SELECT * FROM role WHERE EXISTS (
  SELECT 1 FROM json_each(methods) WHERE json_each.value LIKE '%*%'
);
```

Anything it returns was allowing nothing and now allows something. Almost
certainly there is nothing — a string like that is one somebody typed expecting
this feature before it existed.

**What you gain**, and the reason for it: a list of methods is a snapshot of
something that grows. Write out every method of a service today and the next
release adds one the role does not allow — silently, to a role whose name says
it covers that service.

```
/app.RobotService/Get     one method
/app.RobotService/*       one service
/app.*/*                  one package
/app.*/Get                that method wherever it appears
/*.*/*                    everything
```

A whole part or nothing. `*Get*` is not a glob, it is a method name that
happens to contain asterisks — partial matching is where three string
comparisons become a regular expression.

And a pattern is covered by **one** pattern you hold, never by several
together. Holding `/app.A/*` and `/app.B/*` does not let you hand out
`/app.*/*` even where those are the only two services, because the third one
added next release would be covered by a grant made before it existed. See
[`frame.Covers`](../frame/method.go).

## payday's entities are copied inside your proto package

**Applies if** your schema has `import "payday/tenant.proto"` or any other
`payday/*.proto`, which is every app generated before this.

`pd gen` used to copy payday's entities to `proto/payday/`. A protobuf registry
is per process and keys files by their path, so two apps that both did that
registered one path twice and panicked before `main` — which is not
hypothetical, it is what the first app to import another payday app's generated
client did to its own binary. The copies land at `proto/<pkg>/payday/` now, so
the path carries the app's proto package and no two apps collide.
[Two apps in one process](guide/packages.md#3-two-apps-in-one-process) is that
rule in full.

**What to change**, in your hand-written schema and in your overlays:

```diff
-import "payday/tenant.proto";
+import "app/payday/tenant.proto";
```

where `app` is whatever your entities declare as their proto package. Then:

```sh
rm -rf proto/payday       # the old copies; pd gen writes the new ones
go tool pd gen .
```

Generated `*_svc.g.proto` are rewritten for you. If one is left holding the old
import, delete it and generate again — a stale one is an input to the next run.

**And check your proto package is your own.** The package name is the path on
disk, so distinct packages are distinct paths — and two apps that both kept
`pd new`'s `package app;` are distinct in neither, their messages being
`app.Holder` on both sides. Renaming it is one line per proto:

```diff
-package app;
+package acme;
```

`go_package` is separate and does not have to match.

The rule this leaves: **two payday apps can share a process when their proto
packages differ**, and two instances of the *same* app always could — one binary
links that app's files once, so nothing registers a path twice.

## `payday.Holder` is watchable, and has a list

**Applies to** every app, and needs no change from you.

`Holder` declares `watch: {}` and a `list:`, so `HolderService` gains `Watch`
and `List`. An app that does not open a stream pays for a generated method and
nothing else.

It is declared in payday's own schema because an app cannot: an overlay merges
**fields** and not the entity option, so there was no way for a deployment to
make payday's entity streamable.

## A field can say it is never answered with

**Applies if** you store a verifier — a password hash, an API key hash.

`(payday.field).secret` marks a field that is written and never handed back, and
`pd.Secret` is the generated layer that clears it on the way out. Stack it:

```diff
-app.Build(walled, core.Build(), pd.AuditBuild(), pd.GateBuild())
+app.Build(walled, core.Build(), pd.AuditBuild(), pd.SecretBuild(), pd.GateBuild())
```

The layer is only generated for an app that declared one, so a stack naming it
without one will not compile — which is the reminder.

**Ordering**: the extension is in payday's buf module, so an app depending on
`buf.build/payday/payday` needs a version of it that has this. Until then the
compile says `unknown extension payday.field`.

## A field can say a request may not assert it

**Applies if** you have a stamp — when something was verified, attested,
approved — that the deployment establishes rather than a caller.

`(payday.field).stamped` refuses it in the generated `Add`:

```proto
google.protobuf.Timestamp date_verified = 9 [
  (orm.field) = {nullable: true},
  (payday.field) = {stamped: true}
];
```

```
InvalidArgument: date_verified: is established by this deployment and not
asserted by a request
```

The refusal asks the question the request can answer. A field with presence in
the `Add` request (a message, an edge, or a scalar that is `nullable` or has a
`default`) is refused when it is set at all. A scalar the row cannot do without
has no presence there (the ORM declares it `IMPLICIT`), so it is refused when it
is not zero, and a list or a map when it is not empty. The same holds for a
field an overlay adds to one of payday's entities (`proto/ext/payday/`), which
once generated a gate that did not compile (#29).

**`immutable:` is not this, and reaching for it is the mistake this exists to
end.** `orm.field.immutable` takes a field out of the **patch** request and
leaves it in `Add`, which is right for a tenancy stamp — set once, never moved —
and exactly backwards for this: nobody asserts a verification when the row is
created, and something writes it on the day the verification happens.

The refusal lands in the **gate**, which is exact rather than convenient: a
stamp is not a permission and not a column nobody may touch, it is a fact a
*request* may not assert. So an app's own work through a server without the gate
— `init`, a seed, the call that does the verifying — writes it without anybody
having listed those callers. `Patch` is untouched, because that is the road the
stamp is written by.

Two apps found this the hard way before the option existed, each closing it in a
layer of its own. A layer works and it is invisible: it is a line in a stack
rather than a property of the message, so the next app declares the same field
and does not know to write the same refusal.

## A field that lies about presence is refused

**Applies if** your schema has a message field with no `nullable`, no `default`
and no marker — a `google.protobuf.Timestamp` you meant to be optional, most
likely.

It generated a NOT NULL column while the API beside it had `Has…`, so a caller
asking whether a value was set was told yes and read a zero. Generation refuses
it now and names both fixes, because they mean different things:

```diff
-google.protobuf.Timestamp date_seen = 8;
+google.protobuf.Timestamp date_seen = 8 [(orm.field) = {nullable: true}];
```

or a `default`, if the value is always there.

`date_created`, `date_updated` and `date_erased` are exempt, and the rule is
their declarations rather than their names — an app whose version field is
called something else is not caught by a rule about spelling.

## `pdtest.DB` gives a second database to a second call

**Applies if** a test of yours calls it twice, which is a test that stands two
apps up.

It used to name the schema after the test, so two calls got one schema and the
second's `DROP SCHEMA` removed the first app's tables. It passed on SQLite and
failed only under `PDTEST_POSTGRES`. Nothing to change: no existing test could
have wanted the old behaviour.

## `order:`, `by:` and `with:` name a field as `{name, number}`

**Applies if** your schema declares a `list:`, which is every entity with a page
or a watch.

They took a bare string. A proto field is renamed without the wire changing, so
the **number** is the identity and the name is a label -- and a declaration
carrying only the label follows a rename onto whatever took the old name. That
is not the loud failure of "there is no such field" but the quiet one of "that
is a different field now", and a paging order that moved to another column is
exactly what this generator exists to refuse.

It is also how the rest of a schema already points at a field: `orm`'s indexes
carry `{name, number}`, and two conventions for one thing is one somebody has to
remember which is which for.

```diff
     list: {
-      order: [{field: "date_created"}, {field: "id"}]
-      by: ["ref", "tenant"]
-      with: ["tenant"]
+      order: [{field: {name: "date_created", number: 15}}, {field: {name: "id", number: 1}}]
+      by: [{name: "ref"}, {name: "tenant", number: 2}]
+      with: [{name: "tenant", number: 2}]
     }
```

Either half alone still works, so a small schema is not made verbose and an
existing one is not rewritten to be read. **Both together is what is checked**:
generation resolves by the number and refuses when the name no longer matches.

`{name: "ref"}` has no number, since it is not a field.

`tenanted: {field: ...}` is unchanged and still takes plain strings -- it names
a column the wall reads, not a filter.
