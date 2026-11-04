import { expect, test } from "@playwright/test";
import { expectVideoPlaying, hasGo, join } from "./helpers.js";

test.skip(
  !hasGo() && process.env.TESSERA_E2E_URL === undefined,
  "Go toolchain not found: cannot start `tessera --dev`; set TESSERA_E2E_URL to use a running server",
);

test("two participants exchange video, mute propagates and leaving is observed", async ({ browser }) => {
  const room = `e2e-${Date.now()}`;
  const ctxA = await browser.newContext();
  const ctxB = await browser.newContext();
  const a = await ctxA.newPage();
  const b = await ctxB.newPage();
  const logs: string[] = [];
  for (const [n, p] of [["A", a], ["B", b]] as const) p.on("console", (m) => logs.push(`${n}: ${m.text()}`));

  try {
    await join(a, room, "alice");
    await join(b, room, "bob");
    await expect(a.locator("#tile-bob")).toBeVisible({ timeout: 15_000 });
    await expect(b.locator("#tile-alice")).toBeVisible();

    await a.click("#cam");
    await a.click("#mic");
    await expectVideoPlaying(b, "alice");

    // Mute propagates to the other participant.
    await a.click("#mic");
    await expect(b.locator("#tile-alice.muted")).toBeVisible({ timeout: 10_000 });

    // Bob publishes too; Alice sees Bob.
    await b.click("#cam");
    await expectVideoPlaying(a, "bob");

    // Stats overlay shows a non-zero receive bitrate.
    await a.click("#stats-toggle");
    await expect(a.locator("#stats")).toContainText(/receive\s+[1-9]\d* kbps/, { timeout: 15_000 });

    // Leaving removes the tile.
    await b.click("#leave");
    await expect(a.locator("#tile-bob")).toHaveCount(0, { timeout: 15_000 });
  } catch (err) {
    console.log(logs.join("\n"));
    throw err;
  } finally {
    await ctxA.close();
    await ctxB.close();
  }
});

test("a bad token is rejected with a visible error", async ({ page }) => {
  const q = new URLSearchParams({ url: process.env.TESSERA_E2E_URL ?? "ws://127.0.0.1:7880", token: "nope", autojoin: "1" });
  await page.goto(`/?${q.toString()}`);
  await expect(page.locator("#error")).toBeVisible({ timeout: 20_000 });
  await expect(page.locator("#status")).toHaveText("disconnected");
});
