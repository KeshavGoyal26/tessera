import { checkSession, login, logout, session } from './api';
import { Cluster, cluster, refreshCluster } from './views/cluster';
import { store } from './store';
import { esc } from './util';
import { render } from './views/view';
import { Connectivity } from './views/connectivity';
import { Overview } from './views/overview';
import { Quality } from './views/quality';
import { Recordings } from './views/recordings';
import { Rooms } from './views/rooms';
import type { View } from './views/view';
import './style.css';

const app = document.getElementById('app')!;
const ROUTES: Record<string, { label: string; make: () => View }> = {
  cluster: { label: 'Cluster', make: () => new Cluster() },
  overview: { label: 'Overview', make: () => new Overview() },
  rooms: { label: 'Rooms', make: () => new Rooms() },
  quality: { label: 'Track quality', make: () => new Quality() },
  connectivity: { label: 'Connectivity', make: () => new Connectivity() },
  recordings: { label: 'Recordings', make: () => new Recordings() },
};

function applyTheme(t: string | null): void {
  const theme = t === 'light' || t === 'dark' ? t : '';
  if (theme) document.documentElement.dataset.theme = theme;
  else delete document.documentElement.dataset.theme;
}
function savedTheme(): string | null { try { return localStorage.getItem('tessera-theme'); } catch { return null; } }
applyTheme(savedTheme());

function toggleTheme(): void {
  const dark = document.documentElement.dataset.theme === 'dark' || (!document.documentElement.dataset.theme && matchMedia('(prefers-color-scheme: dark)').matches);
  const next = dark ? 'light' : 'dark';
  try { localStorage.setItem('tessera-theme', next); } catch { /* storage unavailable */ }
  applyTheme(next);
  current?.view.destroy();
  current = undefined;
  route();
}

let current: { name: string; args: string; view: View } | undefined;
let unsub: (() => void) | undefined;

function route(): void {
  const home = session.cluster ? 'cluster' : 'overview';
  const [name = home, ...args] = location.hash.replace(/^#\/?/, '').split('/');
  const key = ROUTES[name] && (name !== 'cluster' || session.cluster) ? name : home;
  const r = ROUTES[key]!;
  document.querySelectorAll('nav a').forEach((a) => {
    const on = a.getAttribute('href') === `#/${key}`;
    a.classList.toggle('active', on);
    if (on) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
  });
  const argStr = args.join('/');
  if (current && current.name === key && current.args === argStr) return;
  current?.view.destroy();
  const root = document.getElementById('view')!;
  const view = r.make();
  current = { name: key, args: argStr, view };
  view.mount(root, args);
}

function shell(): void {
  app.innerHTML = `<header><div class="brand">Tessera <span class="muted">admin</span></div>
    <nav aria-label="Views">${Object.entries(ROUTES).filter(([k]) => k !== 'cluster' || session.cluster).map(([k, v]) => `<a href="#/${k}">${v.label}</a>`).join('')}</nav>
    ${session.cluster ? '<label class="node-pick">Node <select id="node" aria-label="Node shown by Overview, Rooms, Track quality, Connectivity and Recordings"></select></label>' : ''}
    <div class="spacer"></div><span id="conn" class="conn" role="status" title="live stream"></span>
    <span class="muted user">${esc(session.apiKey)}</span><button id="theme" title="Toggle theme" aria-label="Toggle light or dark theme">◐</button><button id="logout">Log out</button></header>
    <div id="scope" class="scope" hidden></div><main id="view"></main>`;
  document.getElementById('theme')!.onclick = toggleTheme;
  document.getElementById('logout')!.onclick = async () => { store.close(); unsub?.(); await logout(); location.reload(); };
  unsub = store.on(() => {
    const c = document.getElementById('conn');
    if (c) { c.className = 'conn ' + (store.connected ? 'on' : 'off'); c.textContent = store.connected ? 'live' : 'reconnecting…'; }
    // A hidden tab skips the work of rendering (charts, tables) and catches up when shown.
    if (!document.hidden) current?.view.update();
  });
  document.addEventListener('visibilitychange', () => { if (!document.hidden) current?.view.update(); });
  window.addEventListener('hashchange', route);
  if (session.cluster) clusterShell();
  void store.start();
  route();
}

/** Node picker and the banner shown while the node views show another node. */
function clusterShell(): void {
  const pick = document.getElementById('node') as HTMLSelectElement;
  const self = session.cluster!.self;
  const fill = () => {
    const nodes = (cluster.state?.nodes ?? []).filter((n) => !n.down && n.reachable && n.id !== self);
    const opts = [`<option value="">${esc(self)} (this node)</option>`,
      ...nodes.map((n) => `<option value="${esc(n.id)}">${esc(n.id)} · ${esc(n.role)}</option>`)];
    if (store.node && !nodes.some((n) => n.id === store.node)) opts.push(`<option value="${esc(store.node)}">${esc(store.node)} (unreachable)</option>`);
    pick.innerHTML = opts.join('');
    pick.value = store.node;
  };
  cluster.listeners.add(fill);
  pick.onchange = () => { store.setNode(pick.value); if (location.hash.startsWith('#/cluster')) location.hash = '#/overview'; };
  pick.onfocus = () => void refreshCluster();
  const scope = document.getElementById('scope')!;
  store.on(() => {
    document.body.classList.toggle('no-actions', !store.canAct);
    if (pick.value !== store.node) fill();
    scope.hidden = !store.node;
    if (store.node) render(scope, `Showing <b>${esc(store.node)}</b> through this dashboard (refreshed every 2 s)${store.canAct ? '' : ' · actions need an edge\'s dashboard'}. <button data-back>Back to ${esc(self)}</button>`);
  });
  scope.addEventListener('click', (e) => { if ((e.target as HTMLElement).closest('[data-back]')) store.setNode(''); });
  void refreshCluster();
}

function loginForm(error = ''): void {
  app.innerHTML = `<form class="login"><h1>Tessera <span class="muted">admin</span></h1>
    <label>API key<input name="k" autocomplete="username" required autofocus></label>
    <label>Secret<input name="s" type="password" autocomplete="current-password" required></label>
    <button type="submit">Sign in</button><p class="error" role="alert">${esc(error)}</p></form>`;
  app.querySelector('form')!.onsubmit = async (e) => {
    e.preventDefault();
    const f = new FormData(e.target as HTMLFormElement);
    try { await login(String(f.get('k')), String(f.get('s'))); shell(); } catch (err) { loginForm((err as Error).message); }
  };
}

void checkSession().then((ok) => (ok ? shell() : loginForm())).catch((e: Error) => loginForm(e.message));
