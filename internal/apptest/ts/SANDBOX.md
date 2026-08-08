# The sandbox

`src/sandbox.ts` is the whole app running inside the page: the same server the
process runs, compiled to `GOOS=js GOARCH=wasm`, with SQLite in a Worker instead
of a file and a message port instead of HTTP/2. A reload is a fresh server.

It is **not** in `npm run check`, and the reason is one thing only: it imports
`@lesomnus/grpc-dgram`, which is not published yet. Once it is, the import
resolves, the file goes back into the default check, and the cast at the one
boundary in it goes away -- that cast exists because a directory-linked package
brings its own `node_modules`, so two copies of `@connectrpc/connect` are two
nominally different `Transport` types.

To check it against a local checkout:

```sh
npm install --save-dev file:../../../../grpc-dgram/ts
npm run check:sandbox
```

That was run, and it passes. What has **not** been done is loading the page in a
browser -- there is none here -- so what is verified is that the pieces fit and
not that the thing runs.

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
