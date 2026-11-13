import { clusterPage } from '../api';
import type { ClusterPage } from '../types';
import { esc } from '../util';
import type { Params } from '../util';
import { listParams, PagedList, type ListState, type PagedOptions } from './paged';

export interface ClusterListOptions<T> extends Omit<PagedOptions<T, ClusterPage<T>>, 'fetch' | 'scope'> {
  /** The cluster/<endpoint> the server merges from every node, e.g. 'rooms' or 'participants'. */
  endpoint: string
  /** Extra query parameters (the view's own filters). */
  params?: () => Params
  /** Do not ask until something is typed (searching, not browsing). */
  needsQuery?: boolean
}

/** Nodes that did not answer and nodes that cut their list: shown above any cluster-wide list. */
export function clusterNotes(p: { errors?: string[] | null; truncated?: boolean }): string {
  return (p.truncated ? '<p class="muted more">A node has more than it lists; filter to narrow the result.</p>' : '') +
    (p.errors?.length ? `<p class="error">Some nodes did not answer: ${esc(p.errors.join('; '))}</p>` : '');
}

/** A cluster-wide server-side searched, sorted and paged list (cluster/<endpoint>); the view supplies the columns. */
export function clusterList<T>(o: ClusterListOptions<T>): PagedList<T, ClusterPage<T>> {
  const fetch = (st: ListState): Promise<ClusterPage<T>> => o.needsQuery && !st.q.trim()
    ? Promise.resolve({ total: 0, offset: 0, items: [], errors: null })
    : clusterPage<T>(o.endpoint, { ...listParams(st), ...o.params?.() });
  return new PagedList<T, ClusterPage<T>>({ ...o, fetch, extra: (p) => clusterNotes(p) + (o.extra?.(p) ?? '') });
}
