import { describe, expect, it } from 'vitest';
import { gradient, levelAt, summary, timelineUrl, type Timeline } from './quality';

const tl: Timeline = {
  step_ms: 4000, final: false,
  samples: [
    { t: 0, q: 0, loss: 0, gap_ms: 0, kbps: 600 }, { t: 4, q: 0, loss: 0, gap_ms: 0, kbps: 600 },
    { t: 8, q: 1, loss: 5, gap_ms: 0, kbps: 500 }, { t: 12, q: 2, loss: 20, gap_ms: 0, kbps: 300 },
    { t: 16, q: 3, loss: 0, gap_ms: 4000, kbps: 0 }, { t: 20, q: 3, loss: 0, gap_ms: 4000, kbps: 0 },
    { t: 24, q: 0, loss: 0, gap_ms: 0, kbps: 600 }, { t: 28, q: 3, loss: 0, gap_ms: 4000, kbps: 0 },
  ],
};

describe('connection timeline', () => {
  it('sits next to the master playlist', () => {
    expect(timelineUrl('/recordings/r/RC_1/master.m3u8')).toBe('/recordings/r/RC_1/quality.json');
    expect(timelineUrl('https://cdn.example/x/master.m3u8?sig=1')).toBe('https://cdn.example/x/quality.json');
  });
  it('gives the level at a position', () => {
    expect(levelAt(tl, 1)).toBe('good');
    expect(levelAt(tl, 9.5)).toBe('fair');
    expect(levelAt(tl, 12)).toBe('poor');
    expect(levelAt(tl, 21)).toBe('lost');
    expect(levelAt(tl, 99)).toBe('unknown');
    expect(levelAt({ step_ms: 4000, final: false, samples: null }, 0)).toBe('unknown');
  });
  it('summarises shares and counts drops (runs of no media)', () => {
    expect(summary(tl)).toBe('Connection: 38% good · 13% unstable · 13% poor · 38% no media (2 drops)');
    expect(summary({ step_ms: 4000, final: false, samples: [] })).toBe('Connection: no data yet');
  });
  it('draws each sample over its share of the bar', () => {
    const g = gradient({ step_ms: 4000, final: true, samples: [{ t: 0, q: 0, loss: 0, gap_ms: 0, kbps: 1 }, { t: 4, q: 2, loss: 20, gap_ms: 0, kbps: 1 }] }, 8);
    expect(g).toContain('var(--q-good) 0.00%, var(--q-good) 50.00%');
    expect(g).toContain('var(--q-poor) 50.00%, var(--q-poor) 100.00%');
    expect(gradient({ step_ms: 4000, final: true, samples: [] }, 8)).toBe('none');
  });
});
