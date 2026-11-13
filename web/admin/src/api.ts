import type { ClusterPoint, ClusterRoom, ClusterState, CoordRecording, Live, NodeRoom, Page, ParticipantDetail, ParticipantPage, Point, RecorderStatus, RoomBrief, TrackBrief, TrackDetail } from './types';
import { qs, type Params } from './util';

const base = location.pathname.replace(/[^/]*$/, '') + 'api/';

/** cluster is set on a cluster node: this node's id and role. */
export const session: { csrf: string; apiKey: string; cluster: { self: string; role: string } | null } = { csrf: '', apiKey: '', cluster: null };

async function req<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (init.method && init.method !== 'GET') headers['X-CSRF-Token'] = session.csrf;
  const res = await fetch(base + path, { credentials: 'same-origin', ...init, headers });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw Object.assign(new Error(body.error ?? res.statusText), { status: res.status });
  return body as T;
}

export async function checkSession(): Promise<boolean> {
  const r = await req<{ authenticated: boolean; csrf?: string; api_key?: string; cluster?: { self: string; role: string } | null }>('session');
  if (r.authenticated) { session.csrf = r.csrf ?? ''; session.apiKey = r.api_key ?? ''; session.cluster = r.cluster ?? null; }
  return r.authenticated;
}
export async function login(api_key: string, secret: string): Promise<void> {
  const r = await req<{ csrf: string; api_key: string; cluster?: { self: string; role: string } | null }>('login', { method: 'POST', body: JSON.stringify({ api_key, secret }) });
  session.csrf = r.csrf; session.apiKey = r.api_key; session.cluster = r.cluster ?? null;
}
export const logout = (): Promise<unknown> => req('logout', { method: 'POST', body: '{}' });
export const history = (): Promise<{ interval_ms: number; points: Point[] }> => req('history');

// Scale-safe endpoints: every one takes `node` (a cluster node id, '' for this node) and lists
// take q, sort, desc, offset and limit; the server filters, sorts and pages.
const get = <T>(path: string, p: Params = {}): Promise<T> => req<T>(path + qs(p));
export const live = (p: Params = {}): Promise<Live> => get('live', p);
export const rooms = (p: Params = {}): Promise<Page<RoomBrief>> => get('rooms', p);
export const participants = (p: Params = {}): Promise<ParticipantPage> => get('participants', p);
export const tracks = (p: Params = {}): Promise<Page<TrackBrief>> => get('tracks', p);
export const recorders = (p: Params = {}): Promise<{ version: number; recorders: RecorderStatus[] }> => get('recorders', p);
/** p: room, sid. */
export const participant = (p: Params): Promise<ParticipantDetail> => get('participant', p);
/** p: sid. */
export const track = (p: Params): Promise<TrackDetail> => get('track', p);
/** Recordings in progress and the newest ended ones; total counts all the coordinator keeps. */
export const recordings = (): Promise<{ recordings: CoordRecording[]; total?: number }> => req('recordings');
// Cluster view: the registry sample (taken every 5 s), its last hour, and pages fetched
// from the nodes only while open (the server caches each for a few seconds).
export const clusterState = (): Promise<ClusterState> => req('cluster');
export const clusterHistory = (): Promise<{ interval_ms: number; points: ClusterPoint[] }> => req('cluster/history');
export const clusterRooms = (): Promise<{ rooms: ClusterRoom[]; errors: string[] | null; truncated: boolean }> => req('cluster/rooms');
export const clusterRoom = (name: string): Promise<{ name: string; nodes: NodeRoom[] }> => req(`cluster/room?name=${encodeURIComponent(name)}`);
export const nodeUpdate = (id: string): Promise<Live> => req(`cluster/node?id=${encodeURIComponent(id)}`);
export const nodeHistory = (id: string): Promise<{ interval_ms: number; points: Point[] }> => req(`cluster/node?id=${encodeURIComponent(id)}&history`);

export function action(name: string, body: Record<string, unknown>): Promise<unknown> {
  return req(`actions/${name}`, { method: 'POST', body: JSON.stringify(body) });
}

/** Subscribes to the one-second SSE stream; EventSource reconnects by itself. */
export function stream(onUpdate: (l: Live) => void, onState: (connected: boolean) => void): () => void {
  const es = new EventSource(base + 'events', { withCredentials: true });
  es.addEventListener('update', (e) => { onState(true); onUpdate(JSON.parse((e as MessageEvent<string>).data) as Live); });
  es.onerror = () => {
    onState(false);
    // A restart or logout ends the session: back to the login form instead of stale data.
    void checkSession().then((ok) => { if (!ok) location.reload(); }).catch(() => undefined);
  };
  return () => es.close();
}
