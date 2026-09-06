/**
 * What vite adds to the module graph.
 *
 * `?worker` is the one this app needs: monaco's language services run in
 * workers, and the URL of a bundled worker is something only the bundler that
 * produced it knows. Without this reference those imports are modules
 * TypeScript has never heard of.
 */

/// <reference types="vite/client" />
