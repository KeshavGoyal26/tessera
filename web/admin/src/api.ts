import type { CoordRecording, Point, Update } from './types';

const base = location.pathname.replace(/[^/]*$/, '') + 'api/';

export const session = { csrf: '', apiKey: '' };

async function req<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (init.method && init.method !== 'GET') headers['X-CSRF-Token'] = session.csrf;
  const res = await fetch(base + path, { credentials: 'same-origin', ...init, headers });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw Object.assign(new Error(body.error ?? res.statusText), { status: res.status });
  return body as T;
}

export async function checkSession(): Promise<boolean> {
  const r = await req<{ authenticated: boolean; csrf?: string; api_key?: string }>('session');
  if (r.authenticated) { session.csrf = r.csrf ?? ''; session.apiKey = r.api_key ?? ''; }
  return r.authenticated;
}
export async function login(api_key: string, secret: string): Promise<void> {
  const r = await req<{ csrf: string; api_key: string }>('login', { method: 'POST', body: JSON.stringify({ api_key, secret }) });
  session.csrf = r.csrf; session.apiKey = r.api_key;
}
export const logout = (): Promise<unknown> => req('logout', { method: 'POST', body: '{}' });
export const history = (): Promise<{ interval_ms: number; points: Point[] }> => req('history');
export const snapshot = (): Promise<Update> => req('snapshot');
export const recordings = (): Promise<{ recordings: CoordRecording[] }> => req('recordings');
export function action(name: string, body: Record<string, unknown>): Promise<unknown> {
  return req(`actions/${name}`, { method: 'POST', body: JSON.stringify(body) });
}

/** Subscribes to the one-second SSE stream; EventSource reconnects by itself. */
export function stream(onUpdate: (u: Update) => void, onState: (connected: boolean) => void): () => void {
  const es = new EventSource(base + 'events', { withCredentials: true });
  es.addEventListener('update', (e) => { onState(true); onUpdate(JSON.parse((e as MessageEvent<string>).data) as Update); });
  es.onerror = () => onState(false);
  return () => es.close();
}
