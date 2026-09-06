/**
 * The sandbox, in a browser.
 *
 * It is skipped unless `PDTEST_SANDBOX` is set, and the reason is what it
 * costs: a 65MB wasm build, a chromium, and a dev server. Those are worth a
 * minute in CI and are not worth them on every `npm test`.
 *
 * # Why it is not a unit test
 *
 * Because there is nothing here to unit test. Everything this file asserts is
 * true or false only in a page: whether the headers make `SharedArrayBuffer`
 * exist, whether the bundler resolves a worker, whether 65MB of Go starts,
 * whether calls travel a message port. The Go half already has tests and the
 * TypeScript half already has tests; what has never been checked is that the
 * two are the same program.
 *
 * Three things were wrong the first time this ran, and all three compiled,
 * linked and started -- see `../SANDBOX.md`.
 *
 * @module
 */

import { chromium, type Browser } from "playwright";
import { createServer, type ViteDevServer } from "vite";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

const enabled =
  process.env.PDTEST_SANDBOX !== undefined && process.env.PDTEST_SANDBOX !== "";
const port = 5177;

// 127.0.0.1 rather than localhost, because vite binds one address family and
// `localhost` resolves to the other often enough to be a flake nobody enjoys
// finding -- the symptom is a server plainly listening and a fetch that never
// connects.
const origin = `http://127.0.0.1:${port}`;

describe.runIf(enabled)("the sandbox", () => {
  let vite: ViteDevServer;
  let browser: Browser;

  beforeAll(async () => {
    // Vite in this process rather than a subprocess. Spawned as a CLI from
    // inside vitest it exits 1 with nothing on either stream, and the API
    // is what it is for -- it also means the config beside this file is the
    // one that applies, headers and all.
    vite = await createServer({
      server: { host: "127.0.0.1", port, strictPort: true },
    });
    await vite.listen();

    browser = await chromium.launch();
  }, 180_000);

  afterAll(async () => {
    await browser?.close();
    await vite?.close();
  });

  // The pages take the viewer's theme, and a media query is the kind of thing
  // that goes back to a hardcoded colour in an edit about something else. This
  // one loads `/`, which starts no wasm, so it costs a page load.
  it.each(["dark", "light"] as const)(
    "is a %s page for a %s viewer",
    async (scheme) => {
      const page = await browser.newPage({ colorScheme: scheme });
      await page.goto(`${origin}/`, { waitUntil: "load" });

      const back = await page.evaluate(
        () => getComputedStyle(document.body).backgroundColor,
      );

      // Parsed rather than matched, because what matters is which side of the
      // middle it is on and not which grey was picked.
      const lit =
        (back.match(/\d+/g) ?? ["255"])
          .slice(0, 3)
          .reduce((a, v) => a + Number(v), 0) / 3;

      expect(lit, `${scheme}: ${back}`).toBeLessThan(
        scheme === "dark" ? 64 : 256,
      );
      expect(lit, `${scheme}: ${back}`).toBeGreaterThan(
        scheme === "dark" ? -1 : 192,
      );

      await page.close();
    },
    60_000,
  );

  // The second visit, which is the one that matters for a 72MB module: the
  // browser's own cache will not hold an entry that size, so without the Cache
  // API every reload is the whole download again. It is a page of its own
  // rather than an assertion in the one above, because what is being tested is
  // what a *reload* does -- one context, two loads.
  //
  // Removing the cache from `start` puts this back to `from: network`.
  it("reads the module back on the next visit", async () => {
    const ctx = await browser.newContext();
    const page = await ctx.newPage();

    const from = async (): Promise<string> => {
      await page.goto(`${origin}/sandbox.html`, { waitUntil: "load" });
      await page.waitForFunction(
        () => (window as never as { __done?: boolean }).__done === true,
        null,
        { timeout: 240_000 },
      );

      const out =
        (await page.evaluate(
          () => (window as never as { __out?: string[] }).__out,
        )) ?? [];

      return out.find((v) => v.startsWith("from:")) ?? "nothing";
    };

    expect(await from()).toBe("from: network");
    expect(await from()).toBe("from: cache");

    await ctx.close();
  }, 300_000);

  it("serves the app the process serves", async () => {
    const page = await browser.newPage();
    const said: string[] = [];
    page.on("console", (m) => said.push(m.text()));

    await page.goto(`${origin}/sandbox.html`, { waitUntil: "load" });
    await page.waitForFunction(
      () => (window as never as { __done?: boolean }).__done === true,
      null,
      {
        timeout: 240_000,
      },
    );

    const out =
      (await page.evaluate(
        () => (window as never as { __out?: string[] }).__out,
      )) ?? [];
    const say = out.join("\n");

    // The module arriving, which is 72MB and the whole of a cold start: a
    // page with nothing to draw there looks hung. See `sandbox.html`.
    expect(say, said.join("\n")).toContain("progress ticks: many");
    expect(say).toContain("progress rises: true");
    expect(say).toContain("progress ends full: true");

    // The headers, which are the thing that fails confusingly.
    expect(say, said.join("\n")).toContain("crossOriginIsolated: true");
    expect(say).toContain("SharedArrayBuffer: function");

    // The whole app, in the page.
    expect(say, said.join("\n")).toContain("served");
    expect(say).toContain("tenant: acme");
    expect(say).toContain("robot: arm-01");
    expect(say).toContain("list: 1");

    // Calls at once, which a page makes and a process does not -- the database
    // is one worker thread here. See `sandbox.html` for what losing this looks
    // like, which is not a message about a lock.
    expect(say, said.join("\n")).toContain("at once read: 8");
    expect(say, said.join("\n")).toContain("at once write: 8");

    // The module was fetched, because this page had never been opened. The
    // second visit is a test of its own -- see below.
    expect(say, said.join("\n")).toContain("from: network");

    // And the wall, which is the same answer it gives over HTTP: a row this
    // caller may not see is a row the query did not match.
    expect(say).toContain("wall: not_found");
  }, 300_000);
});
