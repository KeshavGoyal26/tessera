import { describe, expect, it } from "vitest";
import { VideoPresets, computeSimulcastEncodings } from "../src/rtc/simulcast.js";

describe("computeSimulcastEncodings", () => {
  it("returns three layers for 720p", () => {
    expect(computeSimulcastEncodings(1280, 720, VideoPresets.h720)).toEqual([
      { rid: "q", active: true, scaleResolutionDownBy: 4, maxBitrate: 170_000, maxFramerate: 15 },
      { rid: "h", active: true, scaleResolutionDownBy: 2, maxBitrate: 510_000, maxFramerate: 30 },
      { rid: "f", active: true, scaleResolutionDownBy: 1, maxBitrate: 1_700_000, maxFramerate: 30 },
    ]);
  });

  it("drops layers below 160px", () => {
    expect(computeSimulcastEncodings(640, 360, VideoPresets.h360).map((e) => e.rid)).toEqual([
      "h",
      "f",
    ]);
    expect(computeSimulcastEncodings(1280, 640, VideoPresets.h720).map((e) => e.rid)).toEqual([
      "q",
      "h",
      "f",
    ]);
    expect(computeSimulcastEncodings(320, 180, VideoPresets.h180).map((e) => e.rid)).toEqual([
      "f",
    ]);
  });

  it("always keeps the full layer for tiny video", () => {
    expect(computeSimulcastEncodings(160, 90, VideoPresets.h180)).toHaveLength(1);
  });

  it("uses the shorter side for portrait video", () => {
    expect(computeSimulcastEncodings(720, 1280, VideoPresets.h720).map((e) => e.rid)).toEqual([
      "q",
      "h",
      "f",
    ]);
    expect(computeSimulcastEncodings(360, 640, VideoPresets.h360).map((e) => e.rid)).toEqual([
      "h",
      "f",
    ]);
  });

  it("caps framerate at the preset", () => {
    const [q, h] = computeSimulcastEncodings(1920, 1080, VideoPresets.h180);
    expect(q?.maxFramerate).toBe(15);
    expect(h?.maxFramerate).toBe(15);
  });

  it("rejects invalid dimensions", () => {
    expect(() => computeSimulcastEncodings(0, 720, VideoPresets.h720)).toThrow(RangeError);
    expect(() => computeSimulcastEncodings(1280, Number.NaN, VideoPresets.h720)).toThrow(RangeError);
  });
});
