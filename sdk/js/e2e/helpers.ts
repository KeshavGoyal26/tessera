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

/** Records every RTCPeerConnection so a failing wait can dump transport state. */
const TRACK_PCS = () => {
  const w = window as unknown as { __allPcs?: RTCPeerConnection[] };
  w.__allPcs = [];
  const Orig = window.RTCPeerConnection;
  window.RTCPeerConnection = new Proxy(Orig, {
    construct(target, args) {
      const pc = Reflect.construct(target, args) as RTCPeerConnection;
      w.__allPcs?.push(pc);
      return pc;
    },
  });
};

/** One-line-per-fact dump of the page's WebRTC state, for CI logs of a failed wait. */
export async function diagnose(page: Page, label: string): Promise<string> {
  const out = await page
    .evaluate(async () => {
      const lines: string[] = [];
      lines.push(`error: ${document.querySelector("#error")?.textContent ?? ""}`);
      lines.push(`status: ${document.querySelector("#status")?.textContent ?? ""}`);
      lines.push(`toast: ${document.querySelector("#toast")?.textContent ?? ""}`);
      lines.push(`gum: ${typeof navigator.mediaDevices?.getUserMedia} ua=${navigator.userAgent.slice(-40)}`);
      lines.push(`tiles: ${[...document.querySelectorAll("[id^=tile-]")].map((t) => t.id + ":" + t.className).join(" | ")}`);
      for (const v of document.querySelectorAll<HTMLVideoElement>("video"))
        lines.push(`video ${v.closest("[id]")?.id}: ${v.videoWidth}x${v.videoHeight} ready=${v.readyState} paused=${v.paused} err=${v.error?.message ?? ""}`);
      const pcs = (window as unknown as { __allPcs?: RTCPeerConnection[] }).__allPcs ?? [];
      let i = 0;
      for (const pc of pcs) {
        lines.push(`pc${i} conn=${pc.connectionState} ice=${pc.iceConnectionState} sig=${pc.signalingState}`);
        for (const t of pc.getTransceivers())
          lines.push(`  tr mid=${t.mid} dir=${t.direction}/${t.currentDirection} kind=${t.receiver.track.kind} sender=${t.sender.track?.readyState ?? "-"}`);
        const stats = await pc.getStats();
        stats.forEach((r: Record<string, unknown>) => {
          if (r.type === "outbound-rtp" || r.type === "inbound-rtp")
            lines.push(`  ${String(r.type)} ${String(r.kind)} bytes=${String(r.bytesSent ?? r.bytesReceived)} frames=${String(r.framesEncoded ?? r.framesDecoded)} codec=${String(r.codecId)}`);
          if (r.type === "codec") lines.push(`  codec ${String(r.id)} ${String(r.mimeType)} ${String(r.sdpFmtpLine ?? "")}`);
          if (r.type === "candidate-pair" && r.state === "succeeded") lines.push(`  pair ${JSON.stringify({ n: r.nominated, rtt: r.currentRoundTripTime })}`);
        });
        i++;
      }
      return lines.join("\n");
    })
    .catch((e: unknown) => `diagnose failed: ${String(e)}`);
  return `--- ${label} ---\n${out}`;
}

const joined = new Set<Page>();
const pageLogs = new WeakMap<Page, string[]>();

export async function join(page: Page, room: string, identity: string): Promise<void> {
  joined.add(page);
  const logs: string[] = [];
  pageLogs.set(page, logs);
  page.on("console", (m) => {
    if (m.type() === "error" || m.type() === "warning") logs.push(`console.${m.type()}: ${m.text()}`);
  });
  page.on("pageerror", (e) => logs.push(`pageerror: ${e.message}`));
  await page.addInitScript(TRACK_PCS);
  const token = mintToken(room, identity);
  const q = new URLSearchParams({ url: SERVER, token, autojoin: "1" });
  await page.goto(`/?${q.toString()}`);
  await page.locator("#status[data-state=connected]").waitFor({ timeout: 20_000 });
}

/** Resolves when a <video> inside the tile of `identity` is rendering frames. */
export async function expectVideoPlaying(page: Page, identity: string): Promise<void> {
  try {
    await waitVideo(page, identity);
  } catch (err) {
    for (const p of joined) {
      if (p.isClosed()) continue;
      console.log(await diagnose(p, `${p === page ? "waiting page" : "other page"} (video of ${identity})`));
      console.log((pageLogs.get(p) ?? []).slice(-15).join("\n"));
    }
    throw err;
  }
}

async function waitVideo(page: Page, identity: string): Promise<void> {
  await page.waitForFunction(
    (id) => {
      const v = document.querySelector<HTMLVideoElement>(`#tile-${id} video`);
      return !!v && v.videoWidth > 0 && v.readyState >= 2 && !v.paused;
    },
    identity,
    { timeout: 30_000 },
  );
}
