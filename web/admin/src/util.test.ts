import { describe, expect, it } from 'vitest';
import type { NodeRoom, Participant, Published, Subscribed } from './types';
import { esc, fmtBps, fmtBytes, fmtDur, matches, mergeRoom, nodeHealth, percentile, playUrl, role, pushCap, qs, roleCounts, table } from './util';

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
  it('prefers the master playlist URL the server reports, also while recording', () => {
    expect(playUrl({ ...base, status: 'RECORDING_STATUS_ACTIVE', master_playlist_url: '/recordings/r/RC_1/master.m3u8' })).toBe('/recordings/r/RC_1/master.m3u8');
  });
  it('derives the master from relative per-track URLs (local storage in --dev)', () => {
    expect(playUrl({ ...base, master_playlist_location: '/x/master.m3u8', outputs: [{ track_sid: 'TR_a', playlist_url: '/recordings/r/RC_1/TR_a/index.m3u8' }] }))
      .toBe('/recordings/r/RC_1/master.m3u8');
  });
  it('has nothing to play without URLs', () => {
    expect(playUrl({ ...base, master_playlist_location: '/x/master.m3u8', outputs: [{ track_sid: 'TR_a' }] })).toBeUndefined();
    expect(playUrl(base)).toBeUndefined();
  });
});

const pub = (sid: string, kind: string): Published => ({ sid, kind, source: kind === 'video' ? 'camera' : 'microphone', codec: '', muted: false, ssrc: 1, active: true,
  bitrate_bps: 0, packets_recv: 0, packets_lost: 0, loss_percent: 0, jitter_ms: 0, nacks: 0, plis: 0, firs: 0, subscribers: 0 });
const sub = (track_sid: string, publisher_sid: string, kind: string): Subscribed => ({ track_sid, publisher_sid, kind, codec: '', bandwidth_estimate_bps: 0, bitrate_bps: 0,
  packets_sent: 0, packets_lost: 0, loss_percent: 0, jitter_ms: 0, rtt_ms: 0, restarts: 0, nacks: 0, plis: 0, retransmits: 0, paused: false, muted: false });
const part = (sid: string, identity: string, o: Partial<Participant> = {}): Participant => ({ sid, identity, joined_at: '', state: 'active', kind: 'standard',
  ice_candidate_type: 'host', ice_setup_ms: 0, dtls_setup_ms: 0, rtt_ms: 0, connection_quality: 'good', quality_score: 5, bandwidth_estimate_bps: 0,
  published: [], subscribed: [], ...o });

describe('presenter/viewer model', () => {
  it('derives roles from what a participant does', () => {
    expect(role(part('PA_1', 'p', { published: [pub('TR_v', 'video')] }))).toBe('presenter');
    expect(role(part('PA_2', 'v', { subscribed: [sub('TR_v', 'PA_1', 'video')] }))).toBe('viewer');
    expect(role(part('PA_3', 'v', { published: null, subscribed: null }))).toBe('viewer');
    expect(role(part('PA_4', 'edge-1', { kind: 'relay', subscribed: [sub('TR_v', 'PA_1', 'video')] }))).toBe('relay');
    expect(role(part('PA_5', 'rec', { kind: 'recorder' }))).toBe('recorder');
    expect(roleCounts([{ participants: [part('PA_1', 'p', { published: [pub('TR_v', 'video')] }), part('PA_2', 'v'), part('PA_3', 'v2')] }, { participants: null }]))
      .toEqual({ presenter: 1, viewer: 2, relay: 0, recorder: 0 });
  });
});

describe('lists', () => {
  it('filters case-insensitively', () => {
    expect(matches('', 'x')).toBe(true);
    expect(matches(' Ali ', 'bob', 'ALICE')).toBe(true);
    expect(matches('carol', 'bob', undefined)).toBe(false);
  });
  it('caps tables and says how many rows were left out', () => {
    const rows = Array.from({ length: 5 }, (_, i) => `<tr><td>${i}</td></tr>`);
    const html = table(['n'], rows, 'empty', 3);
    expect(html.match(/<tr><td>/g)).toHaveLength(3);
    expect(html).toContain('Showing 3 of 5.');
    expect(table(['n'], rows)).not.toContain('Showing');
    expect(table(['n'], [], 'No <rooms>.')).toBe('<p class="muted">No &lt;rooms&gt;.</p>');
  });
});

describe('queries', () => {
  it('builds a query string, leaving out empty values', () => {
    expect(qs({})).toBe('');
    expect(qs({ q: '', sort: 'rtt', desc: true, offset: 0, limit: 100, node: undefined, x: false })).toBe('?sort=rtt&desc=1&offset=0&limit=100');
    expect(qs({ q: 'a b&c', node: 'n/1' })).toBe('?q=a%20b%26c&node=n%2F1');
  });
  it('keeps the newest points of a history', () => {
    const a = [1, 2, 3];
    pushCap(a, 4, 3);
    expect(a).toEqual([2, 3, 4]);
  });
});

describe('cluster', () => {
  const part = (identity: string, kind: string, published: boolean, ice = 'host') =>
    ({ sid: `PA_${identity}`, identity, kind, ice_candidate_type: ice, published: published ? [{ sid: `TR_${identity}`, kind: 'video' }] : [], subscribed: [] }) as unknown as Participant;
  it('merges a room: presenters from media nodes, viewers from edges, no relayed copies or relay sessions', () => {
    const parts = [
      { node: 'e1', role: 'edge', room: { participants: [part('alice', 'standard', true, ''), part('v2', 'standard', false), part('v1', 'standard', false)] } },
      { node: 'm1', role: 'media', room: { participants: [part('alice', 'standard', true), part('relay-e1', 'relay', false), part('rec', 'recorder', false)] } },
      { node: 'e2', role: 'edge', error: 'timeout' },
    ] as unknown as NodeRoom[];
    const { people, relays } = mergeRoom(parts);
    expect(people.map((x) => `${x.role}:${x.p.identity}@${x.node}`)).toEqual(['presenter:alice@m1', 'viewer:v1@e1', 'viewer:v2@e1']);
    expect(relays).toEqual({ m1: 1 });
  });
  it('rates node health: down, late heartbeat, draining, alerts, ok', () => {
    const now = 100_000;
    expect(nodeHealth({ down: true, seen: now }, now).label).toBe('down');
    expect(nodeHealth({ seen: now - 5000 }, now).label).toBe('late');
    expect(nodeHealth({ draining: true, seen: now }, now).label).toBe('draining');
    expect(nodeHealth({ seen: now, summary: { critical: 2, warnings: 1 } }, now)).toEqual({ label: '2 critical', kind: 'bad' });
    expect(nodeHealth({ seen: now - 1000, summary: { critical: 0, warnings: 0 } }, now)).toEqual({ label: 'ok', kind: 'ok' });
  });
});
