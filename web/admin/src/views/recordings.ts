import { action, recordings } from '../api';
import { store } from '../store';
import type { CoordRecording } from '../types';
import { badge, esc, fmtBytes, fmtDur, fmtNum, playUrl } from '../util';
import { table, toast } from './rooms';
import type { View } from './view';

export class Recordings implements View {
  private root!: HTMLElement;
  private coord: CoordRecording[] = [];
  private coordErr = '';
  private timer?: number;
  private hls?: { destroy(): void };
  private onClick = (e: Event) => void this.click(e);

  mount(root: HTMLElement): void {
    this.root = root;
    root.addEventListener('click', this.onClick);
    root.innerHTML = '<h2>Recordings</h2><div class="player" hidden><div class="player-bar"><b class="player-title"></b><button data-act="close-player">Close</button></div><video controls playsinline></video><p class="muted player-msg"></p></div><div class="body"></div>';
    void this.refreshCoord();
    this.timer = window.setInterval(() => void this.refreshCoord(), 5000);
    this.update();
  }

  private async refreshCoord(): Promise<void> {
    try { this.coord = (await recordings()).recordings; this.coordErr = ''; } catch (e) { this.coordErr = (e as Error).message; }
    this.update();
  }

  private async click(e: Event): Promise<void> {
    const b = (e.target as HTMLElement).closest<HTMLButtonElement>('button[data-act]');
    if (!b) return;
    const d = b.dataset as Record<string, string>;
    if (d.act === 'stop-recording') {
      if (!confirm(`Stop recording ${d.id}?`)) return;
      try { await action('stop-recording', { recording_id: d.id }); toast('Stop requested'); void this.refreshCoord(); } catch (err) { toast((err as Error).message, true); }
    } else if (d.act === 'play') {
      await this.play(d.url!, d.title ?? '');
    } else if (d.act === 'close-player') {
      this.closePlayer();
    }
  }

  private async play(url: string, title: string): Promise<void> {
    const box = this.root.querySelector<HTMLElement>('.player')!;
    const video = box.querySelector('video')!;
    const msg = box.querySelector('.player-msg')!;
    box.hidden = false;
    box.scrollIntoView({ behavior: 'smooth', block: 'start' });
    box.querySelector('.player-title')!.textContent = title;
    msg.textContent = '';
    this.hls?.destroy();
    if (video.canPlayType('application/vnd.apple.mpegurl')) { video.src = url; void video.play().catch(() => undefined); return; }
    const { default: Hls } = await import('hls.js');
    if (!Hls.isSupported()) { msg.textContent = 'HLS playback is not supported in this browser.'; return; }
    const hls = new Hls();
    hls.on(Hls.Events.ERROR, (_e, data) => { if (data.fatal) msg.textContent = `Playback error: ${data.details}`; });
    hls.loadSource(url);
    hls.attachMedia(video);
    this.hls = hls;
    void video.play().catch(() => undefined);
  }

  private closePlayer(): void {
    this.hls?.destroy();
    this.hls = undefined;
    const box = this.root.querySelector<HTMLElement>('.player')!;
    box.querySelector('video')!.removeAttribute('src');
    box.hidden = true;
  }

  update(): void {
    const body = this.root.querySelector('.body');
    if (!body) return;
    const recs = store.update?.recorders ?? [];
    const workers = recs.map((r) => {
      if (!r.ok || !r.stats) return `<div class="card bad"><div class="label">${esc(r.url)}</div><div class="value">down</div><div class="sub">${esc(r.error ?? '')}</div></div>`;
      const s = r.stats;
      return `<div class="card"><div class="label">${esc(s.worker_id)}</div><div class="value">${s.active_recordings} active</div>
        <div class="sub">${s.tracks_recording} tracks · capacity ${s.capacity_used}/${s.capacity} · backlog ${s.upload_backlog} · delay ${fmtNum(s.segment_delay_ms)} ms (max ${fmtNum(s.segment_delay_max_ms)}) · ${fmtBytes(s.bytes_uploaded)} uploaded · errors ${s.upload_errors + s.track_errors}</div></div>`;
    }).join('');
    const active = recs.flatMap((r) => r.stats?.recordings ?? []);
    const activeRows = active.flatMap((a) => a.tracks.map((t) => `<tr><td>${esc(a.recording_id)}</td><td>${esc(a.room)}</td><td>${esc(t.participant)}/${esc(t.track_sid)} ${esc(t.kind)}</td><td>${esc(t.codec)}${t.width ? ` ${t.width}×${t.height}` : ''}</td>
      <td>${fmtDur(a.duration_ms)}</td><td>${t.segments}</td><td>${t.upload_backlog}</td><td>${t.packets_lost}</td><td>${(t.last_packet_age_ms / 1000).toFixed(1)}s</td><td>${t.upload_errors || t.error ? badge(t.error ?? 'upload errors', 'bad') : badge('ok', 'ok')}</td></tr>`));
    const fin = recs.flatMap((r) => r.stats?.finished ?? []).sort((a, b) => b.ended_at.localeCompare(a.ended_at));
    const finRows = fin.map((f) => {
      const who = [...new Set(f.outputs.map((o) => o.participant).filter(Boolean))].join(', ') || '—';
      const title = `${who} · ${f.room} · ${new Date(f.ended_at).toLocaleString()}`;
      const playable = f.outputs.filter((o) => o.playlist_url);
      // One click plays everything together: the master playlist pairs video with its audio.
      const main = f.master_url ?? (playable.length === 1 ? playable[0].playlist_url : undefined);
      const play = main
        ? `<button class="primary" data-act="play" data-url="${esc(main)}" data-title="${esc(title)}">▶ Play</button>`
        : playable.length ? '' : f.outputs.some((o) => o.playlist_location)
          ? `<span class="muted" title="${esc(f.outputs.map((o) => o.playlist_location).join('\n'))}">files only</span>`
          : '<span class="muted" title="Nothing was published while recording">—</span>';
      const tracks = playable.length > 1 || (!main && playable.length)
        ? playable.map((o) => `<button data-act="play" data-url="${esc(o.playlist_url!)}" data-title="${esc(title + ' · ' + o.track_sid)}" title="Play this track alone">${esc(o.track_sid.slice(-6))}</button>`).join(' ')
        : '';
      const st = f.status === 'RECORDING_STATUS_COMPLETE' ? badge('complete', 'ok') : badge(f.status.replace('RECORDING_STATUS_', '').toLowerCase(), 'bad');
      return `<tr><td>${esc(who)}</td><td>${esc(f.room)}</td><td>${new Date(f.ended_at).toLocaleTimeString()}</td><td>${fmtDur(f.duration_ms)}</td><td>${st}</td>
        <td class="actions">${play} ${tracks}</td><td>${esc(f.error ?? '')}</td></tr>`;
    });
    const coordRows = this.coord.slice().reverse().map((c) => {
      const who = c.request?.participant?.identity ?? c.outputs?.find((o) => o.participant_identity)?.participant_identity ?? (c.request?.track ? `track ${c.request.track.track_sid}` : '');
      const started = c.started_at_ms ? new Date(Number(c.started_at_ms)) : undefined;
      const dur = started && c.ended_at_ms ? fmtDur(Number(c.ended_at_ms) - started.getTime()) : '';
      const st = c.status.replace('RECORDING_STATUS_', '').toLowerCase();
      const stBadge = st === 'complete' ? badge(st, 'ok') : /starting|active|ending/.test(st) ? badge(st === 'active' ? '● recording' : st, 'warn') : (st === 'aborted' ? badge(st, 'muted') : badge(st, 'bad'));
      const url = playUrl(c);
      const title = `${who || c.recording_id} · ${c.room}${started ? ' · ' + started.toLocaleString() : ''}`;
      const act = /STARTING|ACTIVE/.test(c.status)
        ? `<button class="danger" data-act="stop-recording" data-id="${esc(c.recording_id)}">■ Stop</button>`
        : url ? `<button class="primary" data-act="play" data-url="${esc(url)}" data-title="${esc(title)}">▶ Play</button>` : '';
      return `<tr><td>${esc(who)}</td><td>${esc(c.room)}</td><td>${started ? started.toLocaleTimeString() : ''}</td><td>${dur}</td><td>${stBadge}</td>
      <td class="actions">${act}</td><td title="${esc(c.recording_id)}">${esc(c.error ?? '')}</td></tr>`;
    });
    body.innerHTML = `<h3>All recordings</h3>${this.coordErr ? `<p class="muted">${esc(this.coordErr)}</p>` : table(['Participant', 'Room', 'Started', 'Duration', 'Status', '', 'Error'], coordRows)}
      <h3>Recorder workers</h3><div class="cards">${workers || '<p class="muted">No recorder stats configured (set TESSERA_DASHBOARD_RECORDERS).</p>'}</div>
      <h3>Active tracks</h3>${table(['Recording', 'Room', 'Track', 'Codec', 'Duration', 'Segments', 'Backlog', 'Lost', 'Last RTP', 'State'], activeRows)}
      <h3>Finished on this worker</h3>${table(['Participant', 'Room', 'Ended', 'Duration', 'Status', 'Watch', 'Error'], finRows)}`;
  }

  destroy(): void {
    window.clearInterval(this.timer);
    this.hls?.destroy();
    this.root.removeEventListener('click', this.onClick);
    this.root.innerHTML = '';
  }
}

