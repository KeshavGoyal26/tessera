import { history, live, nodeHistory, session, stream } from './api';
import type { Alert, Live, Point } from './types';

const NODE_POLL_MS = 2000;

class Store {
  /** The latest live frame (small and fixed-size); lists and details are fetched by the views. */
  live?: Live;
  points: Point[] = [];
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
        try { const u = await live({ node }); if (!stopped) { this.ingest(u); this.setConnected(true); } } catch { if (!stopped) this.setConnected(false); }
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
    this.live = undefined;
    this.points = [];
    this.emit();
    void this.start();
  }
  /** Actions on another node's participants go through the admin forwarder, which edges have. */
  get canAct(): boolean { return !this.node || session.cluster?.role === 'edge'; }
  private setConnected(c: boolean): void { if (c !== this.connected) { this.connected = c; this.emit(); } }

  private ingest(l: Live): void {
    this.live = l;
    const last = this.points[this.points.length - 1];
    if (!last || l.point.t > last.t) this.points.push(l.point);
    if (this.points.length > 3600) this.points.shift();
    this.emit();
  }
  get alerts(): Alert[] { return this.live?.alerts ?? []; }
}

export const store = new Store();
