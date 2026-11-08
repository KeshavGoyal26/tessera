// Connection quality, shown as a green / yellow / red dot: live from the server's
// connection-quality updates, and while rewinding from the recording's timeline
// (quality.json next to its master playlist, one sample every 4 s), also drawn as a bar.
import { ConnectionQuality } from "@tessera/client";

export type Level = "good" | "fair" | "poor" | "lost" | "unknown";

const WORDS: Record<Level, string> = { good: "good", fair: "unstable", poor: "poor", lost: "no media", unknown: "unknown" };

export function liveLevel(q: ConnectionQuality): Level {
  switch (q) {
    case ConnectionQuality.EXCELLENT:
    case ConnectionQuality.GOOD:
      return "good";
    case ConnectionQuality.POOR:
      return "fair";
    case ConnectionQuality.LOST:
      return "lost";
  }
  return "unknown";
}

/** Sets a dot element's level and tooltip. */
export function setDot(dot: Element | null, level: Level, what = "Connection"): void {
  if (!dot) return;
  (dot as HTMLElement).dataset.level = level;
  dot.setAttribute("title", `${what}: ${WORDS[level]}`);
  dot.setAttribute("aria-label", `${what}: ${WORDS[level]}`);
}

export interface Sample { t: number; q: number; loss: number; gap_ms: number; kbps: number }
export interface Timeline { step_ms: number; final: boolean; samples: Sample[] | null }

const REC: Level[] = ["good", "fair", "poor", "lost"];
const COLOR: Record<Level, string> = { good: "var(--q-good)", fair: "var(--q-fair)", poor: "var(--q-poor)", lost: "var(--q-lost)", unknown: "transparent" };

export const timelineUrl = (master: string): string => master.replace(/master\.m3u8(\?.*)?$/, "quality.json");

export function levelAt(tl: Timeline, t: number): Level {
  const step = tl.step_ms / 1000;
  const s = (tl.samples ?? []).find((x) => t >= x.t && t < x.t + step);
  return s ? REC[s.q] ?? "unknown" : "unknown";
}

/** "Connection: 90% good · 6% unstable · 2% poor · 2% no media (1 drop)" */
export function summary(tl: Timeline): string {
  const ss = tl.samples ?? [];
  if (!ss.length) return "Connection: no data yet";
  const n = [0, 0, 0, 0];
  let drops = 0;
  ss.forEach((s, i) => {
    n[s.q] = (n[s.q] ?? 0) + 1;
    if (s.q === 3 && ss[i - 1]?.q !== 3) drops++;
  });
  const pct = (k: number) => Math.round((n[k]! * 100) / ss.length);
  const parts = [`${pct(0)}% good`];
  if (n[1]) parts.push(`${pct(1)}% unstable`);
  if (n[2]) parts.push(`${pct(2)}% poor`);
  if (n[3]) parts.push(`${pct(3)}% no media (${drops} drop${drops > 1 ? "s" : ""})`);
  return `Connection: ${parts.join(" · ")}`;
}

/** The timeline as a CSS gradient over [0, total] seconds. */
export function gradient(tl: Timeline, total: number): string {
  const step = tl.step_ms / 1000;
  const stops: string[] = [];
  for (const s of tl.samples ?? []) {
    const a = Math.min(100, (s.t / total) * 100), b = Math.min(100, ((s.t + step) / total) * 100);
    const c = COLOR[REC[s.q] ?? "unknown"];
    stops.push(`${c} ${a.toFixed(2)}%`, `${c} ${b.toFixed(2)}%`);
  }
  return stops.length ? `linear-gradient(to right, transparent 0%, ${stops.join(", ")}, transparent 100%)` : "none";
}

/** Draws the timeline of url in bar (click to seek), calling onLevel with the level at the
 *  playhead and the summary. Re-fetched every 10 s until the recording is final. */
export function attachTimeline(video: HTMLVideoElement, bar: HTMLElement, url: string, onLevel: (l: Level, summary: string) => void): () => void {
  let tl: Timeline | undefined, timer = 0, stopped = false;
  const head = document.createElement("i");
  head.className = "qhead";
  bar.replaceChildren(head);
  const total = () => {
    const end = Number.isFinite(video.duration) ? video.duration : video.seekable.length ? video.seekable.end(0) : 0;
    const last = tl?.samples?.at(-1);
    return Math.max(end, last ? last.t + tl!.step_ms / 1000 : 0, 1);
  };
  const draw = () => {
    if (!tl) return;
    bar.style.backgroundImage = gradient(tl, total());
    bar.title = summary(tl);
    bar.setAttribute("aria-label", bar.title);
    head.style.left = `${Math.min(100, (video.currentTime / total()) * 100)}%`;
    onLevel(levelAt(tl, video.currentTime), bar.title);
  };
  const load = async () => {
    try {
      const r = await fetch(url, { cache: "no-store" });
      if (r.ok) tl = (await r.json()) as Timeline;
    } catch { /* not written yet */ }
    draw();
    if (!stopped && !tl?.final) timer = window.setTimeout(load, 10_000);
  };
  const seek = (e: MouseEvent) => {
    const box = bar.getBoundingClientRect();
    video.currentTime = Math.max(0, ((e.clientX - box.left) / box.width) * total());
  };
  video.addEventListener("timeupdate", draw);
  bar.addEventListener("click", seek);
  void load();
  return () => {
    stopped = true;
    window.clearTimeout(timer);
    video.removeEventListener("timeupdate", draw);
    bar.removeEventListener("click", seek);
  };
}
