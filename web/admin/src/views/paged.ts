import { esc, fmtNum, table } from '../util';
import { filterBox, render } from './view';

export interface ListState { q: string; sort: string; desc: boolean; offset: number; limit: number }
export interface Column {
  label: string
  /** Server-side sort field; without one the header is not clickable. */
  sort?: string
  /** The first click sorts descending (numbers where the biggest matters). */
  desc?: boolean
}
interface PageLike<T> { total: number; offset: number; items: T[] }

export const PAGE_SIZE = 100;
export const REFRESH_MS = 2000;
export const FILTER_MS = 300;

/** Query for a list endpoint from the paging state (empty values are dropped by qs). */
export function listParams(s: ListState): Record<string, string | number | boolean> {
  return { q: s.q.trim(), sort: s.sort, desc: s.desc, offset: s.offset, limit: s.limit };
}

/** State after a click on a column: the same field flips the direction, a new one restarts at page 1. */
export function clickSort(s: ListState, col: Column): ListState {
  if (!col.sort) return s;
  if (col.sort === s.sort) return { ...s, desc: !s.desc, offset: 0 };
  return { ...s, sort: col.sort, desc: !!col.desc, offset: 0 };
}

/** "1–100 of 12,345" (or "0" for an empty set). */
export function pageLabel(offset: number, count: number, total: number): string {
  return count && total ? `${fmtNum(offset + 1)}–${fmtNum(offset + count)} of ${fmtNum(total)}` : '0';
}

/**
 * Calls fetch now and then every ms while the tab is visible. A response is dropped when a newer
 * request was made (sequence number), a request is not started while one is in flight, and a change
 * of scope (the node picker) fetches at once.
 */
export class Poller<T> {
  private seq = 0;
  private busy = false;
  private timer = 0;
  private scope: string;
  constructor(private o: { fetch: () => Promise<T>; onData: (d: T) => void; onError: (e: Error) => void; ms: number; scope?: () => string; onScope?: () => void }) {
    this.scope = o.scope?.() ?? '';
  }
  start(): void {
    void this.run();
    this.timer = window.setInterval(() => { if (!document.hidden && !this.busy) void this.run(); }, this.o.ms);
  }
  stop(): void { window.clearInterval(this.timer); this.seq++; }
  /** Call on every store update: refetches when the scope changed. */
  check(): void {
    const s = this.o.scope?.() ?? '';
    if (s === this.scope) return;
    this.scope = s;
    this.o.onScope?.();
    void this.run();
  }
  async run(): Promise<void> {
    const n = ++this.seq;
    this.busy = true;
    try {
      const d = await this.o.fetch();
      if (n === this.seq) this.o.onData(d);
    } catch (e) {
      if (n === this.seq) this.o.onError(e as Error);
    } finally {
      if (n === this.seq) this.busy = false;
    }
  }
}

export interface PagedOptions<T, P extends PageLike<T>> {
  /** Where the filter box goes. */
  head: HTMLElement
  body: HTMLElement
  filterLabel: string
  columns: Column[]
  sort: string
  desc?: boolean
  fetch: (s: ListState) => Promise<P>
  row: (item: T) => string
  empty: string
  /** Extra html above the table (aggregates), built from the page. */
  extra?: (p: P) => string
  scope?: () => string
}

/** A server-side filtered, sorted and paged table; the view only supplies fetch and row. */
export class PagedList<T, P extends PageLike<T> = PageLike<T>> {
  state: ListState;
  private page?: P;
  private err = '';
  private poller: Poller<P>;
  private debounce = 0;
  private filter: HTMLInputElement;
  private onClick = (e: Event) => this.click(e);

  constructor(private o: PagedOptions<T, P>) {
    this.state = { q: '', sort: o.sort, desc: !!o.desc, offset: 0, limit: PAGE_SIZE };
    this.filter = filterBox(o.head, o.filterLabel, () => {
      window.clearTimeout(this.debounce);
      this.debounce = window.setTimeout(() => { this.state = { ...this.state, q: this.filter.value, offset: 0 }; void this.poller.run(); }, FILTER_MS);
    });
    this.poller = new Poller<P>({
      fetch: () => o.fetch({ ...this.state }),
      ms: REFRESH_MS,
      scope: o.scope,
      onScope: () => { this.state = { ...this.state, offset: 0 }; this.page = undefined; },
      onData: (p) => {
        // The set shrank under the page being viewed: go to the last one.
        if (!p.items.length && p.total > 0 && this.state.offset > 0) {
          this.state = { ...this.state, offset: Math.floor((p.total - 1) / this.state.limit) * this.state.limit };
          void this.poller.run();
          return;
        }
        this.page = p; this.err = ''; this.draw();
      },
      onError: (e) => { this.err = e.message; this.draw(); },
    });
    o.body.addEventListener('click', this.onClick);
    this.draw();
    this.poller.start();
  }

  /** Call from the view's update(): follows the node picker. */
  check(): void { this.poller.check(); }
  destroy(): void { window.clearTimeout(this.debounce); this.poller.stop(); this.o.body.removeEventListener('click', this.onClick); }

  private click(e: Event): void {
    const b = (e.target as HTMLElement).closest<HTMLButtonElement>('button[data-sort], button[data-page]');
    if (!b) return;
    if (b.dataset.sort) this.state = clickSort(this.state, this.o.columns.find((c) => c.sort === b.dataset.sort)!);
    else {
      const d = b.dataset.page === 'next' ? 1 : -1;
      this.state = { ...this.state, offset: Math.max(0, this.state.offset + d * this.state.limit) };
    }
    void this.poller.run();
  }

  private draw(): void {
    const { page, state } = this;
    const err = this.err ? `<p class="error" role="alert">${esc(this.err)}</p>` : '';
    if (!page) { render(this.o.body, err || '<p class="muted">Loading…</p>'); return; }
    const heads = this.o.columns.map((c) => {
      if (!c.sort) return esc(c.label);
      const on = c.sort === state.sort;
      return `<button class="sort" data-sort="${esc(c.sort)}" title="Sort by ${esc(c.label)}">${esc(c.label)}${on ? (state.desc ? ' ▼' : ' ▲') : ''}</button>`;
    });
    // Column labels are html for table(), so the empty-action column stays as given.
    const rows = page.items.map((i) => this.o.row(i));
    const from = page.offset, to = page.offset + page.items.length;
    const pager = page.total > state.limit || from > 0
      ? `<div class="pager"><button data-page="prev"${from <= 0 ? ' disabled' : ''}>‹ Prev</button> <span class="muted">${pageLabel(from, page.items.length, page.total)}</span> <button data-page="next"${to >= page.total ? ' disabled' : ''}>Next ›</button></div>`
      : page.total ? `<p class="muted more">${pageLabel(from, page.items.length, page.total)}</p>` : '';
    render(this.o.body, (this.o.extra?.(page) ?? '') + err + table(heads, rows, state.q ? 'Nothing matches the filter.' : this.o.empty, state.limit) + pager);
  }
}
