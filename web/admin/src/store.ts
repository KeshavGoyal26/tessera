import { history, stream } from './api';
import type { Alert, Point, Update } from './types';

export interface TrackPoint { t: number; bitrate: number; loss: number; jitter: number; fps: number; layers: number[]; rtt: number; bwe: number; spatial: number }

const MAX_TRACK_POINTS = 900; // 15 minutes at 1 Hz, kept in the browser

class Store {
  update?: Update;
  points: Point[] = [];
  /** Per-track history collected in the browser, key "pub:<sid>" or "sub:<participant>:<sid>". */
  tracks = new Map<string, TrackPoint[]>();
  connected = false;
  private listeners = new Set<() => void>();
  private stop?: () => void;

  on(fn: () => void): () => void { this.listeners.add(fn); return () => this.listeners.delete(fn); }
  private emit(): void { this.listeners.forEach((f) => f()); }

  async start(): Promise<void> {
    try { this.points = (await history()).points; } catch { this.points = []; }
    this.stop = stream((u) => this.ingest(u), (c) => { if (c !== this.connected) { this.connected = c; this.emit(); } });
  }
  close(): void { this.stop?.(); this.stop = undefined; this.connected = false; }

  private ingest(u: Update): void {
    this.update = u;
    const last = this.points[this.points.length - 1];
    if (!last || u.point.t > last.t) this.points.push(u.point);
    if (this.points.length > 3600) this.points.shift();
    const t = u.point.t;
    const seen = new Set<string>();
    for (const r of u.snapshot.rooms ?? []) {
      for (const p of r.participants ?? []) {
        for (const tr of p.published ?? []) {
          const key = `pub:${tr.sid}`; seen.add(key);
          const top = (tr.layers ?? []).find((l) => l.active && l.height > 0);
          this.push(key, { t, bitrate: tr.bitrate_bps, loss: tr.loss_percent, jitter: tr.jitter_ms, fps: top?.fps ?? 0,
            layers: [0, 1, 2].map((i) => (tr.layers ?? []).find((l) => l.spatial === i)?.bitrate_bps ?? 0), rtt: 0, bwe: 0, spatial: -1 });
        }
        for (const tr of p.subscribed ?? []) {
          const key = `sub:${p.sid}:${tr.track_sid}`; seen.add(key);
          this.push(key, { t, bitrate: tr.bitrate_bps, loss: tr.loss_percent, jitter: tr.jitter_ms, fps: 0, layers: [], rtt: tr.rtt_ms,
            bwe: tr.bandwidth_estimate_bps, spatial: tr.current_spatial });
        }
      }
    }
    for (const k of this.tracks.keys()) if (!seen.has(k)) this.tracks.delete(k);
    this.emit();
  }
  private push(key: string, p: TrackPoint): void {
    let a = this.tracks.get(key);
    if (!a) this.tracks.set(key, (a = []));
    a.push(p);
    if (a.length > MAX_TRACK_POINTS) a.shift();
  }
  get alerts(): Alert[] { return this.update?.alerts ?? []; }
}

export const store = new Store();
