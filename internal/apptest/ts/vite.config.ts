import { defineConfig } from "vite";

// What it takes to serve the sandbox, which is two things and neither is
// payday's to fix. Both fail confusingly, which is why they are written out
// rather than left to a README.
export default defineConfig({
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
