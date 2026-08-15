# The browser

Two things a payday app does that most gRPC frameworks stop short of: it serves
a browser, and it compiles into one.

[The page guide](guide/client.md) is how to use the client. This is the
architecture — the transports, the sandbox, and the layering of the store.

- [1. Two transports, and why](#1-two-transports-and-why)
- [2. The whole app in a page](#2-the-whole-app-in-a-page)
- [3. The client is a replica](#3-the-client-is-a-replica)
- [4. What is forced and what is not](#4-what-is-forced-and-what-is-not)

## 1. Two transports, and why

gRPC is not a protocol a browser speaks. Not a missing library — a thing the
platform does not expose. So payday serves both:

- **real gRPC over a socket**, for machines
- **Connect and gRPC-Web**, for pages — [`payday/web`](../web)

They are the same `*grpc.Server`. `web.New` transcodes; it is not a second
stack. Same handlers, same interceptors, same wall, and no second door for a
rule to be missing from.

### The reversal

This document originally chose drpc-over-WebSocket for production, on the
grounds that Connect would make the sandbox and production use different client
libraries. **That premise was wrong.** `grpc-dgram`'s TypeScript port *is* a
Connect transport, so either way the client library is Connect-ES and only the
transport swaps.

With the premise gone, three things were left on the scale:

| | drpc over WebSocket | Connect / gRPC-Web |
| --- | --- | --- |
| sandbox vs production | byte-identical wire | different wires |
| credentials | **per connection** (handshake cookie) | **per call** |
| infrastructure in front of the browser | knows nothing about it | it is just a POST |

The second is decisive. A credential carried on the handshake means a token that
expires mid-connection changes **nothing** — the connection goes on being
trusted — so expiry has to be built as a separate mechanism that cuts the stream.
Per-call credentials do not have that problem at all.

The third is real too. What is actually in front of a browser is a CDN, a WAF, a
reverse proxy — things that understand HTTP and not gRPC framing. Connect is one
POST with `Content-Type: application/json`, so devtools read it, a HAR captures
it, and **a request the page sent can be replayed from a shell**.

What is lost is the first row: the sandbox no longer runs the same wire as
production. Everything above the transport is identical, so what escapes is a
defect in the wire itself.

## 2. The whole app in a page

The sandbox is the entire server — gate, wall, trail, ent, SQLite — compiled
`GOOS=js GOARCH=wasm` and running in the browser. The page talks to it over a
message port instead of a socket.

```
page ── Connect transport ── message port ── worker
                                              ├ the app, as wasm
                                              └ SQLite, as wasm
```

What it is for:

- **A demo that needs no backend.** A link, and the app runs.
- **Tests of the real stack** with no database to start.
- **Proof that the runtime assumes nothing.** No file system, no listener, no
  network. CI builds `GOOS=js GOARCH=wasm go build ./...` for exactly this, and
  the sandbox test says the thing it builds actually runs.

### What it costs, measured

payday's own sandbox, built from `internal/apptest` — eight entities:

| | raw | brotli |
| --- | ---: | ---: |
| the Go runtime alone | 1.8 MB | 0.5 MB |
| \+ the generated messages | 22.1 MB | 3.9 MB |
| \+ the generated servers | 37.8 MB | 6.6 MB |
| the whole thing | 65.9 MB | **10.6 MB** |

Two things in that table decide everything else about size.

**The runtime is 2.7% of it.** The rest is generated code and the reflection
that comes with it — which is why a smaller Go is not the lever it looks like.
See below.

**Importing the generated messages costs 20 MB**, and `google.golang.org/grpc`
and `net/http` are already linked at that point: the generated `*_grpc.pb.go`
imports gRPC, so the sandbox links a server it never serves from. That is not
recoverable without changing what is generated.

The vite config `pd sandbox init` writes compresses on `vite build`, so a demo
link is about 10 MB rather than 66. `npm run dev` reads the file off disk and
compresses nothing, because there is nothing to save.

The build loop is not the problem either. On a 2022 laptop, changing one line:

| | |
| --- | --- |
| nothing (cached) | 1.6 s |
| `wasm/main.go` | 1.3–1.8 s |
| something in payday's runtime | 2.5 s |

### Why not TinyGo

It is the obvious idea and the table above is why it is not taken. TinyGo's
size advantage is a smaller runtime, no reflection metadata, and harder dead
code elimination. Here the runtime is 1.8 MB of 65.9, and the other 64 MB is
generated protobuf, generated servers, and ent — all of which lean on exactly
the reflection TinyGo does without.

Past that, it would not compile: `google.golang.org/protobuf` under
`API_OPAQUE` depends on `protoimpl`'s unsafe field offsets, `grpc` needs `net`
and `crypto/tls`, and `database/sql` reflects to scan a row.

The design argument is the stronger one. **The sandbox is worth having because
it is the same server.** A build needing a different protobuf runtime, a
different SQL layer and no gRPC would be a second implementation, and the
first thing a second implementation does is disagree with the first — which is
the thing this exists to make impossible.

That last one earns its minute. The first time the sandbox was ever loaded in a
page, three things were wrong — no broker named, nothing seeded, no interceptors
— and every one of them compiled, linked and started. **A `main` that is built
and never run says nothing at all.**

### Getting one

```sh
$ go tool pd sandbox init .
```

It writes `wasm/main.go` — the second entry point — and the page's half:
`ts/src/sandbox.ts`, `ts/src/sandbox-worker.ts`, and a `ts/vite.config.ts`
carrying the two settings the page needs plus the compression a demo build
wants. Then it prints the build, which is not run for you because it fetches a
module and writes tens of megabytes.

**`pd new` does not write it, and that is a decision.** `wasm/main.go` imports
`github.com/lesomnus/grpc-dgram`, which becomes a *direct* requirement of your
app — a module payday itself does not depend on. An app that is only a server
should not acquire one by scaffolding. (`pd new` writing `ts/` to everybody is
not the same imposition: an unused directory costs nothing, and an unused
module requirement is in `go.mod`, in the sums, and in every audit of what the
app depends on.)

So there is one path to a sandbox whenever you want one, which also means it
cannot rot: an app that started without a page and grew one runs exactly what a
new app would have run.

`ts/vite.config.ts` already exists, so it is replaced only when it is still
byte-for-byte what `pd new` wrote. Otherwise the two settings are printed for
you to add — payday does not edit a file a person wrote.

`pd doctor` checks all four of the things below for an app that has a sandbox,
and says nothing to one that does not.

### What the page needs from the app

`@lesomnus/payday/sandbox` is the part that does not vary:

```ts
import { start } from '@lesomnus/payday/sandbox'

const box = await start({ worker: new URL('./sandbox-worker.ts', import.meta.url) })
const c = app(box.transport)          // the same `app()` the real server gets
```

It answers with a `Transport` and not a client, because payday cannot name your
services — `client.ts` is what knows them, and it takes a `Transport` like any
other. That is the whole reason a sandbox is worth having: the code that runs
against it is the code that runs against the server.

**Wrap `box.transport` rather than dialing `box.sock` again** if you need to add
a header. A second transport is a second connection nobody asked for, and if
`@lesomnus/grpc-dgram` ever resolves to two copies — payday's TypeScript linked
by path, say — the socket and the transport come from different ones. That is
not a type error. It is a refusal arriving with no status, so `NotFound` reads
as `Unknown` and nothing else in the run looks wrong.

### What the sandbox is not

It is not the local store. They get confused because both put data in the
browser, and they are different things:

| | Sandbox | Local store |
| --- | --- | --- |
| what runs | the whole server | a cache in front of a real one |
| the database | SQLite, in a worker | a memory replica, mirrored to IndexedDB |
| when it is used | demos, tests | always |
| is there a server | no | yes, and it is the truth |

An app uses the store every day and the sandbox when it wants to run without a
backend.

### Two imports that must share a realm

The worker imports the SQLite wasm module and the transport worker in that
order, in the same file. Splitting them puts them in different realms and the
`SharedArrayBuffer` handoff stops working — and the page needs COOP/COEP headers
for `crossOriginIsolated` to be true at all.

## 3. The client is a replica

The design's centre of gravity: **the calling code does not know whether an
answer is local or remote.**

`payday/query` sits where the call goes, so it knows what was drawn from what.
When a row changes, everything that drew it re-renders. There are no
declarations — what a render asked for *is* its dependency.

### Why memory is the truth and the disk is the mirror

Reactivity needs a **synchronous read**: `useSyncExternalStore` takes a function
that answers *now*. IndexedDB cannot do that, so a store on top of it grows a
memory copy — and the moment there is a copy, that copy is the store and the
database is persistence.

So it is memory first, mirrored to IndexedDB, and never the other way round.

### Rows are not enough

A refresh that restores only rows still draws a spinner where a list was one
second ago, because **a list is an order and a membership** and neither of those
lives on any row. So the query layer mirrors its own answers under the same
credential (`Store.blob`), and restoration is **synchronous** — a reopened page
draws the list on its first frame.

The mirror is stamped with a digest of field numbers, names and kinds. A
mismatch throws the whole mirror away rather than reinterpreting it: an old
shape read back as the current one is a screen that is wrong with nothing having
failed.

### Writes go the other way for the same reason

A write's **answer is the row**, so it goes into the store and everything drawing
that row is immediately correct — with no round trip and no invalidation rule.

What an answer cannot say is that a *set* changed, so the lists of whatever
entity the write touched are re-read — and only the ones currently drawn, since
an idle query re-reads when it wakes. Judging membership locally instead would
mean evaluating the server's filter and ordering over a partial copy, which is
confidently wrong at page boundaries.

`Erase` answers with nothing, so its subject is read out of the **request**.

### And an expiry

`DiskOpts.keep`, seven days by default. Without one, the answer to every
question the app ever asked stays on disk, and a restored answer is drawn as if
true for one round trip — so *how stale may this be* needs an answer.

It is measured from **when this side last wrote it**, not from the server's
`dateUpdated`. Measuring the other way discards rows that have not changed since
2020 first — that is, the rows that never change.

## 4. What is forced and what is not

**Forced: the store.** Not React. The reactive layer is thirty lines of
`useSyncExternalStore` in a separate entry point, and any framework with a
subscribe-and-snapshot primitive can have the same thing.

**The template's defaults are Vite and React**, and they are defaults. The
boundary is one file.

**Two things are genuine requirements**, whatever you build the page with:

- something that bundles ES modules and understands `import.meta.url`, because
  the worker is loaded that way
- COOP/COEP headers if you want the sandbox, for `crossOriginIsolated`

**The generated TypeScript is one plugin.** protobuf-es v2 emits service
descriptors, and Connect's `createClient` takes them directly, so there is no
per-service generated file to drift. The domain table is generated too — keeping
two copies is exactly the drift `pdid` exists to prevent.

## See also

- [guide/client.md](guide/client.md) — how to use the store and the query layer
- [guide/errors.md](guide/errors.md) — a refusal, from the server to a form field
- [RUNTIME.md](RUNTIME.md) — the server half
