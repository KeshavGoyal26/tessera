import { describe, expect, it } from 'vitest';
import { fmtNum, qs } from './util';
import { clickSort, listParams, pageLabel, type ListState } from './views/paged';

const st: ListState = { q: '', sort: 'quality', desc: false, offset: 200, limit: 100 };

describe('paged list', () => {
  it('maps the state to the query', () => {
    expect(qs({ ...listParams(st), node: 'edge-1' })).toBe('?sort=quality&offset=200&limit=100&node=edge-1');
    expect(qs(listParams({ ...st, q: ' alice ', desc: true, offset: 0 }))).toBe('?q=alice&sort=quality&desc=1&offset=0&limit=100');
  });
  it('sorts by clicking a header: same field flips, new field restarts at page 1', () => {
    expect(clickSort(st, { label: 'Quality', sort: 'quality' })).toMatchObject({ sort: 'quality', desc: true, offset: 0 });
    expect(clickSort(st, { label: 'RTT', sort: 'rtt', desc: true })).toMatchObject({ sort: 'rtt', desc: true, offset: 0 });
    expect(clickSort(st, { label: 'Room', sort: 'room' })).toMatchObject({ sort: 'room', desc: false, offset: 0 });
    expect(clickSort(st, { label: 'Role' })).toBe(st);
  });
  it('labels the page', () => {
    expect(pageLabel(0, 100, 12345)).toBe(`1–100 of ${fmtNum(12345)}`);
    expect(pageLabel(100, 45, 145)).toBe('101–145 of 145');
    expect(pageLabel(0, 0, 0)).toBe('0');
  });
});
