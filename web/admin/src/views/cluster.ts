import { clusterHistory, clusterRoom, clusterRooms, clusterState, session } from '../api';
import { store } from '../store';
import type { ClusterPoint, ClusterRoom, ClusterState, NodeRoom, NodeView } from '../types';
import { badge, esc, fmtBps, fmtBytes, fmtDur, fmtNum, matches, mergeRoom, nodeHealth, qualityBadge, table, safeDecode } from '../util';
import { handleAction, media } from './rooms';
import { ChartGroup, filterBox, render, type View } from './view';

const STATE_MS = 5000; // the server samples the registry every 5 s
const ROOMS_MS = 5000;
const ROOM_MS = 2000;

/** The latest cluster sample, shared with the header's node picker. */
export const cluster: { state?: ClusterState; listeners: Set<() => void> } = { listeners: new Set() };
export async function refreshCluster(): Promise<void> {
  try { cluster.state = await clusterState(); } catch { /* keep the last sample */ }
  cluster.listeners.forEach((f) => f());
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
  private rooms?: { rooms: ClusterRoom[]; errors: string[] | null; truncated: boolean };
  private roomErr = '';
  private filter?: HTMLInputElement;
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
      <div class="view-head"><h3>Rooms</h3></div><div class="rooms"></div>`;
    this.filter = filterBox(root.querySelector('.view-head')!, 'Filter rooms', () => this.update());
    const g = (this.charts = new ChartGroup(root.querySelector('.charts')!));
    const P = () => this.points;
    g.add({ title: 'People', series: [{ label: 'viewers', color: '--c1', get: (i) => P()[i]!.viewers, fill: true }, { label: 'presenters', color: '--c2', get: (i) => P()[i]!.presenters }], fmt: (v) => fmtNum(v), int: true });
    g.add({ title: 'Bandwidth (all nodes)', series: [{ label: 'out', color: '--c2', get: (i) => P()[i]!.bps_out }, { label: 'in', color: '--c1', get: (i) => P()[i]!.bps_in }], fmt: fmtBps });
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
    this.stops.push(every(ROOMS_MS, async () => {
      try { this.rooms = await clusterRooms(); this.roomErr = ''; } catch (e) { this.roomErr = (e as Error).message; }
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

  update(): void {
    if (this.room) return this.updateRoom();
    const st = cluster.state;
    const q = this.filter?.value ?? '';
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
      card('Egress', fmtBps(p?.bps_out ?? 0), `ingress ${fmtBps(p?.bps_in ?? 0)}`) +
      card('Busiest CPU', `${(p?.cpu_max ?? 0).toFixed(0)}%`, `average ${(p?.cpu_avg ?? 0).toFixed(0)}%`, (p?.cpu_max ?? 0) > 80 ? 'warn' : '') +
      card('Recording', `${fmtNum(p?.recordings ?? 0)} active`, `capacity ${fmtNum(p?.rec_used ?? 0)} of ${fmtNum(p?.rec_cap ?? 0)} units`,
        p && p.rec_cap && p.rec_used * 10 >= p.rec_cap * 9 ? 'warn' : ''));
    const alerts = st.alerts ?? [];
    render(this.root.querySelector('.alerts')!, (st.error ? `<p class="error">${esc(st.error)}</p>` : '') + (alerts.length
      ? alerts.map((a) => `<div class="alert ${a.severity}">${badge(a.kind, a.severity === 'critical' ? 'bad' : 'warn')} ${esc(a.message)} <span class="muted">since ${new Date(a.since).toLocaleTimeString()}</span></div>`).join('')
      : '<p class="ok-line">No active alerts.</p>'));
    render(this.root.querySelector('.nodes')!, nodesTable(nodes, st.time));
    this.charts?.update(this.points.map((x) => x.t / 1000), this.points.length);
    const r = this.rooms;
    const rows = (r?.rooms ?? []).filter((x) => matches(q, x.name, ...x.media, ...x.edges)).map((x) => `<tr>
      <td><a href="#/cluster/room/${encodeURIComponent(x.name)}">${esc(x.name)}</a></td><td>${fmtNum(x.presenters)}</td><td>${fmtNum(x.viewers)}</td>
      <td>${x.media.map(esc).join(', ') || '<span class="muted">none</span>'}</td><td>${x.edges.map(esc).join(', ')}</td><td>${x.recording ? badge('● recording', 'bad') : ''}</td></tr>`);
    const notes = (r?.truncated ? '<p class="muted more">A node has more rooms than it lists; filter or open a node to see them all.</p>' : '') +
      (r?.errors?.length ? `<p class="error">Some nodes did not answer: ${esc(r.errors.join('; '))}</p>` : '');
    render(this.root.querySelector('.rooms')!, this.roomErr ? `<p class="error">${esc(this.roomErr)}</p>`
      : !r ? '<p class="muted">Loading…</p>'
      : table(['Room', 'Presenters', 'Viewers', 'Media nodes', 'Edges', 'Recording'], rows, q ? 'No room matches the filter.' : 'No rooms in the cluster.') + notes);
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
    cluster.listeners.delete(this.onState);
    this.charts?.destroy();
    this.root.removeEventListener('click', this.onClick);
    this.root.innerHTML = '';
  }
}

function nodesTable(nodes: NodeView[], now: number): string {
  const rows = nodes.map((n) => {
    const s = n.summary;
    const h = nodeHealth(n, now);
    const name = n.down || !n.reachable
      ? `${esc(n.id)}${n.reachable ? '' : ' <span class="muted" title="no private_url: its dashboard cannot be reached from here">(no URL)</span>'}`
      : `<a href="#" data-node="${esc(n.id)}">${esc(n.id)}</a>`;
    const people = !s ? '' : n.role === 'edge' ? `${fmtNum(s.viewers)} viewers` : `${fmtNum(s.presenters)} presenters${s.viewers ? ` · ${fmtNum(s.viewers)} viewers` : ''}${s.relays ? ` · ${s.relays} relays` : ''}`;
    return `<tr><td>${name}${n.self ? ` ${badge('this node')}` : ''}</td><td>${badge(n.role)}</td><td>${badge(h.label, h.kind)}</td>
      <td>${s ? `${s.cpu.toFixed(0)}% <span class="muted">of ${s.cores}</span>` : ''}</td><td>${s ? fmtBytes(s.rss) : ''}</td><td>${people}</td>
      <td>${s && s.rec_cap ? `${fmtNum(s.recordings)} · ${fmtNum(s.rec_used)}/${fmtNum(s.rec_cap)}` : ''}</td>
      <td>${s ? fmtBps(s.bps_out) : ''}</td><td>${s ? fmtNum(s.rooms) : ''}</td>
      <td>${s ? esc(s.version) : ''}</td><td>${s ? fmtDur(now - s.started_at) : ''}</td>
      <td>${n.down ? `gone at ${new Date(n.seen).toLocaleTimeString()}` : `${Math.max(0, (now - n.seen) / 1000).toFixed(0)} s ago`}</td></tr>`;
  });
  return table(['Node', 'Role', 'Status', 'CPU', 'Memory', 'People', 'Recording (active · units)', 'Out', 'Rooms', 'Version', 'Uptime', 'Heartbeat'], rows, 'No node is registered.');
}

export const isCluster = (): boolean => !!session.cluster;
