import { action, recordings } from '../api';
import { store } from '../store';
import type { CoordRecording, RecTrack } from '../types';
import { attachTimeline, setDot, timelineUrl } from '../quality';
import { badge, esc, fmtBytes, fmtDur, fmtNum, matches, playUrl, table } from '../util';
import { toast } from './rooms';
import { filterBox, render, type View } from './view';

export class Recordings implements View {
  private root!: HTMLElement;
  private coord: CoordRecording[] = [];
  private coordTotal = 0;
  private coordErr = '';
  private loaded = false;
  private filter!: HTMLInputElement;
  private timer?: number;
  private hls?: { destroy(): void };
  private stopTimeline?: () => void;
  private onClick = (e: Event) => void this.click(e);

  mount(root: HTMLElement): void {
    this.root = root;
    root.addEventListener('click', this.onClick);
    root.innerHTML = '<div class="view-head"><h2>Recordings</h2></div><div class="player" hidden><div class="player-bar"><b class="player-title"></b><button data-act="close-player">Close</button></div><video controls playsinline></video><div class="qline" hidden><i class="cq"></i><div class="qbar" role="img"></div><span class="qsum muted"></span></div><p class="muted player-msg" role="status"></p></div><div class="body"></div>';
    this.filter = filterBox(root.querySelector('.view-head')!, 'Filter by participant, room or id', () => this.update());
    void this.refreshCoord();
    // The list is not in the live stream; refresh it while the page is visible.
    this.timer = window.setInterval(() => { if (!document.hidden) void this.refreshCoord(); }, 5000);
    this.update();
  }

  private async refreshCoord(): Promise<void> {
    try {
      const r = await recordings();
      this.coord = r.recordings ?? [];
      this.coordTotal = r.total ?? this.coord.length;
      this.coordErr = '';
    } catch (e) { this.coordErr = (e as Error).message; }
    this.loaded = true;
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
    this.stopTimeline?.();
    // A participant recording's connection timeline sits next to its master playlist.
    const line = box.querySelector<HTMLElement>('.qline')!;
    line.hidden = !/master\.m3u8(\?|$)/.test(url);
    if (!line.hidden) {
      const dot = line.querySelector('.cq'), sum = line.querySelector('.qsum')!;
      this.stopTimeline = attachTimeline(video, line.querySelector('.qbar')!, timelineUrl(url), (l, s) => { setDot(dot, l, 'Connection at this moment'); sum.textContent = s; });
    }
    if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.onerror = () => { msg.textContent = `Playback error: ${video.error?.message || 'the recording could not be loaded'}`; };
      video.src = url;
      void video.play().catch(() => undefined);
      return;
    }
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
    this.stopTimeline?.();
    this.stopTimeline = undefined;
    this.hls = undefined;
    const box = this.root.querySelector<HTMLElement>('.player')!;
    box.querySelector('video')!.removeAttribute('src');
    box.hidden = true;
  }

  update(): void {
    const body = this.root.querySelector<HTMLElement>('.body');
    if (!body) return;
    const q = this.filter.value;
    const recs = store.update?.recorders ?? [];
    const workers = recs.map((r) => {
      if (!r.ok || !r.stats) return `<div class="card bad"><div class="label">${esc(r.url)}</div><div class="value">down</div><div class="sub">${esc(r.error ?? '')}</div></div>`;
      const s = r.stats;
      return `<div class="card"><div class="label">${esc(s.worker_id)}</div><div class="value">${s.active_recordings} active</div>
        <div class="sub">${s.tracks_recording} tracks · capacity ${s.capacity_used}/${s.capacity} · backlog ${s.upload_backlog} · delay ${fmtNum(s.segment_delay_ms)} ms (max ${fmtNum(s.segment_delay_max_ms)}) · ${fmtBytes(s.bytes_uploaded)} uploaded · errors ${s.upload_errors + s.track_errors}</div></div>`;
    }).join('');
    const active = recs.flatMap((r) => r.stats?.recordings ?? []).filter((a) => matches(q, a.recording_id, a.room, ...a.tracks.map((t) => t.participant)));
    // Problems first (errors, then no RTP for over 3 s), the rest in a stable order.
    const rank = (t: RecTrack): number => (t.error || t.upload_errors ? 0 : t.last_packet_age_ms > 3000 ? 1 : 2);
    const tracks = active.flatMap((a) => a.tracks.map((t) => ({ a, t })))
      .sort((x, y) => rank(x.t) - rank(y.t) || x.a.recording_id.localeCompare(y.a.recording_id) || x.t.track_sid.localeCompare(y.t.track_sid));
    const activeRows = tracks.map(({ a, t }) => `<tr><td>${esc(a.recording_id)}</td><td>${esc(a.room)}</td><td>${esc(t.participant)}/${esc(t.track_sid)} ${esc(t.kind)}</td><td>${esc(t.codec)}${t.width ? ` ${t.width}×${t.height}` : ''}</td>
      <td>${fmtDur(a.duration_ms)}</td><td>${t.segments}</td><td>${t.upload_backlog}</td><td>${t.packets_lost}</td><td>${t.last_packet_age_ms > 3000 ? badge(`${(t.last_packet_age_ms / 1000).toFixed(0)}s`, 'warn') : `${(t.last_packet_age_ms / 1000).toFixed(1)}s`}</td><td>${t.upload_errors || t.error ? badge(t.error ?? 'upload errors', 'bad') : badge('ok', 'ok')}</td></tr>`);
    const fin = recs.flatMap((r) => r.stats?.finished ?? []).map((f) => ({ ...f, outputs: f.outputs ?? [] })) // null when aborted before any media
      .filter((f) => matches(q, f.recording_id, f.room, ...f.outputs.map((o) => o.participant))).sort((a, b) => b.ended_at.localeCompare(a.ended_at));
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
    const now = Date.now();
    const coordRows = this.coord.slice().reverse().filter((c) => matches(q, who(c), c.room, c.recording_id)).map((c) => {
      const name = who(c);
      const started = c.started_at_ms ? new Date(Number(c.started_at_ms)) : undefined;
      const st = c.status.replace('RECORDING_STATUS_', '').toLowerCase();
      const live = /starting|active|ending/.test(st);
      const dur = started ? fmtDur((c.ended_at_ms ? Number(c.ended_at_ms) : live ? now : started.getTime()) - started.getTime()) : '';
      const stBadge = st === 'complete' ? badge(st, 'ok') : live ? badge(st === 'active' ? '● recording' : st, 'warn') : (st === 'aborted' ? badge(st, 'muted') : badge(st, 'bad'));
      const url = playUrl(c);
      const title = `${name || c.recording_id} · ${c.room}${started ? ' · ' + started.toLocaleString() : ''}`;
      const play = url ? `<button class="primary" data-act="play" data-url="${esc(url)}" data-title="${esc(title + (live ? ' · live' : ''))}">${live ? '▶ Watch live' : '▶ Play'}</button>` : '';
      const stop = /STARTING|ACTIVE/.test(c.status) ? `<button class="danger" data-act="stop-recording" data-id="${esc(c.recording_id)}">■ Stop</button>` : '';
      return `<tr><td>${esc(name)}</td><td>${esc(c.room)}</td><td>${started ? started.toLocaleTimeString() : ''}</td><td>${dur}</td><td>${stBadge}</td>
      <td class="actions">${play} ${stop}</td><td title="${esc(c.recording_id)}">${esc(c.error ?? '')}</td></tr>`;
    });
    const running = this.coord.filter((c) => /STARTING|ACTIVE|ENDING/.test(c.status)).length;
    const listNote = this.coordTotal > this.coord.length
      ? `<p class="muted more">Listing every recording in progress and the newest ${this.coord.length - running} ended of ${this.coordTotal}; the admin API lists the rest.</p>` : '';
    const coordHtml = this.coordErr ? `<p class="error">Could not list recordings: ${esc(this.coordErr)}</p>`
      : !this.loaded ? '<p class="muted">Loading…</p>'
      : table(['Participant', 'Room', 'Started', 'Duration', 'Status', '', 'Error'], coordRows, q ? 'No recording matches the filter.' : 'No recordings yet.') + listNote;
    render(body, `<h3>All recordings${running ? ` <span class="badge warn">${running} in progress</span>` : ''}</h3>${coordHtml}
      <h3>Recorder workers</h3><div class="cards">${workers || '<p class="muted">No recorder stats: recording is disabled or the metrics listener is off.</p>'}</div>
      <h3>Active tracks</h3>${table(['Recording', 'Room', 'Track', 'Codec', 'Duration', 'Segments', 'Backlog', 'Lost', 'Last RTP', 'State'], activeRows, 'No track is being recorded.')}
      <h3>Finished on this worker</h3>${table(['Participant', 'Room', 'Ended', 'Duration', 'Status', 'Watch', 'Error'], finRows)}`);
  }

  destroy(): void {
    window.clearInterval(this.timer);
    this.hls?.destroy();
    this.stopTimeline?.();
    this.root.removeEventListener('click', this.onClick);
    this.root.innerHTML = '';
  }
}

/** Who a recording is of: the participant asked for, else the one in its outputs, else its track. */
function who(c: CoordRecording): string {
  return c.request?.participant?.identity ?? c.outputs?.find((o) => o.participant_identity)?.participant_identity ?? (c.request?.track ? `track ${c.request.track.track_sid}` : '');
}
