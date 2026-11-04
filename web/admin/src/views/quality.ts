import { store, type TrackPoint } from '../store';
import type { Participant, Published, Room } from '../types';
import { badge, esc, fmtBps, layerName } from '../util';
import { table } from './rooms';
import { ChartGroup, type View } from './view';

export class Quality implements View {
  private root!: HTMLElement;
  private sid = '';
  private detail!: HTMLElement;
  private charts?: ChartGroup;

  mount(root: HTMLElement, args: string[]): void {
    this.root = root;
    this.sid = decodeURIComponent(args[0] ?? '');
    root.innerHTML = '<h2>Track quality</h2><div class="body"></div>';
    this.detail = root.querySelector('.body')!;
    if (this.sid) {
      this.detail.innerHTML = '<div class="info"></div><div class="grid charts"></div><div class="tables"></div>';
      const g = (this.charts = new ChartGroup(this.detail.querySelector('.charts')!));
      const H = (): TrackPoint[] => store.tracks.get(`pub:${this.sid}`) ?? [];
      g.add({ title: 'Bitrate per layer', series: [0, 1, 2].map((i) => ({ label: layerName(i), color: `--c${i + 1}`, get: (k: number) => H()[k]!.layers[i] ?? 0 })), fmt: fmtBps });
      g.add({ title: 'Loss %', series: [{ label: 'loss', color: '--c6', get: (k) => H()[k]!.loss, fill: true }], fmt: (v) => v.toFixed(1) + '%' });
      g.add({ title: 'Jitter (ms)', series: [{ label: 'jitter', color: '--c4', get: (k) => H()[k]!.jitter }], fmt: (v) => v.toFixed(1) });
      g.add({ title: 'Frame rate', series: [{ label: 'fps', color: '--c2', get: (k) => H()[k]!.fps }], fmt: (v) => v.toFixed(0) });
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
      const rows: string[] = [];
      for (const r of rooms) for (const p of r.participants ?? []) for (const t of p.published ?? []) {
        rows.push(`<tr><td><a href="#/quality/${encodeURIComponent(t.sid)}">${esc(t.sid)}</a></td><td>${esc(r.name)}</td><td>${esc(p.identity)}</td><td>${esc(t.kind)}/${esc(t.source)}</td>
          <td>${fmtBps(t.bitrate_bps)}</td><td>${t.loss_percent.toFixed(1)}%</td><td>${t.jitter_ms.toFixed(1)} ms</td><td>${t.nacks}/${t.plis}/${t.firs}</td><td>${t.subscribers}</td></tr>`);
      }
      this.detail.innerHTML = table(['Track', 'Room', 'Publisher', 'Kind', 'Bitrate', 'Loss', 'Jitter', 'NACK/PLI/FIR', 'Subs'], rows);
      return;
    }
    const f = this.find();
    const info = this.detail.querySelector('.info')!;
    const tables = this.detail.querySelector('.tables')!;
    if (!f) { info.innerHTML = `<p class="muted">Track ${esc(this.sid)} not found. <a href="#/quality">Back</a></p>`; tables.innerHTML = ''; return; }
    const { room, p, t } = f;
    info.innerHTML = `<p><a href="#/quality">All tracks</a> · <b>${esc(t.sid)}</b> ${esc(t.kind)} ${esc(t.codec)} from ${esc(p.identity)} in ${esc(room.name)}
      ${t.muted ? badge('muted', 'warn') : ''}${t.simulcast ? badge('simulcast') : ''} · NACK ${t.nacks} · PLI ${t.plis} · FIR ${t.firs} · ${t.packets_lost}/${t.packets_recv} packets lost</p>`;
    const layers = (t.layers ?? []).map((l) => `<tr><td>${esc(l.rid || layerName(l.spatial))}</td><td>${l.width ? `${l.width}×${l.height}` : '–'}</td><td>${l.fps.toFixed(1)}</td><td>${fmtBps(l.bitrate_bps)}</td><td>${l.active ? badge('active', 'ok') : badge('inactive')}</td></tr>`);
    const subs: string[] = [];
    for (const rp of room.participants ?? []) for (const s of rp.subscribed ?? []) if (s.track_sid === t.sid)
      subs.push(`<tr><td>${esc(rp.identity)}</td><td>${layerName(s.current_spatial)} → ${layerName(s.target_spatial)} (max ${layerName(s.max_spatial)})</td><td>${fmtBps(s.bandwidth_estimate_bps)}</td><td>${fmtBps(s.bitrate_bps)}</td>
        <td>${s.loss_percent.toFixed(1)}%</td><td>${s.rtt_ms.toFixed(0)} ms</td><td>${s.layer_switches}</td><td>${s.nacks}/${s.plis}/${s.retransmits}</td><td>${s.paused ? badge('paused', 'warn') : ''}${s.dynacast_paused ? badge('dynacast paused') : ''}</td></tr>`);
    tables.innerHTML = `<h3>Layers</h3>${table(['Layer', 'Resolution', 'FPS', 'Bitrate', 'State'], layers)}
      <h3>Subscribers</h3>${table(['Subscriber', 'Layer', 'Bandwidth estimate', 'Bitrate', 'Loss', 'RTT', 'Switches', 'NACK/PLI/retx', 'Flags'], subs)}`;
    const h = store.tracks.get(`pub:${this.sid}`) ?? [];
    this.charts?.update(h.map((x) => x.t / 1000), h.length);
  }

  destroy(): void { this.charts?.destroy(); this.root.innerHTML = ''; }
}
