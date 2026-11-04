import { store } from '../store';
import { badge, esc, fmtBps, fmtBytes, fmtDur, fmtNum } from '../util';
import { ChartGroup, type View } from './view';

export class Overview implements View {
  private root!: HTMLElement;
  private cards!: HTMLElement;
  private alerts!: HTMLElement;
  private charts!: ChartGroup;

  mount(root: HTMLElement): void {
    this.root = root;
    root.innerHTML = `<h2>Overview</h2><div class="cards"></div><h3>Alerts</h3><div class="alerts"></div><h3>Last hour</h3><div class="grid charts"></div>`;
    this.cards = root.querySelector('.cards')!;
    this.alerts = root.querySelector('.alerts')!;
    const g = (this.charts = new ChartGroup(root.querySelector('.charts')!));
    const P = () => store.points;
    g.add({ title: 'CPU (100 = one core)', series: [{ label: 'process', color: '--c1', get: (i) => P()[i]!.cpu, fill: true }], fmt: (v) => v.toFixed(0) + '%' });
    g.add({ title: 'Memory', series: [{ label: 'RSS', color: '--c1', get: (i) => P()[i]!.rss }, { label: 'heap', color: '--c2', get: (i) => P()[i]!.heap }], fmt: fmtBytes });
    g.add({ title: 'Goroutines', series: [{ label: 'goroutines', color: '--c3', get: (i) => P()[i]!.goroutines }], fmt: (v) => fmtNum(v) });
    g.add({ title: 'GC (p99 pause, CPU share)', series: [{ label: 'pause ms', color: '--c4', get: (i) => P()[i]!.gc_pause_ns / 1e6 }, { label: 'gc cpu %', color: '--c5', get: (i) => P()[i]!.gc_cpu }], fmt: (v) => v.toFixed(2) });
    g.add({ title: 'UDP packets / s', series: [{ label: 'in', color: '--c1', get: (i) => P()[i]!.pkts_in }, { label: 'out', color: '--c2', get: (i) => P()[i]!.pkts_out }], fmt: (v) => fmtNum(v) });
    g.add({ title: 'Bandwidth', series: [{ label: 'in', color: '--c1', get: (i) => P()[i]!.bps_in }, { label: 'out', color: '--c2', get: (i) => P()[i]!.bps_out }], fmt: fmtBps });
    g.add({ title: 'Subscriber loss %', series: [{ label: 'avg', color: '--c3', get: (i) => P()[i]!.loss_avg }, { label: 'max', color: '--c6', get: (i) => P()[i]!.loss_max }], fmt: (v) => v.toFixed(1) + '%' });
    g.add({ title: 'RTT (ms)', series: [{ label: 'avg', color: '--c3', get: (i) => P()[i]!.rtt_avg }, { label: 'max', color: '--c6', get: (i) => P()[i]!.rtt_max }], fmt: (v) => v.toFixed(0) });
    g.add({ title: 'Totals', series: [{ label: 'participants', color: '--c1', get: (i) => P()[i]!.participants }, { label: 'published', color: '--c2', get: (i) => P()[i]!.published }, { label: 'subscribed', color: '--c3', get: (i) => P()[i]!.subscribed }, { label: 'recordings', color: '--c6', get: (i) => P()[i]!.recordings }], fmt: (v) => fmtNum(v) });
    this.update();
  }

  update(): void {
    const u = store.update;
    if (!u) { this.cards.innerHTML = '<p class="muted">Waiting for data…</p>'; return; }
    const n = u.snapshot.node;
    const card = (label: string, value: string, sub = '') => `<div class="card"><div class="label">${label}</div><div class="value">${esc(value)}</div><div class="sub">${esc(sub)}</div></div>`;
    this.cards.innerHTML =
      card('Rooms', fmtNum(n.rooms)) + card('Participants', fmtNum(n.participants), `${n.peer_connections} peer connections`) +
      card('Tracks', `${n.published_tracks} ↑ / ${n.subscribed_tracks} ↓`) +
      card('CPU', `${(n.cpu_percent / Math.max(n.num_cpu, 1)).toFixed(0)}%`, `${n.num_cpu} cores`) +
      card('Memory', fmtBytes(n.rss_bytes), `heap ${fmtBytes(n.heap_bytes)}`) +
      card('Goroutines', fmtNum(n.goroutines), `${fmtNum(n.num_gc)} GCs, p99 ${(n.gc_pause_p99_ns / 1e6).toFixed(2)} ms`) +
      card('UDP in', fmtBps(n.udp_bytes_in_per_sec * 8), `${fmtNum(n.udp_packets_in_per_sec)} pkt/s`) +
      card('UDP out', fmtBps(n.udp_bytes_out_per_sec * 8), `${fmtNum(n.udp_packets_out_per_sec)} pkt/s`) +
      card('Uptime', fmtDur(Date.now() - Date.parse(n.started_at)), `${n.node_id} ${n.version}`);
    this.alerts.innerHTML = u.alerts.length
      ? u.alerts.map((a) => `<div class="alert ${a.severity}">${badge(a.kind, a.severity === 'critical' ? 'bad' : 'warn')} ${esc(a.message)} <span class="muted">since ${new Date(a.since).toLocaleTimeString()}</span></div>`).join('')
      : '<p class="ok-line">No active alerts.</p>';
    const pts = store.points;
    this.charts.update(pts.map((p) => p.t / 1000), pts.length);
  }

  destroy(): void { this.charts.destroy(); this.root.innerHTML = ''; }
}
