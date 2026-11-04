import { checkSession, login, logout, session } from './api';
import { store } from './store';
import { esc } from './util';
import { Connectivity } from './views/connectivity';
import { Overview } from './views/overview';
import { Quality } from './views/quality';
import { Recordings } from './views/recordings';
import { Rooms } from './views/rooms';
import type { View } from './views/view';
import './style.css';

const app = document.getElementById('app')!;
const ROUTES: Record<string, { label: string; make: () => View }> = {
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
  const [name = 'overview', ...args] = location.hash.replace(/^#\/?/, '').split('/');
  const r = ROUTES[name] ?? ROUTES.overview!;
  const key = ROUTES[name] ? name : 'overview';
  document.querySelectorAll('nav a').forEach((a) => a.classList.toggle('active', a.getAttribute('href') === `#/${key}`));
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
    <nav>${Object.entries(ROUTES).map(([k, v]) => `<a href="#/${k}">${v.label}</a>`).join('')}</nav>
    <div class="spacer"></div><span id="conn" class="conn" title="live stream"></span>
    <span class="muted user">${esc(session.apiKey)}</span><button id="theme" title="Toggle theme">◐</button><button id="logout">Log out</button></header>
    <main id="view"></main>`;
  document.getElementById('theme')!.onclick = toggleTheme;
  document.getElementById('logout')!.onclick = async () => { store.close(); unsub?.(); await logout(); location.reload(); };
  unsub = store.on(() => {
    const c = document.getElementById('conn');
    if (c) { c.className = 'conn ' + (store.connected ? 'on' : 'off'); c.textContent = store.connected ? 'live' : 'reconnecting…'; }
    current?.view.update();
  });
  window.addEventListener('hashchange', route);
  void store.start();
  route();
}

function loginForm(error = ''): void {
  app.innerHTML = `<form class="login"><h1>Tessera <span class="muted">admin</span></h1>
    <label>API key<input name="k" autocomplete="username" required autofocus></label>
    <label>Secret<input name="s" type="password" autocomplete="current-password" required></label>
    <button type="submit">Sign in</button><p class="error">${esc(error)}</p></form>`;
  app.querySelector('form')!.onsubmit = async (e) => {
    e.preventDefault();
    const f = new FormData(e.target as HTMLFormElement);
    try { await login(String(f.get('k')), String(f.get('s'))); shell(); } catch (err) { loginForm((err as Error).message); }
  };
}

void checkSession().then((ok) => (ok ? shell() : loginForm())).catch((e: Error) => loginForm(e.message));
