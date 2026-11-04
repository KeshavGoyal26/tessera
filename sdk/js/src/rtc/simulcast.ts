/** Capture resolution and encoder limits for one video quality level. */
export interface VideoPreset {
  readonly width: number;
  readonly height: number;
  /** Maximum bitrate in bits per second. */
  readonly maxBitrate: number;
  readonly maxFramerate: number;
}

/** Standard 16:9 video presets. */
export const VideoPresets = {
  h180: { width: 320, height: 180, maxBitrate: 160_000, maxFramerate: 15 },
  h360: { width: 640, height: 360, maxBitrate: 450_000, maxFramerate: 20 },
  h720: { width: 1280, height: 720, maxBitrate: 1_700_000, maxFramerate: 30 },
  h1080: { width: 1920, height: 1080, maxBitrate: 3_000_000, maxFramerate: 30 },
} as const satisfies Record<string, VideoPreset>;

/** Layers smaller than this (shorter side, in pixels) are not sent. */
export const MIN_SIMULCAST_LAYER_SIZE = 160;

const LAYERS = [
  { rid: "q", scaleResolutionDownBy: 4, bitrateFactor: 0.1, maxFramerate: 15 },
  { rid: "h", scaleResolutionDownBy: 2, bitrateFactor: 0.3, maxFramerate: Infinity },
  { rid: "f", scaleResolutionDownBy: 1, bitrateFactor: 1, maxFramerate: Infinity },
] as const;

/**
 * Computes simulcast encodings for a video track, lowest layer first.
 *
 * Layers use RIDs `q`, `h` and `f` with `scaleResolutionDownBy` 4, 2 and 1. A layer is
 * dropped when its shorter side (the height for landscape video) would be below
 * {@link MIN_SIMULCAST_LAYER_SIZE}; the full-resolution `f` layer is always kept. Bitrates
 * are 10 %, 30 % and 100 % of the preset's `maxBitrate`; the `q` layer is capped at 15 fps.
 *
 * @param width - Capture width in pixels.
 * @param height - Capture height in pixels.
 * @param preset - Limits of the top (`f`) layer.
 * @throws {RangeError} if `width` or `height` is not a positive number.
 */
export function computeSimulcastEncodings(
  width: number,
  height: number,
  preset: VideoPreset,
): RTCRtpEncodingParameters[] {
  if (!(width > 0) || !(height > 0)) {
    throw new RangeError(`invalid video dimensions ${width}x${height}`);
  }
  const shortSide = Math.min(width, height);
  return LAYERS.filter(
    (l) => l.rid === "f" || shortSide / l.scaleResolutionDownBy >= MIN_SIMULCAST_LAYER_SIZE,
  ).map((l) => ({
    rid: l.rid,
    active: true,
    scaleResolutionDownBy: l.scaleResolutionDownBy,
    maxBitrate: Math.round(preset.maxBitrate * l.bitrateFactor),
    maxFramerate: Math.min(preset.maxFramerate, l.maxFramerate),
  }));
}
