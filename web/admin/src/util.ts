import type { CoordRecording, NodeRoom, Participant, Snapshot } from './types';
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

// playUrl is what ▶ Play opens for a recording: the master playlist URL the server
// reports (set while recording, so a live recording can be watched and rewound), else one
// derived from the per-track URLs (the master sits next to the per-track folders:
// <prefix>/master.m3u8 and <prefix>/<track>/index.m3u8), else its only playable track.
export function playUrl(c: CoordRecording): string | undefined {
  if (c.master_playlist_url) return c.master_playlist_url;
  const outs = (c.outputs ?? []).filter((o) => o.playlist_url);
  if (!outs.length) return undefined;
  if (c.master_playlist_location) {
    const m = outs[0].playlist_url!.replace(/\/[^/]+\/index\.m3u8$/, '/master.m3u8');
    if (m !== outs[0].playlist_url) return m;
  }
  return outs.length === 1 ? outs[0].playlist_url : undefined;
}

export type Role = 'presenter' | 'viewer' | 'relay' | 'recorder';
const ROLE_ORDER: Record<Role, number> = { presenter: 0, viewer: 1, relay: 2, recorder: 3 };

// role places a participant in the presenter/viewer model (ADR-0012) from what it does:
// presenters publish, viewers only watch. Edge relay sessions (on a media node) and
// recorders are internal and listed apart.
export function role(p: Participant): Role {
  if (p.kind === 'relay') return 'relay';
  if (p.kind === 'recorder') return 'recorder';
  return p.published?.length ? 'presenter' : 'viewer';
}

const cmp = (a: string, b: string): number => (a < b ? -1 : a > b ? 1 : 0);
const videoFirst = (a: { kind: string }, b: { kind: string }): number => Number(a.kind !== 'video') - Number(b.kind !== 'video');

// sortSnapshot orders a snapshot in place: rooms by name, presenters before viewers, then
// by identity, video before audio. The server builds the lists from maps, so without this
// rows would swap places every second (and a button could move under the pointer).
export function sortSnapshot(s: Snapshot): void {
  const rooms = s.rooms ?? [];
  rooms.sort((a, b) => cmp(a.name, b.name));
  for (const r of rooms) {
    const ps = r.participants ?? [];
    ps.sort((a, b) => ROLE_ORDER[role(a)] - ROLE_ORDER[role(b)] || cmp(a.identity, b.identity) || cmp(a.sid, b.sid));
    const who = new Map(ps.map((p) => [p.sid, p.identity]));
    for (const p of ps) {
      p.published?.sort((a, b) => videoFirst(a, b) || cmp(a.sid, b.sid));
      p.subscribed?.sort((a, b) => cmp(who.get(a.publisher_sid) ?? '', who.get(b.publisher_sid) ?? '') || videoFirst(a, b) || cmp(a.track_sid, b.track_sid));
    }
  }
}

/** Counts the participants of rooms by role (hidden ones included: relay sessions are hidden). */
export function roleCounts(rooms: { participants: Participant[] | null }[]): Record<Role, number> {
  const n: Record<Role, number> = { presenter: 0, viewer: 0, relay: 0, recorder: 0 };
  for (const r of rooms) for (const p of r.participants ?? []) n[role(p)]++;
  return n;
}

/** Case-insensitive substring match of q against any of the fields (an empty q matches). */
export function matches(q: string, ...fields: (string | undefined)[]): boolean {
  const s = q.trim().toLowerCase();
  return !s || fields.some((f) => f?.toLowerCase().includes(s));
}

/** The most rows a table renders; the rest are counted below it (big nodes have thousands). */
export const MAX_ROWS = 200;

export function table(head: string[], rows: string[], empty = 'Nothing to show.', limit = MAX_ROWS): string {
  if (!rows.length) return `<p class="muted">${esc(empty)}</p>`;
  const more = rows.length > limit ? `<p class="muted more">Showing ${limit} of ${rows.length}.</p>` : '';
  return `<div class="table-wrap"><table><thead><tr>${head.map((h) => `<th scope="col">${h}</th>`).join('')}</tr></thead><tbody>${rows.slice(0, limit).join('')}</tbody></table></div>${more}`;
}

export interface ClusterParticipant { p: Participant; node: string; role: Role }

/** One room across the cluster: presenters (and any viewers) from media nodes, viewers from
 *  edges. An edge's presenters are relayed copies and a media node's relay sessions are
 *  internal, so neither is listed; relays counts the relay sessions per media node. */
export function mergeRoom(parts: NodeRoom[]): { people: ClusterParticipant[]; relays: Record<string, number> } {
  const people: ClusterParticipant[] = [];
  const relays: Record<string, number> = {};
  for (const part of parts) {
    for (const p of part.room?.participants ?? []) {
      const r = role(p);
      if (r === 'recorder') continue;
      if (r === 'relay') { relays[part.node] = (relays[part.node] ?? 0) + 1; continue; }
      if (part.role === 'edge' && r === 'presenter') continue;
      people.push({ p, node: part.node, role: r });
    }
  }
  people.sort((a, b) => ROLE_ORDER[a.role] - ROLE_ORDER[b.role] || cmp(a.p.identity, b.p.identity) || cmp(a.node, b.node));
  return { people, relays };
}

/** Health of a node in the cluster table: down, late heartbeat, draining or alerts. */
export function nodeHealth(n: { down?: boolean; draining?: boolean; seen: number; summary?: { critical: number; warnings: number } }, now: number): { label: string; kind: 'ok' | 'warn' | 'bad' } {
  if (n.down) return { label: 'down', kind: 'bad' };
  if (now - n.seen > 4000) return { label: 'late', kind: 'warn' };
  if (n.draining) return { label: 'draining', kind: 'warn' };
  if (n.summary?.critical) return { label: `${n.summary.critical} critical`, kind: 'bad' };
  if (n.summary?.warnings) return { label: `${n.summary.warnings} warning${n.summary.warnings > 1 ? 's' : ''}`, kind: 'warn' };
  return { label: 'ok', kind: 'ok' };
}

/** decodeURIComponent that keeps a malformed escape as typed instead of throwing. */
export function safeDecode(s: string): string {
  try { return decodeURIComponent(s); } catch { return s; }
}
