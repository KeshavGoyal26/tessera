import { describe, expect, it } from 'vitest';
import { connectionsTable, fmtNodes, fmtPath, fmtQuality } from './conns';
import type { NodeConns } from '../types';

const none: NodeConns = { presenters_in: 0, presenters_turn: 0, viewers_out: 0, viewers_turn: 0, pulled: 0, pulls_in: 0, pulls_out: 0, proxied: 0, turn_active: 0, turn: 0, turn_bps_in: 0, turn_bps_out: 0 };

describe('connections', () => {
  it('formats paths, destinations and quality', () => {
    expect(fmtPath(4, 3)).toBe('3 via TURN · 1 direct');
    expect(fmtPath(2, 0)).toBe('2 direct');
    expect(fmtPath(0, 0)).toBe('');
    expect(fmtNodes({ m2: 3, m1: 2 })).toBe('m1 2 · m2 3');
    expect(fmtQuality({ good: 5, poor: 1 })).toContain('1 poor');
  });
  it('shows an edge its pulls, proxied signaling and TURN, and hides kinds it has none of', () => {
    const html = connectionsTable({ ...none, viewers_out: 1, pulled: 4, pulls_in: 2, pulls_from: { m1: 1, m2: 1 }, proxied: 5, proxied_to: { m1: 2, m2: 3 }, turn_active: 4, turn: 9 });
    expect(html).toContain('Pulled presenters');
    expect(html).toContain('from m1 1 · m2 1');
    expect(html).toContain('to m1 2 · m2 3');
    expect(html).toContain('TURN relays in use');
    expect(html).not.toContain('Edge pulls served');
  });
});
