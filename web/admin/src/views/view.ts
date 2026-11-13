import { Chart, type ChartDef } from '../charts';

export interface View {
  mount(root: HTMLElement, args: string[]): void;
  update(): void;
  destroy(): void;
}

/** Helper that owns a set of charts fed from a shared time axis. */
export class ChartGroup {
  charts: Chart[] = [];
  constructor(private parent: HTMLElement) {}
  add(def: ChartDef): void { this.charts.push(new Chart(def, this.parent)); }
  update(times: number[], n: number): void { this.charts.forEach((c) => c.update(times, n)); }
  destroy(): void { this.charts.forEach((c) => c.destroy()); this.charts = []; }
}

const rendered = new WeakMap<HTMLElement, string>();
const focusKey = (e: Element): string =>
  `${e.tagName}|${e.getAttribute('href') ?? ''}|${Object.entries((e as HTMLElement).dataset).join(';')}`;

/**
 * Replaces el's content with html for the once-a-second refresh without getting in the
 * operator's way: nothing happens when the html is unchanged or while text inside el is
 * selected (to copy an identity or track id), and keyboard focus returns to the same link
 * or button afterwards.
 */
export function render(el: HTMLElement, html: string): void {
  if (rendered.get(el) === html) return;
  const sel = document.getSelection();
  if (sel && !sel.isCollapsed && sel.anchorNode && el.contains(sel.anchorNode)) return;
  const active = document.activeElement;
  const key = active && active !== el && el.contains(active) ? focusKey(active) : '';
  el.innerHTML = html;
  rendered.set(el, html);
  if (!key) return;
  for (const c of el.querySelectorAll<HTMLElement>('a[href], button')) if (focusKey(c) === key) { c.focus(); break; }
}

/** A search box that keeps its value across refreshes; onInput re-renders the view. */
export function filterBox(parent: HTMLElement, label: string, onInput: () => void): HTMLInputElement {
  const input = document.createElement('input');
  input.type = 'search';
  input.className = 'filter';
  input.placeholder = label;
  input.setAttribute('aria-label', label);
  input.addEventListener('input', onInput);
  parent.appendChild(input);
  return input;
}

/** A select for a list filter ([value, text] options, the first is the default); onChange re-queries. */
export function selectBox(parent: HTMLElement, label: string, options: [string, string][], onChange: () => void): HTMLSelectElement {
  const sel = document.createElement('select');
  sel.setAttribute('aria-label', label);
  sel.title = label;
  sel.innerHTML = options.map(([v, t]) => `<option value="${v}">${t}</option>`).join('');
  sel.addEventListener('change', onChange);
  parent.appendChild(sel);
  return sel;
}
