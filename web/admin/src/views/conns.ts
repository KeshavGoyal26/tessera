import type { NodeConns } from '../types';
import { badge, esc, fmtBps, fmtNum, table } from '../util';

// A node's connections by kind, where they lead and their quality (Conns in
// pkg/dashboard/cluster.go). Shown on the Overview and, in short, in the cluster tables.

/** "m2 3 · m1 2", largest first; past max nodes "… +N more", the full list on hover. */
export function fmtNodes(m?: Record<string, number>, max = 4): string {
  const all = Object.entries(m ?? {}).sort(([a, x], [b, y]) => y - x || a.localeCompare(b)).map(([k, v]) => `${k} ${fmtNum(v)}`);
  if (all.length <= max) return esc(all.join(' · '));
  return `<span title="${esc(all.join('\n'))}">${esc(all.slice(0, max).join(' · '))} · +${all.length - max} more</span>`;
}

/** "3 via TURN · 1 direct" (connections whose selected ICE path is a TURN relay, the rest). */
export function fmtPath(total: number, turn: number): string {
  if (!total) return '';
  return [turn ? `${fmtNum(turn)} via TURN` : '', total - turn ? `${fmtNum(total - turn)} direct` : ''].filter(Boolean).join(' · ');
}

const QUALITY_ORDER = ['lost', 'poor', 'good', 'excellent', 'unknown'];

/** "1 poor · 5 good" with a badge on lost and poor. */
export function fmtQuality(m?: Record<string, number>): string {
  return QUALITY_ORDER.filter((q) => m?.[q]).map((q) => (q === 'lost' || q === 'poor' ? badge(`${fmtNum(m![q]!)} ${q}`, q === 'lost' ? 'bad' : 'warn') : `${fmtNum(m![q]!)} ${q}`)).join(' · ');
}

/** The Overview's "Connections on this node" table: one row per kind of connection. */
export function connectionsTable(c: NodeConns): string {
  const rows: string[] = [];
  const row = (kind: string, help: string, count: number, path: string, where: string, quality = '') =>
    rows.push(`<tr><td>${kind} <span class="muted">${help}</span></td><td>${fmtNum(count)}</td><td>${path}</td><td>${where}</td><td>${quality}</td></tr>`);
  row('Presenters', 'sending media to this node', c.presenters_in, fmtPath(c.presenters_in, c.presenters_turn), '', fmtQuality(c.presenter_quality));
  row('Viewers', 'receiving media from this node', c.viewers_out, fmtPath(c.viewers_out, c.viewers_turn), '', fmtQuality(c.viewer_quality));
  if (c.pulled || c.pulls_in) row('Pulled presenters', 'copies from media nodes, for this edge\'s viewers', c.pulled, `${fmtNum(c.pulls_in)} pull sessions`, `from ${fmtNodes(c.pulls_from)}`);
  if (c.pulls_out) row('Edge pulls served', 'edges pulling presenters from this node', c.pulls_out, 'pull sessions', `to ${fmtNodes(c.pulls_to)}`);
  if (c.proxied) row('Proxied presenter signaling', 'WebSockets passed through to media nodes', c.proxied, 'signaling only', `to ${fmtNodes(c.proxied_to)}`);
  if (c.turn || c.turn_active) row('TURN relays in use', 'presenter media relayed to media nodes', c.turn_active,
    `${fmtNum(c.turn)} allocations · ${fmtBps(Math.max(c.turn_bps_in, c.turn_bps_out))} relayed`, '<span class="muted">media nodes</span>');
  return table(['Connection', 'Count', 'Path', 'Where to', 'Quality'], rows, '');
}
