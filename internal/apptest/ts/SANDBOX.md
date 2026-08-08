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

What has **not** been done is loading the page in a browser -- there is none
here -- so what is verified is that the pieces fit and not that the thing runs.

To serve it, three things:

```sh
GOOS=js GOARCH=wasm go build -o public/app.wasm ../wasm
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" ./public/
```

and headers, which is the one that fails confusingly:

```
Cross-Origin-Opener-Policy: same-origin
Cross-Origin-Embedder-Policy: require-corp
```

SQLite in a Worker cancels work with a `SharedArrayBuffer`, which does not exist
without cross-origin isolation. The symptom is "it works on the other dev
server".
