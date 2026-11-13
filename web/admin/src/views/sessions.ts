import { session, sessionDetail, sessions } from '../api';
import { store } from '../store';
import type { Hops, QualityShare, SessionBrief, SessionDetail } from '../types';
import { badge, esc, fmtBps, fmtDur, fmtNum, newestFirst, qualityBadge, safeDecode, table } from '../util';
import { listParams, PagedList, Poller, REFRESH_MS, type Column } from './paged';
import { render, selectBox, type View } from './view';

const STATE_BADGE: Record<string, 'ok' | 'muted'> = { active: 'ok' };
/** Event kinds with a known colour; any other kind is shown generically (kind and detail). */
const EVENT_BADGE: Record<string, 'ok' | 'warn' | 'bad' | 'muted'> = { joined: 'ok', left: 'muted', quality: 'warn', ice: 'muted' };
const SHARES: (keyof QualityShare)[] = ['excellent', 'good', 'poor', 'lost', 'unknown'];

const when = (iso?: string): string => (iso ? new Date(iso).toLocaleString() : '');
const bad = (s: QualityShare): number => s.poor + s.lost;
const pct = (f: number): string => `${(100 * f).toFixed(f > 0 && f < 0.01 ? 1 : 0)}%`;

const columns: Column<SessionBrief>[] = [
  { label: 'Participant', sort: 'identity', get: (s) => s.identity, cell: (s) => `<a href="#/sessions/${encodeURIComponent(s.sid)}">${esc(s.identity)}</a>` },
  { label: 'Room', get: (s) => s.room },
  { label: 'Role', get: (s) => s.role },
  { label: 'State', get: (s) => s.state, cell: (s) => badge(s.state, STATE_BADGE[s.state] ?? 'muted') },
  { label: 'Quality', get: (s) => s.quality, cell: (s) => qualityBadge(s.quality) },
  { label: 'Started', sort: 'started', desc: true, get: (s) => s.started_at, cell: (s) => when(s.started_at) },
  { label: 'Ended', sort: 'ended', desc: true, get: (s) => s.ended_at, cell: (s) => when(s.ended_at) },
  { label: 'Duration', sort: 'duration', desc: true, get: (s) => s.duration_ms, cell: (s) => fmtDur(s.duration_ms) },
  { label: 'Poor or lost', sort: 'bad', desc: true, get: (s) => Number(bad(s.share).toFixed(4)), cell: (s) => pct(bad(s.share)) },
  { label: 'Max RTT', sort: 'rtt', desc: true, get: (s) => s.max_rtt_ms, cell: (s) => `${fmtNum(s.max_rtt_ms)} ms` },
  { label: 'Max loss', sort: 'loss', desc: true, get: (s) => s.max_loss_percent, cell: (s) => `${s.max_loss_percent.toFixed(1)}%` },
  { label: 'ICE', get: (s) => s.ice_type, cell: (s) => esc(s.ice_type || '–') },
  { label: 'SDK', get: (s) => s.client_sdk },
  { label: 'Events', get: (s) => s.events },
];

/** The share of time per quality as one bar with a legend. */
export function shareBar(s: QualityShare): string {
  const seg = SHARES.filter((k) => s[k] > 0);
  return `<div class="sharebar" role="img" aria-label="Time per connection quality">${seg.map((k) => `<i class="${k}" style="width:${100 * s[k]}%" title="${k} ${pct(s[k])}"></i>`).join('')}</div>
    <p class="muted">${SHARES.map((k) => `${k} ${pct(s[k])}`).join(' · ')}</p>`;
}

function hopTables(h: Hops): string {
  const up = h.uplink.map((t) => `<tr><td>${esc(t.track_sid)}</td><td>${esc(t.kind)}</td><td>${t.loss_percent.toFixed(1)}%</td><td>${t.jitter_ms.toFixed(1)} ms</td><td>${fmtBps(t.bitrate_bps)}</td></tr>`);
  const src = h.sources.map((t) => `<tr><td>${esc(t.presenter)}</td><td>${esc(t.track_sid)}</td><td>${esc(t.kind)}</td><td>${t.uplink_loss_percent.toFixed(1)}%</td><td>${t.on_this_node ? '' : badge('other node')}</td></tr>`);
  const down = h.downlink.map((t) => `<tr><td>${esc(t.presenter)}</td><td>${esc(t.track_sid)}</td><td>${esc(t.kind)}</td><td>${t.loss_percent.toFixed(1)}%</td><td>${t.jitter_ms.toFixed(1)} ms</td><td>${t.rtt_ms.toFixed(0)} ms</td><td>${fmtBps(t.bitrate_bps)}</td><td>${t.paused ? badge('paused', 'warn') : ''}</td></tr>`);
  return `<div class="hops"><div><h4>Its uplink (what it publishes)</h4>${table(['Track', 'Kind', 'Loss', 'Jitter', 'Bitrate'], up, 'Publishes nothing.', 500)}</div>
    <div><h4>Presenters' uplinks (what it watches)</h4>${table(['Presenter', 'Track', 'Kind', 'Uplink loss', ''], src, 'Watches nothing.', 500)}</div>
    <div><h4>Its downlink (worst 50)</h4>${table(['Presenter', 'Track', 'Kind', 'Loss', 'Jitter', 'RTT', 'Bitrate', ''], down, 'Receives nothing.', 500)}</div></div>`;
}

/** #/sessions: the session list. #/sessions/<sid>: one session's summary, quality, events and (while active) hops. */
export class Sessions implements View {
  private root!: HTMLElement;
  private list?: PagedList<SessionBrief>;
  private poller?: Poller<SessionDetail>;
  private body!: HTMLElement;

  mount(root: HTMLElement, args: string[]): void {
    this.root = root;
    const sid = safeDecode(args[0] ?? '');
    if (sid) return this.mountOne(sid);
    // On an edge presenters are only relayed copies: their sessions are on the media nodes.
    const onEdge = session.cluster?.role === 'edge' && !store.node;
    root.innerHTML = `<div class="view-head"><h2>Sessions</h2></div>${onEdge ? '<p class="muted">This edge holds viewers\' sessions; presenters\' sessions are on the media nodes (pick one with the node picker).</p>' : ''}<div class="body"></div>`;
    const head = root.querySelector<HTMLElement>('.view-head')!;
    const f = document.createElement('span');
    f.className = 'filters';
    head.appendChild(f);
    const re = () => this.list?.reload();
    const state = selectBox(f, 'State', [['', 'All states'], ['active', 'Active'], ['ended', 'Ended']], re);
    const quality = selectBox(f, 'Quality', [['', 'Any quality'], ['poor', 'Had poor or lost time']], re);
    const role = selectBox(f, 'Role', [['', 'All roles'], ['presenter', 'Presenters'], ['viewer', 'Viewers']], re);
    this.list = new PagedList<SessionBrief>({
      head, body: root.querySelector('.body')!, filterLabel: 'Search identity, name, room or session', sort: 'started', desc: true, scope: () => store.node,
      empty: 'No sessions on this node yet.', name: 'sessions', columns,
      fetch: (st) => sessions({ ...listParams(st), state: state.value, quality: quality.value, role: role.value, node: store.node }),
    });
  }

  private mountOne(sid: string): void {
    this.root.innerHTML = `<div class="view-head"><h2 class="crumbs"><a href="#/sessions">Sessions</a> <span class="muted">›</span> <span class="who">${esc(sid)}</span></h2></div><div class="body"></div>`;
    this.body = this.root.querySelector('.body')!;
    this.poller = new Poller<SessionDetail>({
      fetch: () => sessionDetail({ sid, node: store.node }), ms: REFRESH_MS, scope: () => store.node,
      onData: (d) => this.show(d),
      onError: (e) => render(this.body, `<p class="muted">${(e as { status?: number }).status === 404 ? 'This session is not known on this node (it may be older than the last 5,000 kept).' : esc(e.message)} <a href="#/sessions">All sessions</a></p>`),
    });
    this.poller.start();
  }

  private show(d: SessionDetail): void {
    const who = this.root.querySelector('.who');
    if (who) who.textContent = d.identity;
    const card = (label: string, value: string, sub = '', cls = '') => `<div class="card ${cls}"><div class="label">${label}</div><div class="value">${esc(value)}</div><div class="sub">${esc(sub)}</div></div>`;
    const active = d.state === 'active';
    const b = bad(d.share);
    const events = newestFirst(d.events ?? []);
    render(this.body, `<div class="cards">
      ${card('State', d.state, `${d.role} in ${d.room}`)}
      ${card('Duration', fmtDur(d.duration_ms), `started ${when(d.started_at)}${d.ended_at ? ` · ended ${when(d.ended_at)}` : ''}`)}
      ${card('Quality now', d.quality, active ? 'current' : 'at the end', d.quality === 'lost' ? 'bad' : d.quality === 'poor' ? 'warn' : '')}
      ${card('Poor or lost', pct(b), 'of its time', b > 0.2 ? 'bad' : b > 0.05 ? 'warn' : '')}
      ${card('Max RTT', `${fmtNum(d.max_rtt_ms)} ms`)}${card('Max loss', `${d.max_loss_percent.toFixed(1)}%`)}
      ${card('ICE', d.ice_type || '–', d.client_sdk ?? '')}</div>
      <p>${active ? `<a href="#/rooms/${encodeURIComponent(d.room)}/${encodeURIComponent(d.sid)}">Live participant page</a>` : ''}${d.name ? ` ${esc(d.name)}` : ''}</p>
      <h3>Time per connection quality</h3>${shareBar(d.share)}
      ${d.hops ? `<h3>Why was this call bad?</h3>${hopTables(d.hops)}` : ''}
      <h3>Events</h3>${events.length ? `<ul class="events">${events.map((e) => `<li><span class="muted">${new Date(e.t).toLocaleTimeString()}</span><span>${badge(e.kind, EVENT_BADGE[e.kind] ?? 'muted')}</span><span>${esc(e.detail)}</span></li>`).join('')}</ul>` : '<p class="muted">No events.</p>'}`);
  }

  update(): void { this.list?.check(); this.poller?.check(); }
  destroy(): void { this.list?.destroy(); this.poller?.stop(); this.root.innerHTML = ''; }
}
