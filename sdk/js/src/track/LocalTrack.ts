import { NotImplementedError } from "../errors.js";
import type { VideoPreset } from "../rtc/simulcast.js";
import { Track, TrackSource } from "./Track.js";

/** Options for publishing a local track. */
export interface TrackPublishOptions {
  /** Track name visible to other participants. */
  name?: string;
  /** Publish simulcast layers (video only). Default `true`. */
  simulcast?: boolean;
  /** Encoder limits of the top layer (video only). */
  videoPreset?: VideoPreset;
  /** Preferred codec, e.g. `"video/H264"`; the server's preference wins when unset. */
  videoCodec?: string;
  /** Disable Opus DTX (audio only). */
  dtx?: boolean;
  /** Request Opus RED (audio only). */
  red?: boolean;
  /** Overrides the source detected from the track. */
  source?: TrackSource;
}

/** A track captured on this device. */
export class LocalTrack extends Track {
  constructor(mediaStreamTrack: MediaStreamTrack, source: TrackSource = TrackSource.Unknown) {
    super(mediaStreamTrack, source);
  }

  get isMuted(): boolean {
    return !this.mediaStreamTrack.enabled;
  }

  /** Mutes the track locally and tells the server. */
  async mute(): Promise<void> {
    throw new NotImplementedError("LocalTrack.mute");
  }

  /** Unmutes the track locally and tells the server. */
  async unmute(): Promise<void> {
    throw new NotImplementedError("LocalTrack.unmute");
  }

  /** Stops capture and releases the device. */
  stop(): void {
    this.mediaStreamTrack.stop();
  }
}
