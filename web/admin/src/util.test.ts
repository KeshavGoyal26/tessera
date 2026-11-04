import { describe, expect, it } from 'vitest';
import { esc, fmtBps, fmtBytes, fmtDur, percentile } from './util';

describe('util', () => {
  it('escapes HTML', () => expect(esc('<a href="x">&')).toBe('&lt;a href=&quot;x&quot;&gt;&amp;'));
  it('formats bitrates', () => { expect(fmtBps(1500)).toBe('1.5 kbps'); expect(fmtBps(0)).toBe('0 bps'); expect(fmtBps(2_500_000)).toBe('2.5 Mbps'); });
  it('formats bytes', () => expect(fmtBytes(1536)).toBe('1.5 KiB'));
  it('formats durations', () => { expect(fmtDur(5000)).toBe('5s'); expect(fmtDur(125000)).toBe('2m 5s'); expect(fmtDur(3700000)).toBe('1h 1m'); });
  it('computes percentiles', () => { expect(percentile([1, 2, 3, 4, 5], 95)).toBe(5); expect(percentile([], 50)).toBe(0); });
});
