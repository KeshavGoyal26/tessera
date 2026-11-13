import { action, participant, participants, rooms } from '../api';
import { store } from '../store';
import type { Participant, ParticipantBrief, ParticipantDetail, RoomBrief } from '../types';
import { badge, esc, fmtBps, fmtNum, pushCap, qualityBadge, role, table, safeDecode } from '../util';
import { listParams, PagedList, Poller, type Column } from './paged';
import { ChartGroup, render, type View } from './view';

const ACTION_DONE: Record<string, string> = {
  'end-room': 'Room ended', 'remove-participant': 'Participant removed', 'mute-track': 'Track updated', 'start-recording': 'Recording requested',
};

interface PartPoint { t: number; pub: number; sub: number; loss: number; rtt: number; bwe: number }

export class Rooms implements View {
  private root!: HTMLElement;
  private crumbs!: HTMLElement;
  private body!: HTMLElement;
  private args: string[] = [];
  private list?: PagedList<RoomBrief | ParticipantBrief>;
  private poller?: Poller<ParticipantDetail>;
  private charts?: ChartGroup;
  private hist: PartPoint[] = [];
  private onClick = (e: Event) => void handleAction(e);

  mount(root: HTMLElement, args: string[]): void {
    this.root = root;
    this.args = args.map(safeDecode);
    const [roomName, psid] = this.args;
    root.innerHTML = '<div class="view-head"><h2 class="crumbs"></h2></div><div class="body"></div>';
    this.crumbs = root.querySelector('.crumbs')!;
    this.body = root.querySelector('.body')!;
    root.addEventListener('click', this.onClick);
    const crumbs = [`<a href="#/rooms">Rooms</a>`];
    if (roomName) crumbs.push(`<a href="#/rooms/${encodeURIComponent(roomName)}">${esc(roomName)}</a>`);
    if (psid) crumbs.push('<span class="who"></span>');
    this.crumbs.innerHTML = crumbs.join(' <span class="muted">›</span> ');
    const head = root.querySelector<HTMLElement>('.view-head')!;
    if (!roomName) {
      this.list = new PagedList<RoomBrief>({
        head, body: this.body, filterLabel: 'Filter rooms', sort: 'name', scope: () => store.node, empty: 'No rooms on this node.', name: 'rooms',
        columns: roomColumns, fetch: (st) => rooms({ ...listParams(st), node: store.node }),
      }) as PagedList<RoomBrief | ParticipantBrief>;
    } else if (!psid) {
      this.list = new PagedList<ParticipantBrief>({
        head, body: this.body, filterLabel: 'Filter participants', sort: 'identity', scope: () => store.node, empty: 'The room is empty.', name: `participants-${roomName}`,
        columns: participantColumns, fetch: (st) => participants({ ...listParams(st), room: roomName, node: store.node }),
      }) as PagedList<RoomBrief | ParticipantBrief>;
    } else {
      this.body.innerHTML = '<div class="info"></div><div class="grid charts"></div><div class="tables"></div>';
      const g = (this.charts = new ChartGroup(this.body.querySelector('.charts')!));
      const H = () => this.hist;
      g.add({ title: 'Bitrate', series: [{ label: 'published', color: '--c1', get: (i) => H()[i]!.pub }, { label: 'subscribed', color: '--c2', get: (i) => H()[i]!.sub }], fmt: fmtBps });
      g.add({ title: 'Worst track loss %', series: [{ label: 'loss', color: '--c6', get: (i) => H()[i]!.loss, fill: true }], fmt: (v) => v.toFixed(1) + '%' });
      g.add({ title: 'RTT (ms)', series: [{ label: 'rtt', color: '--c3', get: (i) => H()[i]!.rtt }], fmt: (v) => v.toFixed(0), int: true });
      g.add({ title: 'Bandwidth estimate', series: [{ label: 'bwe', color: '--c4', get: (i) => H()[i]!.bwe }], fmt: fmtBps });
      this.poller = new Poller<ParticipantDetail>({
        fetch: () => participant({ room: roomName, sid: psid, node: store.node }),
        ms: 1000, scope: () => store.node, onScope: () => { this.hist = []; },
        onData: (d) => this.showParticipant(d),
        onError: (e) => this.showGone(e),
      });
      this.poller.start();
    }
  }

  update(): void { this.list?.check(); this.poller?.check(); }

  private showGone(e: Error): void {
    const [roomName] = this.args;
    const info = this.body.querySelector<HTMLElement>('.info');
    if (info) render(info, `<p class="muted">${esc((e as { status?: number }).status === 404 ? `This participant has left "${roomName}" (or the room ended).` : e.message)} <a href="#/rooms/${encodeURIComponent(roomName!)}">Back to the room</a></p>`);
  }

  private showParticipant(d: ParticipantDetail): void {
    const p = d.participant, r = { name: d.room };
    const who = this.crumbs.querySelector('.who');
    if (who) who.textContent = p.identity;
    const ro = role(p);
    const info = this.body.querySelector<HTMLElement>('.info')!;
    render(info, `<p>${badge(ro, ro === 'presenter' ? 'ok' : 'muted')} ${qualityBadge(p.connection_quality)} RTT ${fmtNum(p.rtt_ms)} ms · ICE ${esc(p.ice_candidate_type || '–')}
      · joined ${new Date(p.joined_at).toLocaleTimeString()}${p.client_sdk ? ` · ${esc(p.client_sdk)}` : ''} · <a href="#/sessions/${encodeURIComponent(p.sid)}">session</a></p>`);
    const pubs = p.published ?? [], subs = p.subscribed ?? [];
    pushCap(this.hist, {
      t: Date.now(), pub: pubs.reduce((a, t) => a + t.bitrate_bps, 0), sub: subs.reduce((a, t) => a + t.bitrate_bps, 0),
      loss: Math.max(0, ...pubs.map((t) => t.loss_percent), ...subs.map((t) => t.loss_percent)), rtt: p.rtt_ms, bwe: p.bandwidth_estimate_bps,
    });
    this.charts?.update(this.hist.map((x) => x.t / 1000), this.hist.length);
    const pub = pubs.map((t) => `<tr><td><a href="#/quality/${encodeURIComponent(t.sid)}">${esc(t.sid)}</a></td><td>${esc(t.kind)}</td><td>${esc(t.source)}</td><td>${esc(t.codec)}</td>
        <td>${t.width ? `${t.width}×${t.height}` : ''}</td><td>${fmtBps(t.bitrate_bps)}</td><td>${t.loss_percent.toFixed(1)}%</td><td>${t.jitter_ms.toFixed(1)} ms</td><td>${t.kind === 'audio' && t.audio_level !== undefined ? t.audio_level.toFixed(2) : ''}</td><td>${t.subscribers}</td>
        <td>${trackState(t)}</td>
        <td class="actions"><button data-act="mute-track" data-room="${esc(r.name)}" data-identity="${esc(p.identity)}" data-track="${esc(t.sid)}" data-muted="${!t.muted}">${t.muted ? 'Unmute' : 'Mute'}</button>
        <button data-act="start-recording" data-room="${esc(r.name)}" data-track="${esc(t.sid)}">Record</button></td></tr>`);
    const sub = subs.map((t) => `<tr><td><a href="#/rooms/${encodeURIComponent(r.name)}/${encodeURIComponent(t.publisher_sid)}">${esc(d.names[t.publisher_sid] ?? t.publisher_sid)}</a></td><td><a href="#/quality/${encodeURIComponent(t.track_sid)}">${esc(t.track_sid)}</a></td><td>${esc(t.kind)}</td>
      <td>${fmtBps(t.bitrate_bps)}</td><td>${t.loss_percent.toFixed(1)}%</td><td>${t.jitter_ms.toFixed(1)} ms</td><td>${t.rtt_ms.toFixed(0)} ms</td><td>${t.restarts}</td>
      <td>${t.paused ? badge('paused', 'warn') : ''}${t.muted ? badge('muted') : ''}</td></tr>`);
    const total = d.subscribed_total ?? p.subscribed_total ?? subs.length;
    const cut = total > subs.length ? `<p class="muted more">Showing ${fmtNum(subs.length)} of ${fmtNum(total)}.</p>` : '';
    const pubTable = `<h3>Published</h3>${table(['Track', 'Kind', 'Source', 'Codec', 'Video', 'Bitrate', 'Loss', 'Jitter', 'Audio level', 'Subs', 'State', ''], pub, 'Publishes nothing.', 500)}`;
    const subTable = `<h3>Watching</h3>${table(['Presenter', 'Track', 'Kind', 'Bitrate', 'Loss', 'Jitter', 'RTT', 'Restarts', 'Flags'], sub, 'Watches nothing.', 500)}${cut}`;
    // A presenter only publishes and a viewer only watches: show the other table only when it has rows.
    render(this.body.querySelector<HTMLElement>('.tables')!, ro === 'presenter' ? pubTable + (sub.length ? subTable : '') : (pub.length ? pubTable : '') + subTable);
  }

  destroy(): void {
    this.list?.destroy();
    this.poller?.stop();
    this.charts?.destroy();
    this.root.removeEventListener('click', this.onClick);
    this.root.innerHTML = '';
  }
}

const roomColumns: Column<RoomBrief>[] = [
  { label: 'Room', sort: 'name', get: (r) => r.name, cell: (r) => `<a href="#/rooms/${encodeURIComponent(r.name)}">${esc(r.name)}</a>` },
  { label: 'Presenters', sort: 'presenters', desc: true, get: (r) => r.presenters },
  { label: 'Viewers', sort: 'viewers', desc: true, get: (r) => r.viewers, cell: (r) => `${fmtNum(r.viewers)}${r.relays ? ` ${badge(`${r.relays} relay`)}` : ''}` },
  { label: 'Recording', get: (r) => r.recording, cell: (r) => (r.recording ? badge('● recording', 'bad') : '') },
  { label: 'Speaking', get: (r) => (r.speakers ?? []).slice(0, 3).join(', '), cell: (r) => (r.speakers ?? []).slice(0, 3).map(esc).join(', ') },
  { label: 'Created', sort: 'created', desc: true, get: (r) => r.created_at, cell: (r) => new Date(r.created_at).toLocaleTimeString() },
  { label: '', cls: 'actions', cell: (r) => `<button class="danger" data-act="end-room" data-room="${esc(r.name)}">End room</button>` },
];

const participantColumns: Column<ParticipantBrief>[] = [
  { label: 'Participant', sort: 'identity', get: (p) => p.identity, cell: (p) => `<a href="#/rooms/${encodeURIComponent(p.room)}/${encodeURIComponent(p.sid)}">${esc(p.identity)}</a> ${p.hidden ? badge('hidden') : ''}${p.kind === 'loadtest' ? badge('loadtest') : ''}` },
  { label: 'Role', get: (p) => p.role, cell: (p) => badge(p.role, p.role === 'presenter' ? 'ok' : 'muted') },
  { label: 'State', get: (p) => p.state },
  { label: 'Quality', sort: 'quality', get: (p) => p.quality, cell: (p) => qualityBadge(p.quality) },
  { label: 'RTT', sort: 'rtt', desc: true, get: (p) => p.rtt_ms, cell: (p) => `${fmtNum(p.rtt_ms)} ms` },
  { label: 'ICE', get: (p) => p.ice_type, cell: (p) => esc(p.ice_type || '–') },
  { label: 'Media', get: (p) => (p.published ? `${p.published} published` : `${p.subscribed} tracks`), cell: mediaBrief },
  { label: 'Bitrate', sort: 'bitrate', desc: true, get: (p) => p.bitrate_bps, cell: (p) => fmtBps(p.bitrate_bps) },
  { label: 'BWE', get: (p) => p.bwe_bps, cell: (p) => fmtBps(p.bwe_bps) },
  { label: 'SDK', get: (p) => p.client_sdk },
  { label: '', cls: 'actions', cell: (p) => `${p.role === 'presenter' ? `<button data-act="start-recording" data-room="${esc(p.room)}" data-identity="${esc(p.identity)}">Record</button>` : ''}
    <button class="danger" data-act="remove-participant" data-room="${esc(p.room)}" data-identity="${esc(p.identity)}">Remove</button>` },
];

/** mediaBrief is media() for a list row. */
export function mediaBrief(p: ParticipantBrief): string {
  if (p.published) return `${p.published} published${p.no_video ? ` ${badge('no video', 'warn')}` : ''}`;
  return `${p.subscribed} tracks${p.paused ? ` ${badge(`${p.paused} paused`, 'warn')}` : ''}`;
}

/** What a participant sends or receives: a presenter's tracks and subscribers, a viewer's tracks and pauses.
 *  A presenter relayed from another node (no connection here) is paused upstream while unwatched: not flagged. */
export function media(p: Participant): string {
  const pub = p.published ?? [], sub = p.subscribed ?? [];
  if (pub.length) {
    const subs = Math.max(0, ...pub.map((t) => t.subscribers));
    const idle = !!p.ice_candidate_type && pub.some((t) => t.kind === 'video' && !t.active && !t.muted);
    return `${pub.length} published · ${subs} subs${idle ? ` ${badge('no video', 'warn')}` : ''}`;
  }
  const paused = sub.filter((t) => t.paused).length;
  return `${sub.length} tracks${paused ? ` ${badge(`${paused} paused`, 'warn')}` : ''}`;
}

export function trackState(t: { active: boolean; muted: boolean }): string {
  if (t.muted) return badge('muted', 'warn');
  return t.active ? badge('receiving', 'ok') : badge('no media', 'warn');
}

export function toast(msg: string, bad = false): void {
  const el = document.createElement('div');
  el.className = 'toast' + (bad ? ' bad' : '');
  el.setAttribute('role', bad ? 'alert' : 'status');
  el.textContent = msg;
  document.body.appendChild(el);
  setTimeout(() => el.remove(), 4000);
}

/** Runs the action of a clicked button[data-act] (room, participant and track actions). */
export async function handleAction(e: Event): Promise<void> {
  const b = (e.target as HTMLElement).closest<HTMLButtonElement>('button[data-act]');
  if (!b) return;
  const d = b.dataset as Record<string, string>;
  const msg: Record<string, string> = {
    'end-room': `End room "${d.room}" and disconnect everyone?`,
    'remove-participant': `Remove "${d.identity}" from "${d.room}"?`,
  };
  if (msg[d.act!] && !confirm(msg[d.act!])) return;
  b.disabled = true;
  try {
    const body: Record<string, unknown> = { room: d.room, identity: d.identity, track_sid: d.track };
    if (d.act === 'mute-track') body.muted = d.muted === 'true';
    await action(d.act!, body);
    toast(ACTION_DONE[d.act!] ?? `${d.act} OK`);
  } catch (err) {
    toast(`${d.act} failed: ${(err as Error).message}`, true);
  } finally {
    b.disabled = false;
  }
}
