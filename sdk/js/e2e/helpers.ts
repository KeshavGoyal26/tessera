import { spawnSync } from "node:child_process";
import type { Page } from "@playwright/test";

export const SERVER = process.env.TESSERA_E2E_URL ?? "ws://127.0.0.1:7880";

/** Mints a join token with the server's own `create-token` command (dev key). */
export function mintToken(room: string, identity: string): string {
  const r = spawnSync("go", ["run", "./cmd/tessera", "create-token", "--room", room, "--identity", identity, "--name", identity], {
    encoding: "utf8",
    cwd: "../..",
  });
  if (r.status !== 0) throw new Error(`create-token failed: ${r.stderr}`);
  return r.stdout.trim();
}

export function hasGo(): boolean {
  return spawnSync("go", ["version"]).status === 0;
}

export async function join(page: Page, room: string, identity: string): Promise<void> {
  const token = mintToken(room, identity);
  const q = new URLSearchParams({ url: SERVER, token, autojoin: "1" });
  await page.goto(`/?${q.toString()}`);
  await page.locator("#status[data-state=connected]").waitFor({ timeout: 20_000 });
}

/** Resolves when a <video> inside the tile of `identity` is rendering frames. */
export async function expectVideoPlaying(page: Page, identity: string): Promise<void> {
  await page.waitForFunction(
    (id) => {
      const v = document.querySelector<HTMLVideoElement>(`#tile-${id} video`);
      return !!v && v.videoWidth > 0 && v.readyState >= 2 && !v.paused;
    },
    identity,
    { timeout: 30_000 },
  );
}
