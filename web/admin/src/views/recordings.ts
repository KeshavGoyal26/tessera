import { action, recorders, recordings } from '../api';
import { store } from '../store';
import type { CoordRecording, RecorderStatus, RecTrack } from '../types';
import { attachTimeline, setDot, timelineUrl } from '../quality';
import { badge, esc, fmtBytes, fmtDur, fmtNum, matches, playUrl, table } from '../util';
import { PagedList, type Column } from './paged';
import { toast } from './rooms';
import { render, selectBox, type View } from './view';

export class Recordings implements View {
  private root!: HTMLElement;
  private recs: RecorderStatus[] = [];
  private recNode = '';
  private loaded = false;
  private list!: PagedList<CoordRecording>;
  private timer?: number;
  private hls?: { destroy(): void };
  private stopTimeline?: () => void;
  private onClick = (e: Event) => void this.click(e);

  mount(root: HTMLElement): void {
    this.root = root;
    root.addEventListener('click', this.onClick);
    root.innerHTML = '<div class="view-head"><h2>Recordings</h2></div><div class="player" hidden><div class="player-bar"><b class="player-title"></b><button data-act="close-player">Close</button></div><video controls playsinline></video><div class="qline" hidden><i class="cq"></i><div class="qbar" role="img"></div><span class="qsum muted"></span></div><p class="muted player-msg" role="status"></p></div><h3>All recordings</h3><div class="coord"></div><div class="body"></div>';
    const head = root.querySelector<HTMLElement>('.view-head')!;
    const status = selectBox(head, 'Status', [['', 'All statuses'], ['active', 'In progress'], ['ended', 'Ended'], ['failed', 'Failed']], () => this.list.reload());
    // The coordinator's list is searched and paged on the server; the workers below are small and local.
    this.list = new PagedList<CoordRecording>({
      head, body: root.querySelector('.coord')!, filterLabel: 'Search participant, room or id', sort: 'started', desc: true, name: 'recordings',
      empty: 'No recordings yet.', columns: coordColumns, onData: () => this.update(),
      fetch: (st) => recordings({ q: st.q.trim(), status: status.value, offset: st.offset, limit: st.limit }), // newest first (the server's only order)
    });
    void this.refreshCoord();
    // The workers are not in the live stream; refresh them while the page is visible.
    this.timer = window.setInterval(() => { if (!document.hidden) void this.refreshCoord(); }, 5000);
    this.update();
  }

  private async refreshCoord(): Promise<void> {
    const node = (this.recNode = store.node);
    try {
      const r = await recorders({ node });
      if (node === store.node) this.recs = r.recorders ?? [];
    } catch { /* the workers' cards keep their last state */ }
    this.loaded = true;
    this.update();
  }

  private async click(e: Event): Promise<void> {
    const b = (e.target as HTMLElement).closest<HTMLButtonElement>('button[data-act]');
    if (!b) return;
    const d = b.dataset as Record<string, string>;
    if (d.act === 'stop-recording') {
      if (!confirm(`Stop recording ${d.id}?`)) return;
      try { await action('stop-recording', { recording_id: d.id }); toast('Stop requested'); this.list.reload(); void this.refreshCoord(); } catch (err) { toast((err as Error).message, true); }
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
    // The node picker changed: fetch that node's recorders now.
    if (this.loaded && this.recNode !== store.node) { this.recNode = store.node; this.recs = []; void this.refreshCoord(); }
    const body = this.root.querySelector<HTMLElement>('.body');
    if (!body) return;
    const q = this.list?.state.q ?? '';
    const recs = this.recs;
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
    render(body, `<h3>Recorder workers</h3><div class="cards">${workers || '<p class="muted">No recorder stats: recording is disabled or the metrics listener is off.</p>'}</div>
      <h3>Active tracks</h3>${table(['Recording', 'Room', 'Track', 'Codec', 'Duration', 'Segments', 'Backlog', 'Lost', 'Last RTP', 'State'], activeRows, 'No track is being recorded.')}
      <h3>Finished on this worker</h3>${table(['Participant', 'Room', 'Ended', 'Duration', 'Status', 'Watch', 'Error'], finRows)}`);
  }

  destroy(): void {
    window.clearInterval(this.timer);
    this.list.destroy();
    this.hls?.destroy();
    this.stopTimeline?.();
    this.root.removeEventListener('click', this.onClick);
    this.root.innerHTML = '';
  }
}

const statusOf = (c: CoordRecording): string => c.status.replace('RECORDING_STATUS_', '').toLowerCase();
const isLive = (c: CoordRecording): boolean => /starting|active|ending/.test(statusOf(c));
const startedAt = (c: CoordRecording): Date | undefined => (c.started_at_ms ? new Date(Number(c.started_at_ms)) : undefined);
const durationMs = (c: CoordRecording): number => {
  const started = startedAt(c);
  return started ? (c.ended_at_ms ? Number(c.ended_at_ms) : isLive(c) ? Date.now() : started.getTime()) - started.getTime() : 0;
};

const coordColumns: Column<CoordRecording>[] = [
  { label: 'Participant', get: who },
  { label: 'Room', get: (c) => c.room },
  { label: 'Started', get: (c) => startedAt(c)?.toISOString(), cell: (c) => startedAt(c)?.toLocaleTimeString() ?? '' },
  { label: 'Duration', get: (c) => durationMs(c), cell: (c) => (startedAt(c) ? fmtDur(durationMs(c)) : '') },
  { label: 'Status', get: statusOf, cell: (c) => {
    const st = statusOf(c);
    return st === 'complete' ? badge(st, 'ok') : isLive(c) ? badge(st === 'active' ? '● recording' : st, 'warn') : (st === 'aborted' ? badge(st, 'muted') : badge(st, 'bad'));
  } },
  { label: '', cls: 'actions', cell: (c) => {
    const started = startedAt(c), live = isLive(c), url = playUrl(c);
    const title = `${who(c) || c.recording_id} · ${c.room}${started ? ' · ' + started.toLocaleString() : ''}`;
    const play = url ? `<button class="primary" data-act="play" data-url="${esc(url)}" data-title="${esc(title + (live ? ' · live' : ''))}">${live ? '▶ Watch live' : '▶ Play'}</button>` : '';
    const stop = /STARTING|ACTIVE/.test(c.status) ? `<button class="danger" data-act="stop-recording" data-id="${esc(c.recording_id)}">■ Stop</button>` : '';
    return `${play} ${stop}`;
  } },
  { label: 'Error', get: (c) => c.error, cell: (c) => `<span title="${esc(c.recording_id)}">${esc(c.error ?? '')}</span>` },
  { label: 'Recording id', get: (c) => c.recording_id },
];

/** Who a recording is of: the participant asked for, else the one in its outputs, else its track. */
function who(c: CoordRecording): string {
  return c.request?.participant?.identity ?? c.outputs?.find((o) => o.participant_identity)?.participant_identity ?? (c.request?.track ? `track ${c.request.track.track_sid}` : '');
}
