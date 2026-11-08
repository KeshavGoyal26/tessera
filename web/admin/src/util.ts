import type { CoordRecording } from './types';
const ESC: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
export const esc = (v: unknown): string => String(v ?? '').replace(/[&<>"']/g, (c) => ESC[c]!);

export function fmtBps(v: number): string {
  if (!isFinite(v) || v <= 0) return '0 bps';
  const u = ['bps', 'kbps', 'Mbps', 'Gbps'];
  let i = 0;
  while (v >= 1000 && i < u.length - 1) { v /= 1000; i++; }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${u[i]}`;
}
export function fmtBytes(v: number): string {
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${u[i]}`;
}
export const fmtNum = (v: number, d = 0): string => (isFinite(v) ? v.toLocaleString(undefined, { maximumFractionDigits: d }) : '–');
export function fmtDur(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), x = s % 60;
  return h ? `${h}h ${m}m` : m ? `${m}m ${x}s` : `${x}s`;
}
export function percentile(values: number[], p: number): number {
  if (!values.length) return 0;
  const s = [...values].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.ceil((p / 100) * s.length) - 1)]!;
}
export const avg = (v: number[]): number => (v.length ? v.reduce((a, b) => a + b, 0) / v.length : 0);
export function badge(text: string, kind: 'ok' | 'warn' | 'bad' | 'muted' = 'muted'): string {
  return `<span class="badge ${kind}">${esc(text)}</span>`;
}
export function qualityBadge(q: string): string {
  return badge(q, q === 'excellent' || q === 'good' ? 'ok' : q === 'poor' ? 'warn' : q === 'lost' ? 'bad' : 'muted');
}

// playUrl is what ▶ Play opens for a recording: its master playlist (video together with
// its audio) when one was written, else its only playable track. The master sits next to
// the per-track folders (<prefix>/master.m3u8 and <prefix>/<track>/index.m3u8).
export function playUrl(c: CoordRecording): string | undefined {
  const outs = (c.outputs ?? []).filter((o) => o.playlist_url);
  if (!outs.length) return undefined;
  if (c.master_playlist_location) {
    const m = outs[0].playlist_url!.replace(/\/[^/]+\/index\.m3u8$/, '/master.m3u8');
    if (m !== outs[0].playlist_url) return m;
  }
  return outs.length === 1 ? outs[0].playlist_url : undefined;
}
