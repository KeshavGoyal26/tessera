import { describe, expect, it } from 'vitest';
import { esc, fmtBps, fmtBytes, fmtDur, percentile, playUrl } from './util';

describe('util', () => {
  it('escapes HTML', () => expect(esc('<a href="x">&')).toBe('&lt;a href=&quot;x&quot;&gt;&amp;'));
  it('formats bitrates', () => { expect(fmtBps(1500)).toBe('1.5 kbps'); expect(fmtBps(0)).toBe('0 bps'); expect(fmtBps(2_500_000)).toBe('2.5 Mbps'); });
  it('formats bytes', () => expect(fmtBytes(1536)).toBe('1.5 KiB'));
  it('formats durations', () => { expect(fmtDur(5000)).toBe('5s'); expect(fmtDur(125000)).toBe('2m 5s'); expect(fmtDur(3700000)).toBe('1h 1m'); });
  it('computes percentiles', () => { expect(percentile([1, 2, 3, 4, 5], 95)).toBe(5); expect(percentile([], 50)).toBe(0); });
});

describe('playUrl', () => {
  const base = { recording_id: 'RC_1', room: 'r', status: 'RECORDING_STATUS_COMPLETE' };
  const out = (sid: string) => ({ track_sid: sid, playlist_url: `http://h/rec/r/RC_1/${sid}/index.m3u8` });
  it('plays the master playlist when one was written', () => {
    expect(playUrl({ ...base, master_playlist_location: '/x/master.m3u8', outputs: [out('TR_a'), out('TR_b')] })).toBe('http://h/rec/r/RC_1/master.m3u8');
  });
  it('falls back to the only playable track', () => {
    expect(playUrl({ ...base, outputs: [out('TR_a'), { track_sid: 'TR_b' }] })).toBe('http://h/rec/r/RC_1/TR_a/index.m3u8');
  });
  it('has nothing to play without URLs', () => {
    expect(playUrl({ ...base, master_playlist_location: '/x/master.m3u8', outputs: [{ track_sid: 'TR_a' }] })).toBeUndefined();
    expect(playUrl(base)).toBeUndefined();
  });
});
