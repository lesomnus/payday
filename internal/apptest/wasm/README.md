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

## The page

`../ts/src/sandbox.ts`, and it is two lines: open the module, dial it, hand the
transport to `app()`. It is that short because everything above the transport is
blind to which one it got -- the sandbox and the real server are the same code
with a different argument, which is the whole reason a sandbox is worth having.
Code that only ever ran against a fake has never run.

It runs -- loaded in headless Chromium, server up in under a second, the wall
answering as it does over HTTP. It did not before that, and three things in this
file were wrong; `../ts/SANDBOX.md` lists them and what it takes to serve.

Two requirements will bite whoever serves it, and neither is this file's to fix:

- **`Cross-Origin-Opener-Policy: same-origin` and
  `Cross-Origin-Embedder-Policy: require-corp`.** `sqlite3-wasm` cancels work
  with a `SharedArrayBuffer`, which does not exist without cross-origin
  isolation. It fails loudly and confusingly — the symptom is "it works on the
  other dev server".
- **A bundler that can resolve a `.wasm` as a URL and start a Worker.** The
  syntax differs between them; `sqlite3-wasm`'s README writes out both.
