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
