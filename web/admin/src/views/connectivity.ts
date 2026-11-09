import { store } from '../store';
import type { Participant } from '../types';
import { avg, badge, esc, fmtNum, percentile, qualityBadge, role, table } from '../util';
import { ChartGroup, render, type View } from './view';

// A presenter relayed to an edge from its media node has no connection to this node.
const relayed = (p: Participant) => p.kind === 'relay' || ((p.published?.length ?? 0) > 0 && !p.ice_candidate_type);

const WORST = 100; // participants listed, worst first

const ORDER = ['lost', 'poor', 'unknown', 'good', 'excellent'];

export class Connectivity implements View {
  private root!: HTMLElement;
  private body!: HTMLElement;
  private charts!: ChartGroup;

  mount(root: HTMLElement): void {
    this.root = root;
    root.innerHTML = '<h2>Connectivity</h2><div class="body"></div><div class="grid charts"></div>';
    this.body = root.querySelector('.body')!;
    this.charts = new ChartGroup(root.querySelector('.charts')!);
    this.charts.add({ title: 'RTT (ms)', series: [{ label: 'avg', color: '--c3', get: (i) => store.points[i]!.rtt_avg }, { label: 'max', color: '--c6', get: (i) => store.points[i]!.rtt_max }], fmt: (v) => v.toFixed(0), int: true });
    this.update();
  }

  update(): void {
    const all: { room: string; p: Participant }[] = [];
    for (const r of store.update?.snapshot.rooms ?? []) for (const p of r.participants ?? []) if (!relayed(p)) all.push({ room: r.name, p });
    if (!all.length) { render(this.body, `<p class="muted">${store.update ? 'No participants on this node.' : 'Waiting for data…'}</p>`); this.charts.update(store.points.map((p) => p.t / 1000), store.points.length); return; }
    const count = (f: (p: Participant) => string): Map<string, number> => {
      const m = new Map<string, number>();
      all.forEach(({ p }) => m.set(f(p) || 'unknown', (m.get(f(p) || 'unknown') ?? 0) + 1));
      return m;
    };
    const bars = (m: Map<string, number>, label: string) => {
      const total = all.length;
      return `<div class="bars"><h3>${label}</h3>${[...m.entries()].sort((a, b) => b[1] - a[1]).map(([k, v]) =>
        `<div class="bar-row"><span class="bar-label">${esc(k)}</span><span class="bar"><i style="width:${(100 * v) / total}%"></i></span><span class="bar-val">${v} (${((100 * v) / total).toFixed(0)}%)</span></div>`).join('')}</div>`;
    };
    const ice = all.map(({ p }) => p.ice_setup_ms).filter((v) => v > 0);
    const dtls = all.map(({ p }) => p.dtls_setup_ms).filter((v) => v > 0);
    const relay = all.filter(({ p }) => p.ice_candidate_type === 'relay').length;
    const stat = (name: string, v: number[]) => `<div class="card"><div class="label">${name}</div><div class="value">${fmtNum(avg(v))} ms</div><div class="sub">p95 ${fmtNum(percentile(v, 95))} · max ${fmtNum(Math.max(0, ...v))}</div></div>`;
    const worst = [...all].sort((a, b) => ORDER.indexOf(a.p.connection_quality) - ORDER.indexOf(b.p.connection_quality) || b.p.rtt_ms - a.p.rtt_ms);
    const rows = worst.map(({ room, p }) => `<tr><td><a href="#/rooms/${encodeURIComponent(room)}/${encodeURIComponent(p.sid)}">${esc(p.identity)}</a> ${p.kind === 'loadtest' ? badge(p.kind) : ''}</td><td>${esc(role(p))}</td><td>${esc(room)}</td><td>${qualityBadge(p.connection_quality)} ${p.quality_score.toFixed(1)}</td>
      <td>${esc(p.ice_candidate_type || '–')}</td><td>${p.ice_setup_ms}</td><td>${p.dtls_setup_ms}</td><td>${fmtNum(p.rtt_ms)}</td></tr>`);
    render(this.body, `<div class="cards">
        <div class="card"><div class="label">TURN relay usage</div><div class="value">${((100 * relay) / all.length).toFixed(0)}%</div><div class="sub">${relay} of ${all.length} participants</div></div>
        ${stat('ICE setup', ice)}${stat('DTLS setup', dtls)}</div>
      <div class="grid">${bars(count((p) => p.ice_candidate_type), 'ICE candidate type')}${bars(count((p) => p.connection_quality), 'Connection quality')}</div>
      <h3>Participants (worst first)</h3>${table(['Participant', 'Role', 'Room', 'Quality', 'ICE type', 'ICE ms', 'DTLS ms', 'RTT ms'], rows, '', WORST)}`);
    this.charts.update(store.points.map((p) => p.t / 1000), store.points.length);
  }

  destroy(): void { this.charts.destroy(); this.root.innerHTML = ''; }
}
