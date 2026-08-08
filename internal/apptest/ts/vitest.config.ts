import { defineConfig } from 'vitest/config'

/**
 * `react` and `react-dom` are resolved from **here**, once.
 *
 * `@lesomnus/payday` is a directory link in this repository rather than an
 * install, and a linked package resolves its own imports from its own
 * directory -- which has no React, because React there would be a *second*
 * copy: two context registries, and a `Provider` written against one is
 * invisible to a tree rendered by the other. Deduping says which copy.
 *
 * It is a link artifact and not a thing an app has to do. Installed from the
 * registry, `react` is an optional peer dependency and npm puts one copy where
 * both find it.
 */
export default defineConfig({
	resolve: { dedupe: ['react', 'react-dom'] },
})
