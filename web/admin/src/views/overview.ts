import { alertHistory, history } from '../api';
import type { AlertEvent } from '../types';
import { PagedList, listParams, type Column } from './paged';
import { store } from '../store';
import type { Point } from '../types';
import { badge, esc, fmtBps, fmtBytes, fmtDur, fmtNum } from '../util';
import { ChartGroup, render, type View } from './view';

export class Overview implements View {
  private root!: HTMLElement;
  private cards!: HTMLElement;
  private alerts!: HTMLElement;
  private charts!: ChartGroup;
  /** '' = the last hour (live), '24h' = the last day at 10 s, refetched every 30 s. */
  private range = '';
  private day: Point[] = [];
  private dayTimer = 0;
  /** Times of alerts raised (seconds), drawn as markers on the charts; refreshed every 30 s. */
  private marks: { t: number; color: string }[] = [];
  private markTimer = 0;
  private hist?: PagedList<AlertEvent>;

  mount(root: HTMLElement): void {
    this.root = root;
    root.innerHTML = `<h2>Overview</h2><div class="cards"></div><h3>Alerts</h3><div class="alerts" aria-live="polite"></div><div class="view-head alog-head"><h3>Alert history (24 h)</h3></div><div class="alog"></div><h3>History <span class="range"><button data-range="" aria-pressed="true">1 h</button> <button data-range="24h" aria-pressed="false">24 h</button></span></h3><div class="grid charts"></div>`;
    root.querySelector('.range')!.addEventListener('click', (e) => {
      const b = (e.target as HTMLElement).closest<HTMLButtonElement>('button[data-range]');
      if (b) this.setRange(b.dataset.range!);
    });
    this.cards = root.querySelector('.cards')!;
    this.alerts = root.querySelector('.alerts')!;
    const g = (this.charts = new ChartGroup(root.querySelector('.charts')!));
    const P = () => (this.range ? this.day : store.points);
    const loadMarks = () => alertHistory({ state: 'raised', limit: 500 }).then((p) => {
      this.marks = p.items.map((e) => ({ t: Date.parse(e.t) / 1000, color: e.severity === 'critical' ? '--bad' : '--warn' }));
    }).catch(() => undefined);
    void loadMarks();
    this.markTimer = window.setInterval(loadMarks, 30_000);
    const cols: Column<AlertEvent>[] = [
      { label: 'Time', get: (e) => e.t, cell: (e) => new Date(e.t).toLocaleString() },
      { label: 'State', get: (e) => e.state, cell: (e) => badge(e.state, e.state === 'raised' ? (e.severity === 'critical' ? 'bad' : 'warn') : 'ok') },
      { label: 'Kind', get: (e) => e.kind ?? '' },
      { label: 'Alert', get: (e) => e.message || e.id, cell: (e) => esc(e.message || e.id) },
    ];
    this.hist = new PagedList<AlertEvent>({ head: root.querySelector<HTMLElement>('.alog-head')!, body: root.querySelector<HTMLElement>('.alog')!,
      filterLabel: 'Filter alerts', sort: '', columns: cols, name: 'alerts', empty: 'No alerts in the last 24 h.', limit: 20,
      fetch: (st) => alertHistory({ ...listParams(st), node: store.node }) });
    g.add({ markers: () => this.marks, title: 'CPU (100 = one core)', series: [{ label: 'process', color: '--c1', get: (i) => P()[i]!.cpu, fill: true }], fmt: (v) => v.toFixed(0) + '%' });
    g.add({ markers: () => this.marks, title: 'Memory', series: [{ label: 'RSS', color: '--c1', get: (i) => P()[i]!.rss }, { label: 'heap', color: '--c2', get: (i) => P()[i]!.heap }], fmt: fmtBytes });
    g.add({ markers: () => this.marks, title: 'Goroutines', series: [{ label: 'goroutines', color: '--c3', get: (i) => P()[i]!.goroutines }], fmt: (v) => fmtNum(v), int: true });
    g.add({ markers: () => this.marks, title: 'GC (p99 pause, CPU share)', series: [{ label: 'pause ms', color: '--c4', get: (i) => P()[i]!.gc_pause_ns / 1e6 }, { label: 'gc cpu %', color: '--c5', get: (i) => P()[i]!.gc_cpu }], fmt: (v) => v.toFixed(2) });
    g.add({ markers: () => this.marks, title: 'UDP packets / s', series: [{ label: 'in', color: '--c1', get: (i) => P()[i]!.pkts_in }, { label: 'out', color: '--c2', get: (i) => P()[i]!.pkts_out }], fmt: (v) => fmtNum(v), int: true });
    g.add({ markers: () => this.marks, title: 'Bandwidth', series: [{ label: 'in', color: '--c1', get: (i) => P()[i]!.bps_in }, { label: 'out', color: '--c2', get: (i) => P()[i]!.bps_out }], fmt: fmtBps });
    g.add({ markers: () => this.marks, title: 'Subscriber loss %', series: [{ label: 'avg', color: '--c3', get: (i) => P()[i]!.loss_avg }, { label: 'max', color: '--c6', get: (i) => P()[i]!.loss_max }], fmt: (v) => v.toFixed(1) + '%' });
    g.add({ markers: () => this.marks, title: 'RTT (ms)', series: [{ label: 'avg', color: '--c3', get: (i) => P()[i]!.rtt_avg }, { label: 'max', color: '--c6', get: (i) => P()[i]!.rtt_max }], fmt: (v) => v.toFixed(0), int: true });
    g.add({ markers: () => this.marks, title: 'Totals', series: [{ label: 'participants', color: '--c1', get: (i) => P()[i]!.participants }, { label: 'published', color: '--c2', get: (i) => P()[i]!.published }, { label: 'subscribed', color: '--c3', get: (i) => P()[i]!.subscribed }, { label: 'recordings', color: '--c6', get: (i) => P()[i]!.recordings }], fmt: (v) => fmtNum(v), int: true });
    this.update();
  }

  update(): void {
    const u = store.live;
    if (!u) { render(this.cards, '<p class="muted">Waiting for data…</p>'); return; }
    const n = u.node;
    const card = (label: string, value: string, sub = '', cls = '') => `<div class="card ${cls}"><div class="label">${label}</div><div class="value">${esc(value)}</div><div class="sub">${esc(sub)}</div></div>`;
    const c = u.counts, q = u.quality; // the server always sends all five
    const recs = u.recorders.filter((r) => r.ok);
    const used = recs.reduce((a, s) => a + s.capacity_used, 0), cap = recs.reduce((a, s) => a + s.capacity, 0);
    const recCard = u.recorders.length
      ? card('Recording', `${recs.reduce((a, s) => a + s.active_recordings, 0)} active`, `${recs.reduce((a, s) => a + s.tracks_recording, 0)} tracks · capacity ${used} of ${cap} units · ${fmtNum(recs.reduce((a, s) => a + s.segments_written, 0))} segments, ${fmtBytes(recs.reduce((a, s) => a + s.bytes_uploaded, 0))} uploaded${recs.some((s) => s.upload_errors || s.track_errors) ? ` · ${fmtNum(recs.reduce((a, s) => a + s.upload_errors + s.track_errors, 0))} errors` : ''}${recs.length < u.recorders.length ? ` · ${u.recorders.length - recs.length} down` : ''}`, cap && used >= cap ? 'bad' : recs.length < u.recorders.length || (cap && used * 10 >= cap * 9) ? 'warn' : '')
      : '';
    const wh = u.webhooks ?? [];
    const sum = (k: 'sent' | 'failed' | 'retried' | 'dropped' | 'queued') => wh.reduce((a, w) => a + w[k], 0);
    const whErr = wh.find((w) => w.last_error);
    const whCard = wh.length
      ? card('Webhooks', `${fmtNum(sum('sent'))} sent`, `${fmtNum(sum('failed'))} failed · ${fmtNum(sum('retried'))} retried · ${fmtNum(sum('dropped'))} dropped · ${fmtNum(sum('queued'))} queued${whErr ? ` · last error: ${whErr.last_error} (${whErr.url})` : ''}`,
        sum('dropped') ? 'bad' : sum('failed') || sum('queued') > 100 ? 'warn' : '')
      : '';
    const ice = Object.entries(u.ice).sort((a, b) => b[1] - a[1]).map(([k, v]) => `${k || 'not connected yet'} ${fmtNum(v)}`).join(' · ');
    render(this.cards,
      card('Rooms', fmtNum(c.rooms)) +
      card('Participants', fmtNum(n.participants), `${fmtNum(c.presenters)} presenters · ${fmtNum(c.viewers)} viewers${c.relays ? ` · ${fmtNum(c.relays)} relay` : ''}${c.recorders ? ` · ${fmtNum(c.recorders)} recorder` : ''}`) +
      card('Connection quality', `${fmtNum(q.poor + q.lost)} poor or lost`, `${fmtNum(q.excellent)} excellent · ${fmtNum(q.good)} good · ${fmtNum(q.poor)} poor · ${fmtNum(q.lost)} lost · ${fmtNum(q.unknown)} unknown`, q.lost ? 'bad' : q.poor ? 'warn' : '') +
      card('ICE candidates', ice || 'none yet') + whCard +
      card('Tracks', `${n.published_tracks} ↑ / ${n.subscribed_tracks} ↓`, `${n.peer_connections} peer connections`) + recCard +
      card('CPU', `${(n.cpu_percent / Math.max(n.num_cpu, 1)).toFixed(0)}%`, `${n.num_cpu} cores`) +
      card('Memory', fmtBytes(n.rss_bytes), `heap ${fmtBytes(n.heap_bytes)}`) +
      card('Goroutines', fmtNum(n.goroutines), `${fmtNum(n.num_gc)} GCs, p99 ${(n.gc_pause_p99_ns / 1e6).toFixed(2)} ms`) +
      card('UDP in', fmtBps(n.udp_bytes_in_per_sec * 8), `${fmtNum(n.udp_packets_in_per_sec)} pkt/s`) +
      card('UDP out', fmtBps(n.udp_bytes_out_per_sec * 8), `${fmtNum(n.udp_packets_out_per_sec)} pkt/s`) +
      card('Uptime', fmtDur(Date.now() - Date.parse(n.started_at)), `${n.node_id} ${n.version}`));
    const more = u.alerts_total > u.alerts.length ? `<p class="muted">… and ${fmtNum(u.alerts_total - u.alerts.length)} more.</p>` : '';
    render(this.alerts, u.alerts.length
      ? u.alerts.map((a) => `<div class="alert ${a.severity}">${badge(a.kind, a.severity === 'critical' ? 'bad' : 'warn')} ${esc(a.message)} <span class="muted">since ${new Date(a.since).toLocaleTimeString()}</span></div>`).join('') + more
      : '<p class="ok-line">No active alerts.</p>');
    this.draw();
  }

  private draw(): void {
    const pts = this.range ? this.day : store.points;
    this.charts.update(pts.map((p) => p.t / 1000), pts.length);
  }

  private setRange(r: string): void {
    this.range = r;
    window.clearInterval(this.dayTimer);
    this.root.querySelectorAll<HTMLButtonElement>('.range button').forEach((b) => b.setAttribute('aria-pressed', String(b.dataset.range === r)));
    if (!r) { this.draw(); return; }
    const load = () => history(r).then((h) => { this.day = h.points; this.draw(); }).catch(() => undefined);
    void load();
    this.dayTimer = window.setInterval(load, 30_000);
  }

  destroy(): void { window.clearInterval(this.dayTimer); window.clearInterval(this.markTimer); this.hist?.destroy(); this.charts.destroy(); this.root.innerHTML = ''; }
}
