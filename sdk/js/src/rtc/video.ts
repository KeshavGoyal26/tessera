/** Capture resolution and encoder limits for a video track (one stream per track). */
export interface VideoPreset {
  readonly width: number;
  readonly height: number;
  /** Maximum bitrate in bits per second. */
  readonly maxBitrate: number;
  readonly maxFramerate: number;
}

/** Standard 16:9 video presets. The camera default is `h360`. */
export const VideoPresets = {
  h180: { width: 320, height: 180, maxBitrate: 160_000, maxFramerate: 15 },
  h360: { width: 640, height: 360, maxBitrate: 450_000, maxFramerate: 20 },
  h720: { width: 1280, height: 720, maxBitrate: 1_700_000, maxFramerate: 30 },
  h1080: { width: 1920, height: 1080, maxBitrate: 3_000_000, maxFramerate: 30 },
} as const satisfies Record<string, VideoPreset>;
