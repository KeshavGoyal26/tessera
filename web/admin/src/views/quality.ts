import { store, type TrackPoint } from '../store';
import type { Participant, Published, Room } from '../types';
import { badge, esc, fmtBps, matches, table, safeDecode } from '../util';
import { trackState } from './rooms';
import { ChartGroup, filterBox, render, type View } from './view';

export class Quality implements View {
  private root!: HTMLElement;
  private sid = '';
  private detail!: HTMLElement;
  private charts?: ChartGroup;
  private filter?: HTMLInputElement;

  mount(root: HTMLElement, args: string[]): void {
    this.root = root;
    this.sid = safeDecode(args[0] ?? '');
    root.innerHTML = '<div class="view-head"><h2>Track quality</h2></div><div class="body"></div>';
    this.detail = root.querySelector('.body')!;
    if (!this.sid) this.filter = filterBox(root.querySelector('.view-head')!, 'Filter by room, presenter or track', () => this.update());
    if (this.sid) {
      this.detail.innerHTML = '<div class="info"></div><div class="grid charts"></div><div class="tables"></div>';
      const g = (this.charts = new ChartGroup(this.detail.querySelector('.charts')!));
      const H = (): TrackPoint[] => store.tracks.get(`pub:${this.sid}`) ?? [];
      g.add({ title: 'Bitrate', series: [{ label: 'bitrate', color: '--c1', get: (k) => H()[k]!.bitrate, fill: true }], fmt: fmtBps });
      g.add({ title: 'Loss %', series: [{ label: 'loss', color: '--c6', get: (k) => H()[k]!.loss, fill: true }], fmt: (v) => v.toFixed(1) + '%' });
      g.add({ title: 'Jitter (ms)', series: [{ label: 'jitter', color: '--c4', get: (k) => H()[k]!.jitter }], fmt: (v) => v.toFixed(1) });
    }
    this.update();
  }

  private find(): { room: Room; p: Participant; t: Published } | undefined {
    for (const room of store.update?.snapshot.rooms ?? [])
      for (const p of room.participants ?? [])
        for (const t of p.published ?? []) if (t.sid === this.sid) return { room, p, t };
  }

  update(): void {
    const rooms = store.update?.snapshot.rooms ?? [];
    if (!this.sid) {
      const q = this.filter?.value ?? '';
      const rows: string[] = [];
      for (const r of rooms) for (const p of r.participants ?? []) for (const t of p.published ?? []) {
        if (!matches(q, r.name, p.identity, t.sid)) continue;
        rows.push(`<tr><td><a href="#/quality/${encodeURIComponent(t.sid)}">${esc(t.sid)}</a></td><td>${esc(r.name)}</td><td>${esc(p.identity)}</td><td>${esc(t.kind)}/${esc(t.source)}</td>
          <td>${fmtBps(t.bitrate_bps)}</td><td>${t.loss_percent.toFixed(1)}%</td><td>${t.jitter_ms.toFixed(1)} ms</td><td>${t.nacks}/${t.plis}/${t.firs}</td><td>${t.subscribers}</td><td>${trackState(t)}</td></tr>`);
      }
      render(this.detail, table(['Track', 'Room', 'Presenter', 'Kind', 'Bitrate', 'Loss', 'Jitter', 'NACK/PLI/FIR', 'Subs', 'State'], rows,
        store.update ? (q ? 'No track matches the filter.' : 'No published tracks on this node.') : 'Waiting for data…'));
      return;
    }
    const f = this.find();
    const info = this.detail.querySelector<HTMLElement>('.info')!;
    const tables = this.detail.querySelector<HTMLElement>('.tables')!;
    if (!f) { render(info, `<p class="muted">Track ${esc(this.sid)} is not published on this node (any more). <a href="#/quality">All tracks</a></p>`); render(tables, ''); return; }
    const { room, p, t } = f;
    render(info, `<p><a href="#/quality">All tracks</a> · <b>${esc(t.sid)}</b> ${esc(t.kind)} ${esc(t.codec)} from
      <a href="#/rooms/${encodeURIComponent(room.name)}/${encodeURIComponent(p.sid)}">${esc(p.identity)}</a> in ${esc(room.name)}
      · NACK ${t.nacks} · PLI ${t.plis} · FIR ${t.firs} · ${t.packets_lost}/${t.packets_recv} packets lost</p>`);
    const stream = [`<tr><td>${t.ssrc || '–'}</td><td>${t.width ? `${t.width}×${t.height}` : '–'}</td><td>${fmtBps(t.bitrate_bps)}</td><td>${trackState(t)}</td></tr>`];
    const subs: string[] = [];
    for (const rp of room.participants ?? []) for (const s of rp.subscribed ?? []) if (s.track_sid === t.sid)
      subs.push(`<tr><td><a href="#/rooms/${encodeURIComponent(room.name)}/${encodeURIComponent(rp.sid)}">${esc(rp.identity)}</a></td><td>${fmtBps(s.bandwidth_estimate_bps)}</td><td>${fmtBps(s.bitrate_bps)}</td>
        <td>${s.loss_percent.toFixed(1)}%</td><td>${s.rtt_ms.toFixed(0)} ms</td><td>${s.restarts}</td><td>${s.nacks}/${s.plis}/${s.retransmits}</td><td>${s.paused ? badge('paused', 'warn') : ''}${s.muted ? badge('muted') : ''}</td></tr>`);
    render(tables, `<h3>Stream</h3>${table(['SSRC', 'Resolution', 'Bitrate', 'State'], stream)}
      <h3>Subscribers</h3>${table(['Subscriber', 'Bandwidth estimate', 'Bitrate', 'Loss', 'RTT', 'Restarts', 'NACK/PLI/retx', 'Flags'], subs, 'Nobody watches this track.')}`);
    const h = store.tracks.get(`pub:${this.sid}`) ?? [];
    this.charts?.update(h.map((x) => x.t / 1000), h.length);
  }

  destroy(): void { this.charts?.destroy(); this.root.innerHTML = ''; }
}
