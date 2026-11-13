import { track, tracks } from '../api';
import { store } from '../store';
import type { TrackBrief, TrackDetail } from '../types';
import { badge, esc, fmtBps, fmtNum, pushCap, safeDecode, table } from '../util';
import { listParams, PagedList, Poller, type Column } from './paged';
import { trackState } from './rooms';
import { ChartGroup, render, type View } from './view';

const trackColumns: Column<TrackBrief>[] = [
  { label: 'Track', get: (t) => t.sid, cell: (t) => `<a href="#/quality/${encodeURIComponent(t.sid)}">${esc(t.sid)}</a>` },
  { label: 'Room', sort: 'room', get: (t) => t.room },
  { label: 'Presenter', sort: 'participant', get: (t) => t.participant },
  { label: 'Kind', get: (t) => `${t.kind}/${t.source}` },
  { label: 'Bitrate', sort: 'bitrate', desc: true, get: (t) => t.bitrate_bps, cell: (t) => fmtBps(t.bitrate_bps) },
  { label: 'Loss', sort: 'loss', desc: true, get: (t) => t.loss_percent, cell: (t) => `${t.loss_percent.toFixed(1)}%` },
  { label: 'Jitter', sort: 'jitter', desc: true, get: (t) => t.jitter_ms, cell: (t) => `${t.jitter_ms.toFixed(1)} ms` },
  { label: 'NACK/PLI', get: (t) => `${t.nacks}/${t.plis}` },
  { label: 'Subs', sort: 'subscribers', desc: true, get: (t) => t.subscribers },
  { label: 'State', get: (t) => (t.muted ? 'muted' : t.active ? 'receiving' : 'no media'), cell: trackState },
];

interface TrackPoint { t: number; bitrate: number; loss: number; jitter: number }

export class Quality implements View {
  private root!: HTMLElement;
  private sid = '';
  private body!: HTMLElement;
  private charts?: ChartGroup;
  private list?: PagedList<TrackBrief>;
  private poller?: Poller<TrackDetail>;
  private hist: TrackPoint[] = []; // the open track only, 15 minutes at 1 Hz

  mount(root: HTMLElement, args: string[]): void {
    this.root = root;
    this.sid = safeDecode(args[0] ?? '');
    root.innerHTML = '<div class="view-head"><h2>Track quality</h2></div><div class="body"></div>';
    this.body = root.querySelector('.body')!;
    if (!this.sid) {
      this.list = new PagedList<TrackBrief>({
        head: root.querySelector('.view-head')!, body: this.body, filterLabel: 'Filter by room, presenter or track', sort: 'loss', desc: true, scope: () => store.node,
        empty: 'No published tracks on this node.',
        name: 'tracks', columns: trackColumns,
        fetch: (st) => tracks({ ...listParams(st), node: store.node }),
      });
      return;
    }
    this.body.innerHTML = '<div class="info"></div><div class="grid charts"></div><div class="tables"></div>';
    const g = (this.charts = new ChartGroup(this.body.querySelector('.charts')!));
    const H = () => this.hist;
    g.add({ title: 'Bitrate', series: [{ label: 'bitrate', color: '--c1', get: (k) => H()[k]!.bitrate, fill: true }], fmt: fmtBps });
    g.add({ title: 'Loss %', series: [{ label: 'loss', color: '--c6', get: (k) => H()[k]!.loss, fill: true }], fmt: (v) => v.toFixed(1) + '%' });
    g.add({ title: 'Jitter (ms)', series: [{ label: 'jitter', color: '--c4', get: (k) => H()[k]!.jitter }], fmt: (v) => v.toFixed(1) });
    // The history exists only here, built from the detail polled once a second.
    this.poller = new Poller<TrackDetail>({
      fetch: () => track({ sid: this.sid, node: store.node }),
      ms: 1000, scope: () => store.node, onScope: () => { this.hist = []; },
      onData: (d) => this.show(d),
      onError: (e) => render(this.body.querySelector<HTMLElement>('.info')!, `<p class="muted">Track ${esc(this.sid)} ${(e as { status?: number }).status === 404 ? 'is not published on this node (any more)' : `could not be loaded: ${esc(e.message)}`}. <a href="#/quality">All tracks</a></p>`),
    });
    this.poller.start();
  }

  private show(d: TrackDetail): void {
    const t = d.track;
    render(this.body.querySelector<HTMLElement>('.info')!, `<p><a href="#/quality">All tracks</a> · <b>${esc(t.sid)}</b> ${esc(t.kind)} ${esc(t.codec)} from
      <a href="#/rooms/${encodeURIComponent(d.room)}/${encodeURIComponent(d.participant_sid)}">${esc(d.participant)}</a> in ${esc(d.room)}
      · NACK ${t.nacks} · PLI ${t.plis} · FIR ${t.firs} · ${t.packets_lost}/${t.packets_recv} packets lost</p>`);
    const stream = [`<tr><td>${t.ssrc || '–'}</td><td>${t.width ? `${t.width}×${t.height}` : '–'}</td><td>${fmtBps(t.bitrate_bps)}</td><td>${trackState(t)}</td></tr>`];
    const subs = (d.subscribers ?? []).map((s) => `<tr><td><a href="#/rooms/${encodeURIComponent(d.room)}/${encodeURIComponent(s.sid)}">${esc(s.identity)}</a></td><td>${fmtBps(s.bwe_bps)}</td><td>${fmtBps(s.bitrate_bps)}</td>
        <td>${s.loss_percent.toFixed(1)}%</td><td>${s.jitter_ms.toFixed(1)} ms</td><td>${s.rtt_ms.toFixed(0)} ms</td><td>${s.restarts}</td><td>${s.paused ? badge('paused', 'warn') : ''}${s.muted ? badge('muted') : ''}</td></tr>`);
    const cut = d.subscribers_total > subs.length ? `<p class="muted more">Showing the ${fmtNum(subs.length)} with the most loss of ${fmtNum(d.subscribers_total)}.</p>` : '';
    render(this.body.querySelector<HTMLElement>('.tables')!, `<h3>Stream</h3>${table(['SSRC', 'Resolution', 'Bitrate', 'State'], stream)}
      <h3>Subscribers</h3>${table(['Subscriber', 'Bandwidth estimate', 'Bitrate', 'Loss', 'Jitter', 'RTT', 'Restarts', 'Flags'], subs, 'Nobody watches this track.', 100)}${cut}`);
    pushCap(this.hist, { t: Date.now(), bitrate: t.bitrate_bps, loss: t.loss_percent, jitter: t.jitter_ms });
    this.charts?.update(this.hist.map((x) => x.t / 1000), this.hist.length);
  }

  update(): void { this.list?.check(); this.poller?.check(); }

  destroy(): void { this.list?.destroy(); this.poller?.stop(); this.charts?.destroy(); this.root.innerHTML = ''; }
}
