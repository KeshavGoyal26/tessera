import { participants } from '../api';
import { store } from '../store';
import type { ParticipantBrief, ParticipantPage, SetupStats } from '../types';
import { badge, esc, fmtNum, qualityBadge } from '../util';
import { listParams, PagedList, type Column } from './paged';
import { ChartGroup, type View } from './view';

const ORDER = ['excellent', 'good', 'poor', 'lost', 'unknown'];

/** Bars of a count per key; the share is of the sum so a bar never exceeds its row. */
export function bars(m: Record<string, number>, label: string, order?: string[]): string {
  const rank = (k: string) => (order ? order.indexOf(k) : 0);
  const entries = Object.entries(m).filter(([, v]) => v > 0).sort((a, b) => (order ? rank(a[0]) - rank(b[0]) : b[1] - a[1]));
  const total = entries.reduce((a, [, v]) => a + v, 0) || 1;
  return `<div class="bars"><h3>${label}</h3>${entries.map(([k, v]) =>
    `<div class="bar-row"><span class="bar-label">${esc(k || 'none yet')}</span><span class="bar"><i style="width:${(100 * v) / total}%"></i></span><span class="bar-val">${fmtNum(v)} (${((100 * v) / total).toFixed(0)}%)</span></div>`).join('')}</div>`;
}

const stat = (name: string, s: SetupStats) =>
  `<div class="card"><div class="label">${name}</div><div class="value">${fmtNum(s.avg)} ms</div><div class="sub">p95 ${fmtNum(s.p95)} · max ${fmtNum(s.max)}</div></div>`;

function extra(p: ParticipantPage): string {
  if (!p.total) return '';
  return `<div class="cards">
      <div class="card"><div class="label">TURN relay usage</div><div class="value">${((100 * p.relay) / p.total).toFixed(0)}%</div><div class="sub">${fmtNum(p.relay)} of ${fmtNum(p.total)} participants</div></div>
      ${stat('ICE setup', p.ice_setup_ms)}${stat('DTLS setup', p.dtls_setup_ms)}</div>
    <div class="grid">${bars(p.ice, 'ICE candidate type')}${bars(p.quality, 'Connection quality', ORDER)}</div>
    <h3>Participants (worst first)</h3>`;
}

const columns: Column<ParticipantBrief>[] = [
  { label: 'Participant', sort: 'identity', get: (p) => p.identity, cell: (p) => `<a href="#/rooms/${encodeURIComponent(p.room)}/${encodeURIComponent(p.sid)}">${esc(p.identity)}</a> ${p.kind === 'loadtest' ? badge(p.kind) : ''}` },
  { label: 'Role', get: (p) => p.role },
  { label: 'Room', sort: 'room', get: (p) => p.room },
  { label: 'Quality', sort: 'quality', get: (p) => p.quality, cell: (p) => `${qualityBadge(p.quality)} ${p.score.toFixed(1)}` },
  { label: 'ICE type', get: (p) => p.ice_type, cell: (p) => esc(p.ice_type || '–') },
  { label: 'ICE ms', get: (p) => p.ice_setup_ms },
  { label: 'DTLS ms', get: (p) => p.dtls_setup_ms },
  { label: 'RTT ms', sort: 'rtt', desc: true, get: (p) => p.rtt_ms, cell: (p) => fmtNum(p.rtt_ms) },
];

export class Connectivity implements View {
  private root!: HTMLElement;
  private charts!: ChartGroup;
  private list!: PagedList<ParticipantBrief, ParticipantPage>;

  mount(root: HTMLElement): void {
    this.root = root;
    root.innerHTML = '<div class="view-head"><h2>Connectivity</h2></div><div class="body"></div><div class="grid charts"></div>';
    this.charts = new ChartGroup(root.querySelector('.charts')!);
    this.charts.add({ title: 'RTT (ms)', series: [{ label: 'avg', color: '--c3', get: (i) => store.points[i]!.rtt_avg }, { label: 'max', color: '--c6', get: (i) => store.points[i]!.rtt_max }], fmt: (v) => v.toFixed(0), int: true });
    // Worst quality first; the aggregates cover the whole filtered set, not just the page.
    this.list = new PagedList<ParticipantBrief, ParticipantPage>({
      head: root.querySelector('.view-head')!, body: root.querySelector('.body')!, filterLabel: 'Filter participants', sort: 'quality', scope: () => store.node,
      empty: 'No participants on this node.', extra,
      columns, name: 'connectivity',
      fetch: (st) => participants({ ...listParams(st), connected: 1, node: store.node }), // own connections only
    });
    this.update();
  }

  update(): void {
    this.list.check();
    this.charts.update(store.points.map((p) => p.t / 1000), store.points.length);
  }

  destroy(): void { this.list.destroy(); this.charts.destroy(); this.root.innerHTML = ''; }
}
