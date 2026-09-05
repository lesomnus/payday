import { defineConfig } from "vite";

// What it takes to serve the sandbox, which is two things and neither is
// payday's to fix. Both fail confusingly, which is why they are written out
// rather than left to a README.
export default defineConfig({
  // `react` and `react-dom` are resolved from **here**, once -- the same line
  // `vitest.config.ts` carries and for the same reason, which is written out
  // there: `@lesomnus/payday` is a directory link in this repository and a
  // linked package resolves its own imports from its own directory, where
  // React is an *optional* peer and so is not installed.
  //
  // Without this the dev server answers payday's `react/jsx-runtime` with a
  // stub, and the page dies on `does not provide an export named 'Fragment'`
  // -- which names neither React nor the link.
  resolve: { dedupe: ["react", "react-dom"] },

  // The worker `@lesomnus/grpc-dgram` starts is
  // `new URL("./wasm/worker.mjs", import.meta.url)`, and dependency
  // pre-bundling rewrites the module into `.vite/deps/` -- where that
  // relative URL resolves to nothing. The failure is "the worker itself
  // failed", which does not mention bundling.
  optimizeDeps: { exclude: ["@lesomnus/grpc-dgram"] },

  server: {
    // SQLite in a Worker cancels work with a `SharedArrayBuffer`, which does
    // not exist without cross-origin isolation. The symptom is "it works on
    // the other dev server".
    headers: {
      "Cross-Origin-Opener-Policy": "same-origin",
      "Cross-Origin-Embedder-Policy": "require-corp",
    },
  },
});
