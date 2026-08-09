# The sandbox

`src/sandbox.ts` is the whole app running inside the page: the same server the
process runs, compiled to `GOOS=js GOARCH=wasm`, with SQLite in a Worker instead
of a file and a message port instead of HTTP/2. A reload is a fresh server.

It is in `npm run check` like everything else. It was not, for one reason --
`@lesomnus/grpc-dgram` was unpublished, so it was a directory link, and a linked
package brings its own `node_modules`; two copies of `@connectrpc/connect` are
two nominally different `Transport` types, which cost one `as unknown as` at the
one boundary in this file. It is published now, so there is one copy, the second
tsconfig is gone and so is the cast.

What that leaves is the line the whole design is arranged around:

```ts
const transport = createDrpcTransport(sock.dial())   // sandbox
const transport = createConnectTransport({ baseUrl }) // a real server
```

Both are a Connect `Transport` and nothing above this file knows which it got.

## It runs

Loaded in headless Chromium (2026-08). The server is up in **under a second**,
serves real calls, and the wall answers `NotFound` to an identifier it does not
hold -- the same answer it gives over HTTP.

```
crossOriginIsolated: true
SharedArrayBuffer: function
server up in 862ms
tenant: acme / 16 byte id
robot: arm-01
list: 1 item(s)
wall: ConnectError: [not_found] Robot not found
```

It did not run before that, and **three things were wrong**. All three compiled,
linked and started; each is invisible to `GOOS=js GOARCH=wasm go build`, which is
the whole of what CI was doing.

1. **No broker was named.** `config.WatchConfig` refuses a deployment that
   leaves it unsaid, and that refusal happens at `Build` -- which nothing
   reached, because a main that is compiled and never run says nothing.
2. **Nothing seeded.** A tenant cannot be put up from inside one, so a page that
   starts an empty sandbox is refused the first thing anybody tries, correctly,
   with no way round it.
3. **No interceptors.** The stack behind them refuses a request with no frame,
   so the sandbox answered `who is asking?` to everything. This is the sharp
   one: it started, published its entry point, and served nothing.

## Serving it

Four things, and the last two are the ones that fail confusingly.

```sh
GOOS=js GOARCH=wasm go build -o public/app.wasm ../wasm
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" ./public/
```

**Headers.**

```
Cross-Origin-Opener-Policy: same-origin
Cross-Origin-Embedder-Policy: require-corp
```

SQLite in a Worker cancels work with a `SharedArrayBuffer`, which does not exist
without cross-origin isolation. The symptom is "it works on the other dev
server".

**A worker of your own**, which `start()` does not take yet and which nothing
above said. Two things have to be in the same realm and neither package can put
them there for the other: `sqlite3-wasm-go` installs the global the Go driver
looks for, and `@lesomnus/grpc-dgram` runs the module that looks for it.
Importing the driver on the main thread installs it in the wrong realm.

```ts
// sandbox-worker.ts
import 'sqlite3-wasm-go'
import '@lesomnus/grpc-dgram/wasm/worker'
```

```ts
const sock = await open('/app.wasm', { workerUrl: new URL('./sandbox-worker.ts', import.meta.url) })
```

**And with vite, `optimizeDeps: { exclude: ['@lesomnus/grpc-dgram'] }`.** The
worker is `new URL("./wasm/worker.mjs", import.meta.url)`, and pre-bundling
rewrites the module into `.vite/deps/` where that relative URL does not resolve.

> `sqlite3-wasm-go` is **not on npm** -- the Go module carries its source and
> `npm i` cannot reach it. Until it is published, the sandbox is built from a
> checkout. That is what stops this being a test rather than a note.
