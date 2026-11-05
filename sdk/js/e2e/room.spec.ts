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

test("subscriber layer selection changes the received resolution", async ({ browser }) => {
  const room = `e2e-layers-${Date.now()}`;
  const ctxA = await browser.newContext();
  const ctxB = await browser.newContext();
  const a = await ctxA.newPage();
  const b = await ctxB.newPage();
  try {
    await join(a, room, "alice");
    await join(b, room, "bob");
    await a.click("#cam");
    await expectVideoPlaying(b, "alice");
    const width = () => b.evaluate(() => document.querySelector<HTMLVideoElement>("#tile-alice video")?.videoWidth ?? 0);
    // The top layer is the publisher's capture size, which depends on the browser's fake camera
    // (640 px in Chromium, smaller in Firefox), so compare against what Alice captures.
    const captured = () => a.evaluate(() => document.querySelector<HTMLVideoElement>("#tile-alice video")?.videoWidth ?? 0);
    await expect.poll(captured, { timeout: 10_000 }).toBeGreaterThan(0);
    const full = Math.min(640, await captured());
    await b.selectOption("#quality", "high");
    await expect.poll(width, { timeout: 20_000 }).toBeGreaterThanOrEqual(full);
    const high = await width();
    await b.selectOption("#quality", "low");
    await expect.poll(width, { timeout: 20_000 }).toBeLessThan(high);
  } finally {
    await ctxA.close();
    await ctxB.close();
  }
});

// The server drops data packets for a subscriber whose data channel is not open yet (no
// buffering), so the sender repeats until the first packet arrives and the test then checks it.
test("data packets are delivered between participants", async ({ browser }) => {
  const room = `e2e-data-${Date.now()}`;
  const ctxA = await browser.newContext();
  const ctxB = await browser.newContext();
  const a = await ctxA.newPage();
  const b = await ctxB.newPage();
  try {
    await join(a, room, "alice");
    await join(b, room, "bob");
    await expect(a.locator("#tile-bob")).toBeVisible();
    await b.evaluate(() => {
      const r = (window as unknown as { room: { on(e: string, f: (p: Uint8Array, who: { identity: string } | undefined, topic?: string) => void): void } }).room;
      (window as unknown as { got: string[] }).got = [];
      r.on("dataReceived", (payload, who, topic) =>
        (window as unknown as { got: string[] }).got.push(`${who?.identity}:${topic}:${new TextDecoder().decode(payload)}`),
      );
    });
    const got = () => b.evaluate(() => (window as unknown as { got: string[] }).got);
    await expect
      .poll(
        async () => {
          await a.evaluate(async () => {
            const r = (window as unknown as { room: { localParticipant: { publishData(p: Uint8Array, o: object): Promise<void> } } }).room;
            await r.localParticipant.publishData(new TextEncoder().encode("hello"), { topic: "chat" });
          });
          return (await got()).length;
        },
        { timeout: 15_000, intervals: [500] },
      )
      .toBeGreaterThan(0);
    expect((await got())[0]).toBe("alice:chat:hello");
  } finally {
    await ctxA.close();
    await ctxB.close();
  }
});

test("the client reconnects after a network outage and media resumes", async ({ browser, browserName }) => {
  // Observed in CI: context.setOffline(true) leaves Firefox's WebSocket and WebRTC traffic
  // untouched (status stays "connected" for 30 s), so the outage cannot be simulated there.
  test.skip(browserName === "firefox", "Playwright setOffline does not interrupt WebSocket/WebRTC in Firefox");
  const room = `e2e-reconnect-${Date.now()}`;
  const ctxA = await browser.newContext();
  const ctxB = await browser.newContext();
  const a = await ctxA.newPage();
  const b = await ctxB.newPage();
  const logs: string[] = [];
  for (const [n, p] of [["A", a], ["B", b]] as const) {
    p.on("console", (m) => logs.push(`${n}: ${m.text()}`));
    p.on("pageerror", (e) => logs.push(`${n} ERROR: ${e.message}`));
  }
  try {
    await join(a, room, "alice");
    await join(b, room, "bob");
    await a.click("#cam");
    await expectVideoPlaying(b, "alice");
    await ctxA.setOffline(true);
    await expect(a.locator("#status")).toHaveText("reconnecting", { timeout: 30_000 });
    await ctxA.setOffline(false);
    await expect(a.locator("#status")).toHaveText("connected", { timeout: 45_000 });
    // Bob still sees Alice and a fresh frame flow.
    await expect(b.locator("#tile-alice")).toBeVisible();
    const t0 = await b.evaluate(() => document.querySelector<HTMLVideoElement>("#tile-alice video")?.currentTime ?? 0);
    await expect.poll(() => b.evaluate(() => document.querySelector<HTMLVideoElement>("#tile-alice video")?.currentTime ?? 0), { timeout: 20_000 }).toBeGreaterThan(t0 + 1);
  } catch (err) {
    console.log(logs.join("\n"));
    throw err;
  } finally {
    await ctxA.close();
    await ctxB.close();
  }
});

test("screen share is published and shown to the other participant", async ({ browser, browserName }) => {
  test.skip(browserName !== "chromium", "automatic screen selection is only configured for Chromium");
  const room = `e2e-screen-${Date.now()}`;
  const ctxA = await browser.newContext();
  const ctxB = await browser.newContext();
  const a = await ctxA.newPage();
  const b = await ctxB.newPage();
  try {
    await join(a, room, "alice");
    await join(b, room, "bob");
    await a.click("#screen");
    await expectVideoPlaying(b, "alice-screen");
    await a.click("#screen");
    await expect(b.locator("#tile-alice-screen")).toHaveCount(0, { timeout: 15_000 });
  } finally {
    await ctxA.close();
    await ctxB.close();
  }
});
