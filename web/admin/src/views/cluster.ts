import { clusterHistory, clusterRoom, clusterState, session, userSessions } from '../api';
import { store } from '../store';
import type { ClusterParticipantBrief, ClusterPoint, ClusterRoom, ClusterState, NodeRoom, NodeView } from '../types';
import { badge, esc, fmtBps, fmtBytes, fmtDur, fmtNum, mergeRoom, nodeHealth, qualityBadge, table, safeDecode } from '../util';
import { handleAction, media } from './rooms';
import { clusterList } from './clusterlist';
import type { Column } from './paged';
import { ChartGroup, render, type View } from './view';

const STATE_MS = 5000; // the server samples the registry every 5 s
const ROOM_MS = 2000;

/** The latest cluster sample, shared with the header's node picker. */
export const cluster: { state?: ClusterState; listeners: Set<() => void> } = { listeners: new Set() };
export async function refreshCluster(): Promise<void> {
  try { cluster.state = await clusterState(); } catch { /* keep the last sample */ }
  cluster.listeners.forEach((f) => f());
}

/** Columns of a cluster-wide participant list (search results and Worst 50). */
const participantColumns: Column<ClusterParticipantBrief>[] = [
  { label: 'Participant', sort: 'identity', get: (p) => p.identity,
    cell: (p) => `<a href="#" data-node="${esc(p.node)}" data-hash="#/rooms/${encodeURIComponent(p.room)}/${encodeURIComponent(p.sid)}">${esc(p.identity)}</a>` },
  { label: 'Role', get: (p) => p.role, cell: (p) => badge(p.role, p.role === 'presenter' ? 'ok' : 'muted') },
  { label: 'Room', get: (p) => p.room, cell: (p) => `<a href="#" data-node="${esc(p.node)}" data-hash="#/rooms/${encodeURIComponent(p.room)}">${esc(p.room)}</a>` },
  { label: 'Node', get: (p) => p.node },
  { label: 'Quality', sort: 'quality', get: (p) => p.quality, cell: (p) => qualityBadge(p.quality) },
  { label: 'RTT', sort: 'rtt', desc: true, get: (p) => p.rtt_ms, cell: (p) => `${fmtNum(p.rtt_ms)} ms` },
  { label: 'Loss', sort: 'loss', desc: true, get: (p) => p.loss_percent, cell: (p) => `${p.loss_percent.toFixed(1)}%` },
  { label: 'Bitrate', sort: 'bitrate', desc: true, get: (p) => p.bitrate_bps, cell: (p) => fmtBps(p.bitrate_bps) },
  { label: 'ICE', get: (p) => p.ice_type, cell: (p) => esc(p.ice_type || '–') },
];

const roomColumns: Column<ClusterRoom>[] = [
  { label: 'Room', sort: 'name', get: (r) => r.name, cell: (r) => `<a href="#/cluster/room/${encodeURIComponent(r.name)}">${esc(r.name)}</a>` },
  { label: 'Presenters', sort: 'presenters', desc: true, get: (r) => r.presenters,
    cell: (r) => (r.presenters === 0 && r.viewers > 0 ? `0 ${badge('no presenter', 'warn')}` : fmtNum(r.presenters)) },
  { label: 'Viewers', sort: 'viewers', desc: true, get: (r) => r.viewers },
  { label: 'Media nodes', get: (r) => r.media.join(' '), cell: (r) => r.media.map(esc).join(', ') || '<span class="muted">none</span>' },
  { label: 'Edges', get: (r) => r.edges.join(' '), cell: (r) => r.edges.map(esc).join(', ') },
  { label: 'Recording', get: (r) => !!r.recording, cell: (r) => (r.recording ? badge('● recording', 'bad') : '') },
];

/** Bars of one value per node; max is the full bar (100 for a percentage, else the largest value). */
export function balance(title: string, rows: [string, number][], fmt: (v: number) => string, max?: number): string {
  const top = max ?? Math.max(1, ...rows.map((r) => r[1]));
  return `<div class="bars"><h4>${esc(title)}</h4>${rows.length ? rows.map(([k, v]) =>
    `<div class="bar-row"><span class="bar-label" title="${esc(k)}">${esc(k)}</span><span class="bar"><i style="width:${Math.min(100, (100 * v) / top)}%"></i></span><span class="bar-val">${esc(fmt(v))}</span></div>`).join('') : '<p class="muted">None.</p>'}</div>`;
}

/** Shows a node in the node views (Overview, Rooms, …) through this dashboard. */
export function openNode(id: string, hash = '#/overview'): void {
  store.setNode(id);
  location.hash = hash;
}

/** Runs fn now and every ms while the tab is visible; returns the stop function. */
function every(ms: number, fn: () => Promise<void>): () => void {
  let timer = 0, stopped = false;
  const run = async () => {
    if (!document.hidden) await fn();
    if (!stopped) timer = window.setTimeout(run, ms);
  };
  void run();
  return () => { stopped = true; window.clearTimeout(timer); };
}

/** #/cluster: nodes, totals, alerts, last hour and the room index. #/cluster/room/<name>: one room on every node. */
export class Cluster implements View {
  private root!: HTMLElement;
  private stops: (() => void)[] = [];
  private onClick = (e: Event) => this.click(e);
  private charts?: ChartGroup;
  private points: ClusterPoint[] = [];
  private lists: { destroy(): void }[] = [];
  private roomErr = '';
  private room?: string;
  private parts?: NodeRoom[];
  private onState = () => this.update();

  mount(root: HTMLElement, args: string[]): void {
    this.root = root;
    root.addEventListener('click', this.onClick);
    cluster.listeners.add(this.onState);
    if (args[0] === 'room' && args[1]) return this.mountRoom(safeDecode(args[1]));
    root.innerHTML = `<h2>Cluster</h2><div class="cards"></div><h3>Alerts</h3><div class="alerts" aria-live="polite"></div>
      <h3>Nodes</h3><div class="nodes"></div><h3>Last hour</h3><div class="grid charts"></div>
      <h3>Capacity</h3><div class="cards capacity"></div>
      <h3>Balance</h3><div class="grid balance"></div>
      <div class="view-head"><h3>Past sessions of a user</h3><form class="usearch"><input name="u" placeholder="Identity (exact)" aria-label="Identity"> <button>Look up</button></form></div><div class="uresult"></div>
      <div class="view-head"><h3>Search participants</h3></div><div class="psearch"></div>
      <h3>Worst 50 participants</h3><div class="worst"></div>
      <div class="view-head rooms-head"><h3>Rooms</h3></div><div class="rooms"></div>`;
    root.querySelector('.usearch')!.addEventListener('submit', (e) => { e.preventDefault(); void this.lookup((root.querySelector('input[name=u]') as HTMLInputElement).value.trim()); });
    // Every list below is searched, sorted and paged on the server (cluster/<endpoint>), merged from all nodes.
    this.lists.push(clusterList<ClusterParticipantBrief>({
      endpoint: 'participants', head: root.querySelector('.view-head')!, body: root.querySelector('.psearch')!, filterLabel: 'Search identity, name, room or session',
      sort: 'identity', columns: participantColumns, needsQuery: true, empty: 'Type to search participants on every node.', name: 'cluster-participants',
    }));
    this.lists.push(clusterList<ClusterParticipantBrief>({
      endpoint: 'participants', body: root.querySelector('.worst')!, sort: 'quality', limit: 50, pager: false, columns: participantColumns,
      empty: 'No participants in the cluster.', name: 'cluster-worst',
    }));
    this.lists.push(clusterList<ClusterRoom>({
      endpoint: 'rooms', head: root.querySelector('.rooms-head')!, body: root.querySelector('.rooms')!, filterLabel: 'Filter rooms',
      sort: 'name', columns: roomColumns, empty: 'No rooms in the cluster.', name: 'cluster-rooms',
    }));
    const g = (this.charts = new ChartGroup(root.querySelector('.charts')!));
    const P = () => this.points;
    g.add({ title: 'People', series: [{ label: 'viewers', color: '--c1', get: (i) => P()[i]!.viewers, fill: true }, { label: 'presenters', color: '--c2', get: (i) => P()[i]!.presenters }], fmt: (v) => fmtNum(v), int: true });
    g.add({ title: 'Bandwidth', series: [
      { label: 'delivered', color: '--c2', get: (i) => P()[i]!.delivered_bps ?? 0, fill: true },
      { label: 'ingest', color: '--c1', get: (i) => P()[i]!.ingest_bps ?? 0 },
      { label: 'between nodes', color: '--c3', get: (i) => P()[i]!.internal_bps ?? 0 },
      { label: 'TURN', color: '--c6', get: (i) => P()[i]!.turn_bps ?? 0 }], fmt: fmtBps });
    g.add({ title: 'CPU (% of each node)', series: [{ label: 'busiest', color: '--c6', get: (i) => P()[i]!.cpu_max }, { label: 'average', color: '--c3', get: (i) => P()[i]!.cpu_avg }], fmt: (v) => v.toFixed(0) + '%' });
    g.add({ title: 'Recording capacity (units)', series: [{ label: 'used', color: '--c6', get: (i) => P()[i]!.rec_used }, { label: 'capacity', color: '--c3', get: (i) => P()[i]!.rec_cap }], fmt: (v) => fmtNum(v), int: true });
    g.add({ title: 'Nodes', series: [{ label: 'media', color: '--c1', get: (i) => P()[i]!.media }, { label: 'edges', color: '--c2', get: (i) => P()[i]!.edges }, { label: 'down', color: '--c6', get: (i) => P()[i]!.down }], fmt: (v) => fmtNum(v), int: true });
    void clusterHistory().then((h) => { this.points = h.points; this.update(); }).catch(() => undefined);
    this.stops.push(every(STATE_MS, async () => {
      await refreshCluster();
      const pt = cluster.state?.point;
      const last = this.points[this.points.length - 1];
      if (pt && (!last || pt.t > last.t)) { this.points.push(pt); if (this.points.length > 720) this.points.shift(); }
      this.update();
    }));
  }

  private mountRoom(name: string): void {
    this.room = name;
    this.root.innerHTML = `<div class="view-head"><h2 class="crumbs"><a href="#/cluster">Cluster</a> <span class="muted">›</span> ${esc(name)}</h2></div><div class="body"></div>`;
    this.stops.push(every(ROOM_MS, async () => {
      try { this.parts = (await clusterRoom(name)).nodes; this.roomErr = ''; } catch (e) { this.roomErr = (e as Error).message; }
      this.update();
    }));
  }

  private click(e: Event): void {
    const b = (e.target as HTMLElement).closest<HTMLElement>('[data-node]');
    if (b) { e.preventDefault(); openNode(b.dataset.node!, b.dataset.hash); return; }
    void handleAction(e);
  }

  /** Past sessions from the cluster session store: works after the node that saw them is gone. */
  private async lookup(id: string): Promise<void> {
    const out = this.root.querySelector<HTMLElement>('.uresult')!;
    if (!id) { render(out, ''); return; }
    try {
      const r = await userSessions(id);
      const rows = r.items.map((s) => `<tr><td>${esc(s.room)}</td><td>${esc(s.role)}</td><td>${new Date(s.started_at).toLocaleString()}</td><td>${fmtDur(s.duration_ms)}</td>
        <td>${qualityBadge(s.quality)}</td><td>${(100 * (s.share.poor + s.share.lost)).toFixed(0)}%</td><td>${fmtNum(s.max_rtt_ms)} ms</td><td>${s.max_loss_percent.toFixed(1)}%</td>
        <td>${esc(s.events.slice(0, 3).map((e) => `${e.kind}: ${e.detail}`).join(' · '))}</td></tr>`);
      render(out, table(['Room', 'Role', 'Started', 'Duration', 'Last quality', 'Poor/lost', 'Max RTT', 'Max loss', 'Last events'], rows, `No saved sessions for "${esc(id)}" in the last 24 h.`));
    } catch (e) { render(out, `<p class="error" role="alert">${esc((e as Error).message)}</p>`); }
  }

  update(): void {
    if (this.room) return this.updateRoom();
    const st = cluster.state;
    if (!st) { render(this.root.querySelector('.cards')!, '<p class="muted">Loading…</p>'); return; }
    const nodes = st.nodes ?? [];
    const up = nodes.filter((n) => !n.down);
    const p = st.point;
    const card = (label: string, value: string, sub = '', cls = '') => `<div class="card ${cls}"><div class="label">${label}</div><div class="value">${esc(value)}</div><div class="sub">${esc(sub)}</div></div>`;
    const down = nodes.length - up.length;
    render(this.root.querySelector('.cards')!,
      card('Nodes', `${up.length} up`, `${p?.media ?? 0} media · ${p?.edges ?? 0} edge${p?.edges === 1 ? '' : 's'}${down ? ` · ${down} down` : ''}`, down ? 'bad' : '') +
      card('Presenters', fmtNum(p?.presenters ?? 0), 'on media nodes') +
      card('Viewers', fmtNum(p?.viewers ?? 0), 'on all nodes') +
      card('Delivered', fmtBps(p?.delivered_bps ?? 0), `to viewers · ingest ${fmtBps(p?.ingest_bps ?? 0)} · between nodes ${fmtBps(p?.internal_bps ?? 0)} · TURN ${fmtBps(p?.turn_bps ?? 0)}`) +
      card('Busiest CPU', `${(p?.cpu_max ?? 0).toFixed(0)}%`, `average ${(p?.cpu_avg ?? 0).toFixed(0)}%`, (p?.cpu_max ?? 0) > 80 ? 'warn' : '') +
      card('Recording', `${fmtNum(p?.recordings ?? 0)} active`, `capacity ${fmtNum(p?.rec_used ?? 0)} of ${fmtNum(p?.rec_cap ?? 0)} units`,
        p && p.rec_cap && p.rec_used * 10 >= p.rec_cap * 9 ? 'warn' : ''));
    const alerts = st.alerts ?? [];
    render(this.root.querySelector('.alerts')!, (st.error ? `<p class="error">${esc(st.error)}</p>` : '') + (alerts.length
      ? alerts.map((a) => `<div class="alert ${a.severity}">${badge(a.kind, a.severity === 'critical' ? 'bad' : 'warn')} ${esc(a.message)} <span class="muted">since ${new Date(a.since).toLocaleTimeString()}</span></div>`).join('')
      : '<p class="ok-line">No active alerts.</p>'));
    render(this.root.querySelector('.nodes')!, nodesTable(nodes, st.time));
    this.charts?.update(this.points.map((x) => x.t / 1000), this.points.length);
    const capEl = this.root.querySelector<HTMLElement>('.capacity');
    if (capEl) render(capEl, Object.entries(st.capacity ?? {}).map(([role, c]) =>
      `<div class="card ${c.n1 ? '' : 'warn'}"><div class="label">${esc(role)} nodes: ${c.n1 ? 'can lose one' : 'cannot lose one'}</div><div class="value">${(100 * c.avg_load).toFixed(0)}% avg</div><div class="sub">${c.nodes} up · busiest ${esc(c.busiest ?? '–')} ${(100 * c.max_load).toFixed(0)}% · ${esc(c.note)}</div></div>`).join('') || '<p class="muted">No node summaries yet.</p>');
    render(this.root.querySelector('.balance')!,
      balance('Presenters per media node', nodes.filter((n) => n.role !== 'edge' && !n.down).map((n) => [n.id, n.summary?.presenters ?? 0]), fmtNum) +
      balance('Viewers per edge', nodes.filter((n) => n.role === 'edge' && !n.down).map((n) => [n.id, n.summary?.viewers ?? 0]), fmtNum) +
      balance('CPU (% of each node)', up.map((n) => [n.id, n.summary?.cpu ?? 0]), (v) => `${v.toFixed(0)}%`, 100));
  }

  private updateRoom(): void {
    const body = this.root.querySelector<HTMLElement>('.body')!;
    if (this.roomErr) return render(body, `<p class="error">${esc(this.roomErr)}</p>`);
    if (!this.parts) return render(body, '<p class="muted">Loading…</p>');
    const room = this.room!;
    const failed = this.parts.filter((x) => x.error);
    const { people, relays } = mergeRoom(this.parts);
    const where = this.parts.filter((x) => x.room).map((x) => {
      const n = mergeRoom([x]).people.length;
      const extra = relays[x.node] ? ` · relayed to ${relays[x.node]} edge${relays[x.node]! > 1 ? 's' : ''}` : '';
      return `<a href="#" data-node="${esc(x.node)}" data-hash="#/rooms/${encodeURIComponent(room)}">${esc(x.node)}</a> ${badge(x.role)} ${n} ${x.role === 'edge' ? 'viewers' : 'here'}${extra}`;
    }).join(' &nbsp;·&nbsp; ');
    const rows = people.map(({ p, node, role }) => `<tr>
      <td><a href="#" data-node="${esc(node)}" data-hash="#/rooms/${encodeURIComponent(room)}/${encodeURIComponent(p.sid)}">${esc(p.identity)}</a></td>
      <td>${badge(role, role === 'presenter' ? 'ok' : 'muted')}</td><td>${esc(node)}</td><td>${qualityBadge(p.connection_quality)}</td>
      <td>${fmtNum(p.rtt_ms)} ms</td><td>${esc(p.ice_candidate_type || '–')}</td><td>${media(p)}</td>
      <td class="actions">${role === 'presenter' ? `<button data-act="start-recording" data-room="${esc(room)}" data-identity="${esc(p.identity)}">Record</button>` : ''}
        <button class="danger" data-act="remove-participant" data-room="${esc(room)}" data-identity="${esc(p.identity)}">Remove</button></td></tr>`);
    render(body, (where ? `<p>${where}</p>` : `<p class="muted">Room "${esc(room)}" is not on any node now. <a href="#/cluster">All rooms</a></p>`) +
      (failed.length ? `<p class="error">Not answering: ${esc(failed.map((x) => `${x.node} (${x.error})`).join('; '))}</p>` : '') +
      (people.length ? table(['Participant', 'Role', 'Node', 'Quality', 'RTT', 'ICE', 'Media', ''], rows) : ''));
  }

  destroy(): void {
    this.stops.forEach((s) => s());
    this.lists.forEach((l) => l.destroy());
    cluster.listeners.delete(this.onState);
    this.charts?.destroy();
    this.root.removeEventListener('click', this.onClick);
    this.root.innerHTML = '';
  }
}

/** The node tables: media nodes and edges apart, each with the columns of its job (ADR-0012).
 *  Status carries late/draining/down; Version shows only when nodes differ. */
function nodesTable(nodes: NodeView[], now: number): string {
  const versions = new Set(nodes.map((n) => n.summary?.version).filter(Boolean));
  const mixed = versions.size > 1;
  const common = (n: NodeView) => {
    const s = n.summary;
    const h = nodeHealth(n, now);
    const name = n.down || !n.reachable
      ? `${esc(n.id)}${n.reachable ? '' : ' <span class="muted" title="no private_url: its dashboard cannot be reached from here">(no URL)</span>'}`
      : `<a href="#" data-node="${esc(n.id)}">${esc(n.id)}</a>`;
    const status = `${badge(h.label, h.kind)}${n.down ? ` <span class="muted">at ${new Date(n.seen).toLocaleTimeString()}</span>` : ''}`;
    return {
      head: `<td>${name}${n.self ? ` ${badge('this node')}` : ''}</td><td>${status}</td>
        <td>${s ? `${s.cpu.toFixed(0)}% <span class="muted">of ${s.cores}</span>` : ''}</td><td>${s ? fmtBytes(s.rss) : ''}</td>`,
      tail: `<td>${s ? fmtBps(s.bps_in) : ''}</td><td>${s ? fmtBps(s.bps_out) : ''}</td><td>${s ? fmtDur(now - s.started_at) : ''}</td>${mixed ? `<td>${s ? esc(s.version) : ''}</td>` : ''}`,
    };
  };
  const tailCols = ['In', 'Out', 'Uptime', ...(mixed ? ['Version'] : [])];
  const viaTURN = (n: number): string => (n ? ` <span class="muted">· ${fmtNum(n)} via TURN</span>` : '');
  const media = nodes.filter((n) => n.role !== 'edge');
  const edges = nodes.filter((n) => n.role === 'edge');
  const mediaRows = media.map((n) => {
    const s = n.summary, c = s?.conns, x = common(n);
    const rec = s && s.rec_cap ? `${fmtNum(s.recordings)} <span class="muted">· ${fmtNum(s.rec_used)}/${fmtNum(s.rec_cap)} units (${Math.round((100 * s.rec_used) / s.rec_cap)}%)</span>` : '';
    return `<tr>${x.head}<td>${c ? `${fmtNum(c.presenters_in)}${viaTURN(c.presenters_turn)}` : ''}</td><td>${c?.pulls_out ? fmtNum(c.pulls_out) : ''}</td><td>${rec}</td>${x.tail}</tr>`;
  });
  const edgeRows = edges.map((n) => {
    const s = n.summary, c = s?.conns, x = common(n);
    const turn = c && (c.turn || c.turn_bps_in) ? `${fmtNum(c.turn)} <span class="muted">· ${fmtBps(Math.max(c.turn_bps_in, c.turn_bps_out))} relayed</span>` : '';
    return `<tr>${x.head}<td>${c ? `${fmtNum(c.viewers_out)}${viaTURN(c.viewers_turn)}` : ''}</td>
      <td>${c?.pulled || c?.pulls_in ? `${fmtNum(c.pulled)} <span class="muted">· ${fmtNum(c.pulls_in)} pulls</span>` : ''}</td>
      <td>${c?.proxied ? fmtNum(c.proxied) : ''}</td><td>${turn}</td>${x.tail}</tr>`;
  });
  if (!nodes.length) return table(['Node'], [], 'No node is registered.');
  return (media.length ? `<h4>Media nodes</h4>` + table(['Node', 'Status', 'CPU', 'Memory', 'Presenters', 'Edge pulls served', 'Recording (active · units)', ...tailCols], mediaRows, '') : '') +
    (edges.length ? `<h4>Edges</h4>` + table(['Node', 'Status', 'CPU', 'Memory', 'Viewers', 'Pulled presenters', 'Proxied presenters', 'TURN (allocations · relayed)', ...tailCols], edgeRows, '') : '');
}

export const isCluster = (): boolean => !!session.cluster;
