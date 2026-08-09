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

    // The headers, which are the thing that fails confusingly.
    expect(say, said.join("\n")).toContain("crossOriginIsolated: true");
    expect(say).toContain("SharedArrayBuffer: function");

    // The whole app, in the page.
    expect(say, said.join("\n")).toContain("served");
    expect(say).toContain("tenant: acme");
    expect(say).toContain("robot: arm-01");
    expect(say).toContain("list: 1");

    // And the wall, which is the same answer it gives over HTTP: a row this
    // caller may not see is a row the query did not match.
    expect(say).toContain("wall: not_found");
  }, 300_000);
});
