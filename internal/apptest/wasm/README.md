# wasm

This app, compiled for the page it serves.

```sh
$ GOOS=js GOARCH=wasm go build -o web/app.wasm ./wasm
```

It is the same server `cmd` runs. The two lines that differ are the driver
(`sqlite3-wasm`, which runs the engine in a Worker of its own rather than on
wazero — that would be wasm inside wasm) and the transport (`jsport`, a message
port rather than HTTP/2). Everything between them — the stack, the wall, the
trail — is the same code from the same schema.

The database is held in memory, so **a reload is a fresh server**. That is the
point rather than a limitation: a sandbox that remembered is one somebody has to
clear.

## What is not here yet

The page. Starting this needs two things from npm — `sqlite3-wasm-go`, whose
worker hosts the Go program, and the TypeScript half of `grpc-dgram`, which
speaks the same wire from the other side. Both exist; wiring them is a front-end
job and not a Go one, so it is left for whoever writes the first UI.

Two requirements will bite whoever does, and neither is this file's to fix:

- **`Cross-Origin-Opener-Policy: same-origin` and
  `Cross-Origin-Embedder-Policy: require-corp`.** `sqlite3-wasm` cancels work
  with a `SharedArrayBuffer`, which does not exist without cross-origin
  isolation. It fails loudly and confusingly — the symptom is "it works on the
  other dev server".
- **A bundler that can resolve a `.wasm` as a URL and start a Worker.** The
  syntax differs between them; `sqlite3-wasm`'s README writes out both.
