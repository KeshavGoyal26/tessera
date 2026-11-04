import { action } from '../api';
import { store } from '../store';
import type { Participant, Room } from '../types';
import { badge, esc, fmtBps, fmtNum, layerName, qualityBadge } from '../util';
import type { View } from './view';

export class Rooms implements View {
  private root!: HTMLElement;
  private args: string[] = [];
  private onClick = (e: Event) => void this.click(e);

  mount(root: HTMLElement, args: string[]): void {
    this.root = root;
    this.args = args.map(decodeURIComponent);
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
      toast(`${d.act} OK`);
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
    if (!roomName) body = this.roomsTable(rooms);
    else if (!room) body = `<p class="muted">Room "${esc(roomName)}" not found (it may have ended).</p>`;
    else if (!part) body = this.participantsTable(room);
    else body = this.tracksTables(room, part);
    this.root.innerHTML = `<h2>${crumbs.join(' <span class="muted">›</span> ')}</h2>${body}`;
  }

  private roomsTable(rooms: Room[]): string {
    if (!rooms.length) return '<p class="muted">No rooms.</p>';
    const rows = rooms.map((r) => {
      const ps = r.participants ?? [];
      const tracks = ps.reduce((n, p) => n + (p.published?.length ?? 0), 0);
      const speakers = (r.active_speakers ?? []).map((s) => esc(s.identity)).join(', ');
      return `<tr><td><a href="#/rooms/${encodeURIComponent(r.name)}">${esc(r.name)}</a></td><td>${ps.filter((p) => !p.hidden).length}</td><td>${tracks}</td>
        <td>${r.active_recording ? badge('recording', 'bad') : ''}</td><td>${speakers}</td><td>${new Date(r.created_at).toLocaleTimeString()}</td>
        <td class="actions"><button class="danger" data-act="end-room" data-room="${esc(r.name)}">End room</button></td></tr>`;
    });
    return table(['Room', 'Participants', 'Published tracks', 'Recording', 'Speaking', 'Created', ''], rows);
  }

  private participantsTable(r: Room): string {
    const rows = (r.participants ?? []).map((p) => `<tr><td><a href="#/rooms/${encodeURIComponent(r.name)}/${encodeURIComponent(p.sid)}">${esc(p.identity)}</a> ${p.hidden ? badge('hidden') : ''}${p.kind !== 'standard' ? badge(p.kind) : ''}</td>
      <td>${esc(p.state)}</td><td>${qualityBadge(p.connection_quality)}</td><td>${fmtNum(p.rtt_ms)} ms</td><td>${esc(p.ice_candidate_type)}</td>
      <td>${p.published?.length ?? 0} / ${p.subscribed?.length ?? 0}</td><td>${fmtBps(p.bandwidth_estimate_bps)}</td><td>${esc(p.client_sdk ?? '')}</td>
      <td class="actions"><button data-act="start-recording" data-room="${esc(r.name)}" data-identity="${esc(p.identity)}">Record</button>
      <button class="danger" data-act="remove-participant" data-room="${esc(r.name)}" data-identity="${esc(p.identity)}">Remove</button></td></tr>`);
    return table(['Participant', 'State', 'Quality', 'RTT', 'ICE', 'Pub / Sub', 'BWE', 'SDK', ''], rows);
  }

  private tracksTables(r: Room, p: Participant): string {
    const pub = (p.published ?? []).map((t) => {
      const top = (t.layers ?? []).filter((l) => l.active).sort((a, b) => b.spatial - a.spatial)[0];
      return `<tr><td><a href="#/quality/${encodeURIComponent(t.sid)}">${esc(t.sid)}</a></td><td>${esc(t.kind)}</td><td>${esc(t.source)}</td><td>${esc(t.codec)}</td>
        <td>${top ? `${top.width}×${top.height}@${top.fps.toFixed(0)}` : ''}</td><td>${fmtBps(t.bitrate_bps)}</td><td>${t.loss_percent.toFixed(1)}%</td><td>${t.jitter_ms.toFixed(1)} ms</td><td>${t.subscribers}</td>
        <td>${t.muted ? badge('muted', 'warn') : ''}${t.simulcast ? badge('simulcast') : ''}</td>
        <td class="actions"><button data-act="mute-track" data-room="${esc(r.name)}" data-identity="${esc(p.identity)}" data-track="${esc(t.sid)}" data-muted="${!t.muted}">${t.muted ? 'Unmute' : 'Mute'}</button>
        <button data-act="start-recording" data-room="${esc(r.name)}" data-track="${esc(t.sid)}">Record</button></td></tr>`;
    });
    const sub = (p.subscribed ?? []).map((t) => `<tr><td>${esc(t.track_sid)}</td><td>${esc(t.kind)}</td><td>${layerName(t.current_spatial)} → ${layerName(t.target_spatial)}</td>
      <td>${fmtBps(t.bitrate_bps)}</td><td>${t.loss_percent.toFixed(1)}%</td><td>${t.rtt_ms.toFixed(0)} ms</td><td>${t.layer_switches}</td>
      <td>${t.paused ? badge('paused', 'warn') : ''}${t.dynacast_paused ? badge('dynacast') : ''}</td></tr>`);
    return `<h3>Published</h3>${table(['Track', 'Kind', 'Source', 'Codec', 'Video', 'Bitrate', 'Loss', 'Jitter', 'Subs', 'Flags', ''], pub)}
      <h3>Subscribed</h3>${table(['Track', 'Kind', 'Layer', 'Bitrate', 'Loss', 'RTT', 'Switches', 'Flags'], sub)}`;
  }

  destroy(): void { this.root.removeEventListener('click', this.onClick); this.root.innerHTML = ''; }
}

export function table(head: string[], rows: string[]): string {
  if (!rows.length) return '<p class="muted">Nothing to show.</p>';
  return `<div class="table-wrap"><table><thead><tr>${head.map((h) => `<th>${h}</th>`).join('')}</tr></thead><tbody>${rows.join('')}</tbody></table></div>`;
}

export function toast(msg: string, bad = false): void {
  const el = document.createElement('div');
  el.className = 'toast' + (bad ? ' bad' : '');
  el.textContent = msg;
  document.body.appendChild(el);
  setTimeout(() => el.remove(), 4000);
}
