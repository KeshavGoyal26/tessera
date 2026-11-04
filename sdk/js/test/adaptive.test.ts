import { describe, expect, it } from "vitest";
import { computeAdaptiveSettings } from "../src/rtc/adaptive.js";
import { StatsSampler, summarizeStats } from "../src/rtc/stats.js";

describe("computeAdaptiveSettings", () => {
  it("disables a track nobody can see", () => {
    expect(computeAdaptiveSettings([])).toEqual({ disabled: true, width: 0, height: 0 });
    expect(computeAdaptiveSettings([{ width: 640, height: 360, visible: false }])).toMatchObject({ disabled: true });
    expect(computeAdaptiveSettings([{ width: 0, height: 0, visible: true }])).toMatchObject({ disabled: true });
  });
  it("requests the largest visible element in device pixels", () => {
    const s = computeAdaptiveSettings(
      [
        { width: 320, height: 180, visible: true },
        { width: 800, height: 450, visible: true },
        { width: 1920, height: 1080, visible: false },
      ],
      2,
    );
    expect(s).toEqual({ disabled: false, width: 1600, height: 900 });
  });
});

describe("stats", () => {
  const base = (ts: number, bytes: number) => [
    { type: "outbound-rtp", id: "o1", timestamp: ts, kind: "video", bytesSent: bytes, packetsSent: 100, framesPerSecond: 30, frameWidth: 1280, frameHeight: 720 },
    { type: "inbound-rtp", id: "i1", timestamp: ts, kind: "video", bytesReceived: bytes * 2, packetsReceived: 90, packetsLost: 10, jitter: 0.012, framesPerSecond: 24, frameWidth: 640, frameHeight: 360 },
    { type: "candidate-pair", id: "cp", timestamp: ts, nominated: true, state: "succeeded", currentRoundTripTime: 0.042, availableOutgoingBitrate: 2_000_000 },
  ];
  it("computes bitrate from two samples, loss, jitter, fps and rtt", () => {
    const prev = new Map();
    const first = summarizeStats(base(1000, 10_000) as never, prev);
    expect(first.send.bitrate).toBe(0);
    const s = summarizeStats(base(2000, 135_000) as never, prev);
    expect(s.send.bitrate).toBe(1_000_000); // 125 kB in 1 s
    expect(s.receive.bitrate).toBe(2_000_000);
    expect(s.receive.lossPercent).toBeCloseTo(10);
    expect(s.receive.jitterMs).toBeCloseTo(12);
    expect(s.send.fps).toBe(30);
    expect(s.receive.width).toBe(640);
    expect(s.rttMs).toBeCloseTo(42);
    expect(s.availableOutgoingBitrate).toBe(2_000_000);
  });
  it("StatsSampler accepts a Map report", () => {
    const sampler = new StatsSampler();
    const m = new Map(base(1000, 5).map((x) => [x.id, x]));
    expect(sampler.sample(m as never).send.packets).toBe(100);
  });
});
