import { expect, test, type Page } from "@playwright/test";
import { hasGo } from "./helpers.js";

// The presenter/viewer demo (examples/web-demo, index.html) against `tessera --dev`: a recorded
// presenter's tile can switch from WebRTC to the live HLS recording (rewind) and back.

test.skip(
  !hasGo() && process.env.TESSERA_E2E_URL === undefined,
  "Go toolchain not found: cannot start `tessera --dev`; set TESSERA_E2E_URL to use a running server",
);

/** Waits until the video element plays: frames decoded and time advancing. */
async function expectPlaying(page: Page, selector: string): Promise<void> {
  await expect
    .poll(
      async () =>
        page.locator(selector).evaluate(async (v: HTMLVideoElement) => {
          const t = v.currentTime;
          await new Promise((r) => setTimeout(r, 700));
          return v.readyState >= 2 && v.videoWidth > 0 && v.currentTime > t;
        }),
      { timeout: 30_000, message: `${selector} plays` },
    )
    .toBe(true);
}

test("a viewer rewinds a recorded presenter over HLS, returns to live, and keeps the recording when they end", async ({
  browser,
  browserName,
}) => {
  // Chromium only: Playwright's Firefox cannot send H.264 and its macOS WebKit has no WebRTC.
  test.skip(browserName !== "chromium", "needs H.264 WebRTC and MSE playback");
  test.setTimeout(120_000);
  const room = `e2e-live-${Date.now()}`;
  const ctxP = await browser.newContext();
  const ctxV = await browser.newContext();
  const presenter = await ctxP.newPage();
  const viewer = await ctxV.newPage();
  const logs: string[] = [];
  for (const [n, p] of [
    ["P", presenter],
    ["V", viewer],
  ] as const)
    p.on("console", (m) => logs.push(`${n}: ${m.text()}`));

  try {
    await presenter.goto(`/?room=${room}&name=Ann&record=1`);
    await presenter.getByRole("button", { name: "Go live" }).click();
    await expect(presenter.locator("#preview.recorded")).toBeVisible({
      timeout: 15_000,
    });

    await viewer.goto(`/?room=${room}&name=Vic`);
    await viewer.getByRole("button", { name: "Watch" }).click();
    const tile = viewer.locator("#watch-stage .tile").first();
    await expect(tile).toBeVisible({ timeout: 15_000 });
    await expectPlaying(viewer, "#watch-stage .tile video:not(.replay)");

    // Once the recording has its first segments, the presenter's attributes carry the
    // playlist and the tile offers Rewind.
    const replayBtn = tile.locator('[data-act="replay"]');
    await expect(replayBtn).toHaveText(/Rewind/, { timeout: 30_000 });
    // Let the recording grow so there is something to rewind over.
    await viewer.waitForTimeout(8_000);
    await replayBtn.click();

    const replay = "#watch-stage .tile video.replay";
    await expectPlaying(viewer, replay);
    await expect(replayBtn).toHaveText(/Live/);
    // The WebRTC camera is dropped while replaying.
    await expect(tile.locator("video:not(.replay)")).toHaveCount(0, {
      timeout: 10_000,
    });

    // The whole recording is seekable; jump back to its start and it keeps playing from there.
    const range = await viewer
      .locator(replay)
      .evaluate((v: HTMLVideoElement) => ({
        start: v.seekable.start(0),
        end: v.seekable.end(0),
      }));
    expect(range.end - range.start).toBeGreaterThan(6); // two or more 4 s segments
    await viewer
      .locator(replay)
      .evaluate(
        (v: HTMLVideoElement) => (v.currentTime = v.seekable.start(0) + 0.5),
      );
    await expectPlaying(viewer, replay);
    const pos = await viewer
      .locator(replay)
      .evaluate((v: HTMLVideoElement) => v.currentTime);
    expect(pos).toBeLessThan(range.end - 3);
    // Audio is in the recording too (Chromium counts decoded audio bytes).
    await expect
      .poll(
        () =>
          viewer
            .locator(replay)
            .evaluate(
              (v) =>
                (v as unknown as { webkitAudioDecodedByteCount: number })
                  .webkitAudioDecodedByteCount,
            ),
        {
          timeout: 10_000,
        },
      )
      .toBeGreaterThan(0);

    // Back to live: the player goes away and WebRTC video returns.
    await replayBtn.click();
    await expect(viewer.locator(replay)).toHaveCount(0);
    await expect(replayBtn).toHaveText(/Rewind/);
    await expectPlaying(viewer, "#watch-stage .tile video:not(.replay)");

    // Rewinding when the presenter ends: the tile stays and plays the finished recording.
    await replayBtn.click();
    await expectPlaying(viewer, replay);
    await presenter.locator("#p-leave").click();
    await expect(tile).toHaveClass(/ended/, { timeout: 15_000 });
    await expect(replayBtn).toBeHidden();
    await viewer
      .locator(replay)
      .evaluate((v: HTMLVideoElement) => (v.currentTime = 0.5));
    await expectPlaying(viewer, replay);
    await tile.locator('[data-act="close"]').click();
    await expect(viewer.locator("#watch-stage .tile")).toHaveCount(0);
  } catch (err) {
    console.log(logs.join("\n"));
    throw err;
  } finally {
    await ctxP.close();
    await ctxV.close();
  }
});

test("connection dot live and while rewinding; the recording's timeline marks an outage", async ({ browser, browserName }) => {
  test.skip(browserName !== "chromium", "needs H.264 WebRTC and MSE playback");
  test.setTimeout(150_000);
  const room = `e2e-cq-${Date.now()}`;
  const ctxP = await browser.newContext();
  const ctxV = await browser.newContext();
  // Keep the presenter's peer connections reachable, to cut its media below.
  await ctxP.addInitScript(() => {
    const w = window as unknown as { __pcs: RTCPeerConnection[] };
    w.__pcs = [];
    const Orig = window.RTCPeerConnection;
    window.RTCPeerConnection = new Proxy(Orig, {
      construct(target, args) {
        const pc = Reflect.construct(target, args) as RTCPeerConnection;
        w.__pcs.push(pc);
        return pc;
      },
    });
  });
  const presenter = await ctxP.newPage();
  const viewer = await ctxV.newPage();
  // An outage on the presenter's side: no media leaves it (as when its network drops).
  const cutMedia = (cut: boolean) =>
    presenter.evaluate((cut) => {
      const w = window as unknown as { __pcs: RTCPeerConnection[]; __saved?: [RTCRtpSender, MediaStreamTrack | null][] };
      if (cut) {
        w.__saved = w.__pcs.flatMap((pc) => pc.getSenders().map((s) => [s, s.track] as [RTCRtpSender, MediaStreamTrack | null]));
        return Promise.all(w.__saved.map(([s]) => s.replaceTrack(null)));
      }
      return Promise.all((w.__saved ?? []).map(([s, t]) => s.replaceTrack(t)));
    }, cut);
  try {
    await presenter.goto(`/?room=${room}&name=Ann&record=1`);
    await presenter.getByRole("button", { name: "Go live" }).click();
    await viewer.goto(`/?room=${room}&name=Vic`);
    await viewer.getByRole("button", { name: "Watch" }).click();
    const tile = viewer.locator("#watch-stage .tile").first();
    await expectPlaying(viewer, "#watch-stage .tile video:not(.replay)");
    // Live: the presenter's connection, on the tile and in the list.
    await expect(tile.locator(".label .cq")).toHaveAttribute("data-level", "good", { timeout: 20_000 });
    await expect(viewer.locator("#presenter-list .cq").first()).toHaveAttribute("data-level", "good");

    // A 7 s outage after a good stretch, then a good stretch again.
    await viewer.waitForTimeout(9_000);
    await cutMedia(true);
    await viewer.waitForTimeout(7_000);
    await cutMedia(false);
    await expectPlaying(viewer, "#watch-stage .tile video:not(.replay)"); // media flows again
    await viewer.waitForTimeout(9_000);

    // Rewind: the bar shows the timeline and the dot follows the playhead.
    await tile.locator('[data-act="replay"]').click();
    await expectPlaying(viewer, "#watch-stage .tile video.replay");
    const bar = tile.locator(".qbar");
    await expect(bar).toHaveAttribute("title", /Connection: \d+% good/, { timeout: 15_000 });
    const url = await viewer.evaluate(() => {
      const r = (window as unknown as { room: { remoteParticipants: Map<string, { attributes: Record<string, string> }> } }).room;
      const p = [...r.remoteParticipants.values()].find((x) => x.attributes.hls_url);
      return new URL(p!.attributes.hls_url!.replace(/master\.m3u8$/, "quality.json"), "http://127.0.0.1:7880").href;
    });
    // The timeline is written every 12 s while recording: wait until it covers the outage.
    let tl: { samples: { t: number; q: number }[] } = { samples: [] };
    await expect
      .poll(async () => {
        tl = (await (await viewer.request.get(url)).json()) as typeof tl;
        return tl.samples.filter((s) => s.q === 0).length > 2 && tl.samples.some((s) => s.q >= 2);
      }, { timeout: 40_000, intervals: [2_000] })
      .toBe(true);
    const bad = tl.samples.filter((s) => s.q >= 2);
    expect(bad.length, JSON.stringify(tl.samples)).toBeGreaterThan(0); // the outage
    expect(tl.samples.filter((s) => s.q === 0).length).toBeGreaterThan(2); // before and after it
    expect(await bar.getAttribute("style")).toContain("--q-");
    // Seek into the outage: the dot turns red there.
    await viewer.locator("#watch-stage .tile video.replay").evaluate((v: HTMLVideoElement, t) => (v.currentTime = t), bad[0]!.t + 1);
    await expect(tile.locator(".label .cq")).toHaveAttribute("data-level", /poor|lost/, { timeout: 10_000 });

    // The dashboard's player shows the same timeline with a summary.
    const admin = await ctxV.newPage();
    await admin.goto("http://127.0.0.1:7880/admin/");
    await admin.fill('input[name="k"]', "devkey");
    await admin.fill('input[name="s"]', "devsecret-devsecret-devsecret-0000");
    await admin.click('button[type="submit"]');
    await admin.click('nav a:text("Recordings")');
    await admin.getByRole("button", { name: /Watch live/ }).first().click();
    await expect(admin.locator(".qline .qsum")).toHaveText(/Connection: \d+% good.*(poor|no media)/, { timeout: 15_000 });
    await expect(admin.locator(".qline .cq")).toHaveAttribute("data-level", /good|fair|poor|lost/);
  } finally {
    await ctxP.close();
    await ctxV.close();
  }
});
