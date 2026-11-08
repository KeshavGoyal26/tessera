import { action } from '../api';
import { store } from '../store';
import type { Participant, Room } from '../types';
import { badge, esc, fmtBps, fmtNum, matches, qualityBadge, role, roleCounts, table } from '../util';
import { filterBox, render, type View } from './view';

const ACTION_DONE: Record<string, string> = {
  'end-room': 'Room ended', 'remove-participant': 'Participant removed', 'mute-track': 'Track updated', 'start-recording': 'Recording requested',
};

export class Rooms implements View {
  private root!: HTMLElement;
  private crumbs!: HTMLElement;
  private body!: HTMLElement;
  private filter?: HTMLInputElement;
  private args: string[] = [];
  private onClick = (e: Event) => void this.click(e);

  mount(root: HTMLElement, args: string[]): void {
    this.root = root;
    this.args = args.map(decodeURIComponent);
    root.innerHTML = '<div class="view-head"><h2 class="crumbs"></h2></div><div class="body"></div>';
    this.crumbs = root.querySelector('.crumbs')!;
    this.body = root.querySelector('.body')!;
    // Lists (rooms, a room's participants) get a filter; a participant's page does not.
    if (args.length < 2) this.filter = filterBox(root.querySelector('.view-head')!, args.length ? 'Filter participants' : 'Filter rooms', () => this.update());
    root.addEventListener('click', this.onClick);
    this.update();
  }

  private async click(e: Event): Promise<void> {
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

  update(): void {
    const rooms = store.update?.snapshot.rooms ?? [];
    const [roomName, psid] = this.args;
    const crumbs = [`<a href="#/rooms">Rooms</a>`];
    if (roomName) crumbs.push(`<a href="#/rooms/${encodeURIComponent(roomName)}">${esc(roomName)}</a>`);
    const room = rooms.find((r) => r.name === roomName);
    const part = room?.participants?.find((p) => p.sid === psid);
    if (part) crumbs.push(esc(part.identity));
    let body: string;
    if (!store.update) body = '<p class="muted">Waiting for data…</p>';
    else if (!roomName) body = this.roomsTable(rooms);
    else if (!room) body = `<p class="muted">Room "${esc(roomName)}" not found (it may have ended). <a href="#/rooms">All rooms</a></p>`;
    else if (!psid) body = this.participantsTable(room);
    else if (!part) body = `<p class="muted">This participant has left "${esc(roomName)}". <a href="#/rooms/${encodeURIComponent(roomName)}">Back to the room</a></p>`;
    else body = this.participantDetail(room, part);
    render(this.crumbs, crumbs.join(' <span class="muted">›</span> '));
    render(this.body, body);
  }

  private roomsTable(rooms: Room[]): string {
    if (!rooms.length) return '<p class="muted">No rooms on this node.</p>';
    const q = this.filter?.value ?? '';
    const rows = rooms.filter((r) => matches(q, r.name)).map((r) => {
      const n = roleCounts([r]);
      const sp = r.active_speakers ?? [];
      const speakers = sp.slice(0, 3).map((s) => esc(s.identity)).join(', ') + (sp.length > 3 ? ` <span class="muted">+${sp.length - 3}</span>` : '');
      return `<tr><td><a href="#/rooms/${encodeURIComponent(r.name)}">${esc(r.name)}</a></td><td>${n.presenter}</td>
        <td>${n.viewer}${n.relay ? ` ${badge(`${n.relay} relay`)}` : ''}</td>
        <td>${r.active_recording ? badge('● recording', 'bad') : ''}</td><td>${speakers}</td><td>${new Date(r.created_at).toLocaleTimeString()}</td>
        <td class="actions"><button class="danger" data-act="end-room" data-room="${esc(r.name)}">End room</button></td></tr>`;
    });
    return table(['Room', 'Presenters', 'Viewers', 'Recording', 'Speaking', 'Created', ''], rows, 'No room matches the filter.');
  }

  private participantsTable(r: Room): string {
    const q = this.filter?.value ?? '';
    const rows = (r.participants ?? []).filter((p) => matches(q, p.identity, p.name, p.sid)).map((p) => {
      const ro = role(p);
      return `<tr><td><a href="#/rooms/${encodeURIComponent(r.name)}/${encodeURIComponent(p.sid)}">${esc(p.identity)}</a> ${p.hidden ? badge('hidden') : ''}${p.kind === 'loadtest' ? badge('loadtest') : ''}</td>
      <td>${badge(ro, ro === 'presenter' ? 'ok' : 'muted')}</td><td>${esc(p.state)}</td><td>${qualityBadge(p.connection_quality)}</td><td>${fmtNum(p.rtt_ms)} ms</td><td>${esc(p.ice_candidate_type || '–')}</td>
      <td>${media(p)}</td><td>${fmtBps(p.bandwidth_estimate_bps)}</td><td>${esc(p.client_sdk ?? '')}</td>
      <td class="actions">${ro === 'presenter' ? `<button data-act="start-recording" data-room="${esc(r.name)}" data-identity="${esc(p.identity)}">Record</button>` : ''}
      <button class="danger" data-act="remove-participant" data-room="${esc(r.name)}" data-identity="${esc(p.identity)}">Remove</button></td></tr>`;
    });
    if (!r.participants?.length) return '<p class="muted">The room is empty.</p>';
    return table(['Participant', 'Role', 'State', 'Quality', 'RTT', 'ICE', 'Media', 'BWE', 'SDK', ''], rows, 'No participant matches the filter.');
  }

  private participantDetail(r: Room, p: Participant): string {
    const who = new Map((r.participants ?? []).map((x) => [x.sid, x.identity]));
    const ro = role(p);
    const info = `<p>${badge(ro, ro === 'presenter' ? 'ok' : 'muted')} ${qualityBadge(p.connection_quality)} RTT ${fmtNum(p.rtt_ms)} ms · ICE ${esc(p.ice_candidate_type || '–')}
      · joined ${new Date(p.joined_at).toLocaleTimeString()}${p.client_sdk ? ` · ${esc(p.client_sdk)}` : ''}</p>`;
    const pub = (p.published ?? []).map((t) => `<tr><td><a href="#/quality/${encodeURIComponent(t.sid)}">${esc(t.sid)}</a></td><td>${esc(t.kind)}</td><td>${esc(t.source)}</td><td>${esc(t.codec)}</td>
        <td>${t.width ? `${t.width}×${t.height}` : ''}</td><td>${fmtBps(t.bitrate_bps)}</td><td>${t.loss_percent.toFixed(1)}%</td><td>${t.jitter_ms.toFixed(1)} ms</td><td>${t.subscribers}</td>
        <td>${trackState(t)}</td>
        <td class="actions"><button data-act="mute-track" data-room="${esc(r.name)}" data-identity="${esc(p.identity)}" data-track="${esc(t.sid)}" data-muted="${!t.muted}">${t.muted ? 'Unmute' : 'Mute'}</button>
        <button data-act="start-recording" data-room="${esc(r.name)}" data-track="${esc(t.sid)}">Record</button></td></tr>`);
    const sub = (p.subscribed ?? []).map((t) => `<tr><td><a href="#/rooms/${encodeURIComponent(r.name)}/${encodeURIComponent(t.publisher_sid)}">${esc(who.get(t.publisher_sid) ?? t.publisher_sid)}</a></td><td><a href="#/quality/${encodeURIComponent(t.track_sid)}">${esc(t.track_sid)}</a></td><td>${esc(t.kind)}</td>
      <td>${fmtBps(t.bitrate_bps)}</td><td>${t.loss_percent.toFixed(1)}%</td><td>${t.rtt_ms.toFixed(0)} ms</td><td>${t.restarts}</td>
      <td>${t.paused ? badge('paused', 'warn') : ''}${t.muted ? badge('muted') : ''}</td></tr>`);
    const pubTable = `<h3>Published</h3>${table(['Track', 'Kind', 'Source', 'Codec', 'Video', 'Bitrate', 'Loss', 'Jitter', 'Subs', 'State', ''], pub, 'Publishes nothing.')}`;
    const subTable = `<h3>Watching</h3>${table(['Presenter', 'Track', 'Kind', 'Bitrate', 'Loss', 'RTT', 'Restarts', 'Flags'], sub, 'Watches nothing.')}`;
    // A presenter only publishes and a viewer only watches: show the other table only when it has rows.
    if (ro === 'presenter') return info + pubTable + (sub.length ? subTable : '');
    return info + (pub.length ? pubTable : '') + subTable;
  }

  destroy(): void { this.root.removeEventListener('click', this.onClick); this.root.innerHTML = ''; }
}

/** What a participant sends or receives: a presenter's tracks and subscribers, a viewer's tracks and pauses.
 *  A presenter relayed from another node (no connection here) is paused upstream while unwatched: not flagged. */
function media(p: Participant): string {
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
