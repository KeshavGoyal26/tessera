import { expect, test } from "@playwright/test";
import { expectVideoPlaying, hasGo, join } from "./helpers.js";

// Video is H.264 only (ADR-0012). Playwright's Firefox ships without the OpenH264 plugin that
// Firefox downloads on first use, so it cannot encode or decode the video: video tests skip it.
const noH264 = (browserName: string) => browserName === "firefox";
const noH264Reason =
  "Playwright's Firefox has no OpenH264 plugin (H.264 is the only video codec)";

test.skip(
  !hasGo() && process.env.TESSERA_E2E_URL === undefined,
  "Go toolchain not found: cannot start `tessera --dev`; set TESSERA_E2E_URL to use a running server",
);

test("two participants exchange video, mute propagates and leaving is observed", async ({
  browser,
  browserName,
}) => {
  test.skip(noH264(browserName), noH264Reason);
  const room = `e2e-${Date.now()}`;
  const ctxA = await browser.newContext();
  const ctxB = await browser.newContext();
  const a = await ctxA.newPage();
  const b = await ctxB.newPage();
  const logs: string[] = [];
  for (const [n, p] of [
    ["A", a],
    ["B", b],
  ] as const)
    p.on("console", (m) => logs.push(`${n}: ${m.text()}`));

  try {
    await join(a, room, "alice");
    await join(b, room, "bob");
    await expect(a.locator("#tile-bob")).toBeVisible({ timeout: 15_000 });
    await expect(b.locator("#tile-alice")).toBeVisible();

    await a.click("#cam");
    await a.click("#mic");
    await expectVideoPlaying(b, "alice");

    // Mute propagates to the other participant (the tile shows .mic-off while muted or without a mic).
    await expect(b.locator("#tile-alice.mic-off")).toHaveCount(0, {
      timeout: 10_000,
    });
    await a.click("#mic");
    await expect(b.locator("#tile-alice.mic-off")).toBeVisible({
      timeout: 10_000,
    });

    // Bob publishes too; Alice sees Bob.
    await b.click("#cam");
    await expectVideoPlaying(a, "bob");

    // Stats overlay shows a non-zero receive bitrate.
    await a.click("#stats-toggle");
    await expect(a.locator("#stats")).toContainText(/receive\s+[1-9]\d* kbps/, {
      timeout: 15_000,
    });

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
  const q = new URLSearchParams({
    url: process.env.TESSERA_E2E_URL ?? "ws://127.0.0.1:7880",
    token: "nope",
    autojoin: "1",
  });
  await page.goto(`/call.html?${q.toString()}`);
  await expect(page.locator("#error")).toBeVisible({ timeout: 20_000 });
  await expect(page.locator("#status")).toHaveText("disconnected");
});

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
      const r = (
        window as unknown as {
          room: {
            on(
              e: string,
              f: (
                p: Uint8Array,
                who: { identity: string } | undefined,
                topic?: string,
              ) => void,
            ): void;
          };
        }
      ).room;
      (window as unknown as { got: string[] }).got = [];
      r.on("dataReceived", (payload, who, topic) =>
        (window as unknown as { got: string[] }).got.push(
          `${who?.identity}:${topic}:${new TextDecoder().decode(payload)}`,
        ),
      );
    });
    const got = () =>
      b.evaluate(() => (window as unknown as { got: string[] }).got);
    await expect
      .poll(
        async () => {
          await a.evaluate(async () => {
            const r = (
              window as unknown as {
                room: {
                  localParticipant: {
                    publishData(p: Uint8Array, o: object): Promise<void>;
                  };
                };
              }
            ).room;
            await r.localParticipant.publishData(
              new TextEncoder().encode("hello"),
              { topic: "chat" },
            );
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

test("the client reconnects after a network outage and media resumes", async ({
  browser,
  browserName,
}) => {
  test.skip(noH264(browserName), noH264Reason);
  // context.setOffline(true) leaves Firefox's and WebKit's WebSocket and WebRTC traffic untouched,
  // so the outage is simulated at the signaling layer in every browser: the WebSocket is cut and
  // new ones are refused until the network "returns". Chromium additionally goes offline for real.
  const room = `e2e-reconnect-${Date.now()}`;
  const ctxA = await browser.newContext();
  const ctxB = await browser.newContext();
  const a = await ctxA.newPage();
  const b = await ctxB.newPage();
  const logs: string[] = [];
  for (const [n, p] of [
    ["A", a],
    ["B", b],
  ] as const) {
    p.on("console", (m) => logs.push(`${n}: ${m.text()}`));
    p.on("pageerror", (e) => logs.push(`${n} ERROR: ${e.message}`));
  }
  let down = false;
  const open: { close(): void }[] = [];
  await ctxA.routeWebSocket(/\/rtc/, (ws) => {
    if (down) {
      void ws.close({ code: 1006 });
      return;
    }
    const server = ws.connectToServer();
    open.push(ws, server);
  });
  const setNetwork = async (up: boolean) => {
    down = !up;
    if (!up) for (const c of open.splice(0)) void c.close();
    if (browserName === "chromium") await ctxA.setOffline(!up);
  };
  try {
    await join(a, room, "alice");
    await join(b, room, "bob");
    await a.click("#cam");
    await expectVideoPlaying(b, "alice");
    await setNetwork(false);
    await expect(a.locator("#status")).toHaveText("reconnecting", {
      timeout: 30_000,
    });
    await setNetwork(true);
    await expect(a.locator("#status")).toHaveText("connected", {
      timeout: 45_000,
    });
    // Bob still sees Alice and a fresh frame flow.
    await expect(b.locator("#tile-alice")).toBeVisible();
    const t0 = await b.evaluate(
      () =>
        document.querySelector<HTMLVideoElement>("#tile-alice video")
          ?.currentTime ?? 0,
    );
    await expect
      .poll(
        () =>
          b.evaluate(
            () =>
              document.querySelector<HTMLVideoElement>("#tile-alice video")
                ?.currentTime ?? 0,
          ),
        { timeout: 20_000 },
      )
      .toBeGreaterThan(t0 + 1);
  } catch (err) {
    console.log(logs.join("\n"));
    throw err;
  } finally {
    await ctxA.close();
    await ctxB.close();
  }
});

// A network change must not drop the participant: ICE restarts in place on both connections,
// the participant keeps its sid and published tracks, and no reconnect cycle happens. (The
// outage itself cannot be simulated in every browser, so the restart is triggered through the
// same entry point as the `online` / `navigator.connection` change handlers.)
test("ICE restart on network change keeps the session and media", async ({
  browser,
  browserName,
}) => {
  test.skip(noH264(browserName), noH264Reason);
  const room = `e2e-ice-${Date.now()}`;
  const ctxA = await browser.newContext();
  const ctxB = await browser.newContext();
  await ctxA.addInitScript(() => {
    const w = window as unknown as { __iceRestarts: number; __pcs: number };
    w.__iceRestarts = 0;
    w.__pcs = 0;
    const Orig = window.RTCPeerConnection;
    const createOffer = Orig.prototype.createOffer as (
      this: RTCPeerConnection,
      o?: RTCOfferOptions,
    ) => Promise<RTCSessionDescriptionInit>;
    Orig.prototype.createOffer = function (
      this: RTCPeerConnection,
      o?: RTCOfferOptions,
    ) {
      if (o?.iceRestart) w.__iceRestarts++;
      return createOffer.call(this, o);
    } as typeof Orig.prototype.createOffer;
    window.RTCPeerConnection = new Proxy(Orig, {
      construct(target, args) {
        w.__pcs++;
        return Reflect.construct(target, args) as RTCPeerConnection;
      },
    });
  });
  const a = await ctxA.newPage();
  const b = await ctxB.newPage();
  type W = {
    room: {
      localParticipant: { sid: string };
      restartIce(): Promise<void>;
      on(e: string, f: () => void): void;
    };
    __iceRestarts: number;
    __pcs: number;
    __reconnecting: number;
  };
  const frames = (p: typeof a, id: string) =>
    p.evaluate(
      (i) =>
        document.querySelector<HTMLVideoElement>(`#tile-${i} video`)
          ?.currentTime ?? 0,
      id,
    );
  try {
    await join(a, room, "alice");
    await join(b, room, "bob");
    await a.click("#cam");
    await expectVideoPlaying(b, "alice");
    await b.click("#cam");
    await expectVideoPlaying(a, "bob");
    const before = await a.evaluate(() => {
      const w = window as unknown as W;
      w.__reconnecting = 0;
      w.room.on("reconnecting", () => w.__reconnecting++);
      return { sid: w.room.localParticipant.sid, pcs: w.__pcs };
    });

    // Alice restarts ICE (publisher by offer, subscriber by the server's restart offer).
    await a.evaluate(() => (window as unknown as W).room.restartIce());
    await expect
      .poll(() => a.evaluate(() => (window as unknown as W).__iceRestarts), {
        timeout: 10_000,
      })
      .toBeGreaterThan(0);

    // Media keeps flowing in both directions after the restart.
    const tB = await frames(b, "alice");
    const tA = await frames(a, "bob");
    await expect
      .poll(() => frames(b, "alice"), { timeout: 20_000 })
      .toBeGreaterThan(tB + 1);
    await expect
      .poll(() => frames(a, "bob"), { timeout: 20_000 })
      .toBeGreaterThan(tA + 1);

    // Same participant, same connections, no reconnect cycle, still connected.
    const after = await a.evaluate(() => {
      const w = window as unknown as W;
      return {
        sid: w.room.localParticipant.sid,
        pcs: w.__pcs,
        reconnecting: w.__reconnecting,
      };
    });
    expect(after).toEqual({
      sid: before.sid,
      pcs: before.pcs,
      reconnecting: 0,
    });
    await expect(a.locator("#status")).toHaveText("connected");
  } finally {
    await ctxA.close();
    await ctxB.close();
  }
});

test("screen share is published and shown to the other participant", async ({
  browser,
  browserName,
}) => {
  test.skip(noH264(browserName), noH264Reason);
  test.skip(
    browserName !== "chromium",
    "only Chromium can auto-select a screen to share under Playwright (Firefox and WebKit show a picker)",
  );
  const room = `e2e-screen-${Date.now()}`;
  const ctxA = await browser.newContext();
  const ctxB = await browser.newContext();
  const a = await ctxA.newPage();
  const b = await ctxB.newPage();
  try {
    await join(a, room, "alice");
    await join(b, room, "bob");
    // Screen capture only emits frames when the screen changes; keep something moving.
    await a.evaluate(() => {
      const d = document.createElement("div");
      d.style.cssText =
        "position:fixed;right:0;bottom:0;width:40px;height:40px;z-index:9999";
      document.body.append(d);
      let n = 0;
      const tick = () => {
        d.style.background = `hsl(${(n += 7) % 360} 90% 50%)`;
        requestAnimationFrame(tick);
      };
      tick();
    });
    await a.click("#screen");
    await expectVideoPlaying(b, "alice-screen");
    await a.click("#screen");
    await expect(b.locator("#tile-alice-screen")).toHaveCount(0, {
      timeout: 15_000,
    });
  } finally {
    await ctxA.close();
    await ctxB.close();
  }
});
