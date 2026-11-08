import { history, nodeHistory, nodeUpdate, session, stream } from './api';
import type { Alert, Point, Update } from './types';
import { sortSnapshot } from './util';

export interface TrackPoint { t: number; bitrate: number; loss: number; jitter: number; rtt: number; bwe: number }

const MAX_TRACK_POINTS = 900; // 15 minutes at 1 Hz, kept in the browser
const NODE_POLL_MS = 2000;

class Store {
  update?: Update;
  points: Point[] = [];
  /** Per-track history collected in the browser, key "pub:<sid>" or "sub:<participant>:<sid>". */
  tracks = new Map<string, TrackPoint[]>();
  connected = false;
  /** The node shown by the node views: '' for this node (live stream), else another
   *  cluster node, polled through this dashboard every NODE_POLL_MS while the tab is visible. */
  node = '';
  private listeners = new Set<() => void>();
  private stop?: () => void;

  on(fn: () => void): () => void { this.listeners.add(fn); return () => this.listeners.delete(fn); }
  private emit(): void { this.listeners.forEach((f) => f()); }

  async start(): Promise<void> {
    const node = this.node;
    if (!node) {
      try { this.points = (await history()).points; } catch { this.points = []; }
      this.stop = stream((u) => this.ingest(u), (c) => this.setConnected(c));
      return;
    }
    try { this.points = (await nodeHistory(node)).points; } catch { this.points = []; }
    let timer = 0, stopped = false;
    const poll = async () => {
      if (!document.hidden) {
        try { const u = await nodeUpdate(node); if (!stopped) { this.ingest(u); this.setConnected(true); } } catch { if (!stopped) this.setConnected(false); }
      }
      if (!stopped) timer = window.setTimeout(poll, NODE_POLL_MS);
    };
    void poll();
    this.stop = () => { stopped = true; window.clearTimeout(timer); };
  }
  close(): void { this.stop?.(); this.stop = undefined; this.connected = false; }

  /** Switches the node views to another cluster node ('' = this one). */
  setNode(id: string): void {
    if (id === session.cluster?.self) id = '';
    if (id === this.node) return;
    this.close();
    this.node = id;
    this.update = undefined;
    this.points = [];
    this.tracks.clear();
    this.emit();
    void this.start();
  }
  /** Actions on another node's participants go through the admin forwarder, which edges have. */
  get canAct(): boolean { return !this.node || session.cluster?.role === 'edge'; }
  private setConnected(c: boolean): void { if (c !== this.connected) { this.connected = c; this.emit(); } }

  private ingest(u: Update): void {
    sortSnapshot(u.snapshot);
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
          this.push(key, { t, bitrate: tr.bitrate_bps, loss: tr.loss_percent, jitter: tr.jitter_ms, rtt: 0, bwe: 0 });
        }
        for (const tr of p.subscribed ?? []) {
          const key = `sub:${p.sid}:${tr.track_sid}`; seen.add(key);
          this.push(key, { t, bitrate: tr.bitrate_bps, loss: tr.loss_percent, jitter: tr.jitter_ms, rtt: tr.rtt_ms, bwe: tr.bandwidth_estimate_bps });
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
