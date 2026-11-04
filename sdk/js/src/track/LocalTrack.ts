import type { LocalEngine } from "../room/engine.js";
import type { VideoPreset } from "../rtc/simulcast.js";
import { Track, TrackSource } from "./Track.js";

/** Options for publishing a local track. */
export interface TrackPublishOptions {
  /** Track name visible to other participants. */
  name?: string;
  /** Publish simulcast layers (video only). Default `true` for the camera. */
  simulcast?: boolean;
  /** Encoder limits of the top layer (video only). */
  videoPreset?: VideoPreset;
  /** Preferred codec, e.g. `"video/H264"`; the room's preference wins when unset. */
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
  /** @internal Set when published. */
  engine: LocalEngine | undefined;
  #muted = false;

  constructor(mediaStreamTrack: MediaStreamTrack, source: TrackSource = TrackSource.Unknown) {
    super(mediaStreamTrack, source);
  }

  get isLocal(): boolean {
    return true;
  }

  get isMuted(): boolean {
    return this.#muted;
  }

  /** Mutes the track locally and tells the server. */
  async mute(): Promise<void> {
    if (this.#muted) return;
    this.#muted = true;
    this.mediaStreamTrack.enabled = false;
    this.engine?.setTrackMuted(this, true);
  }

  /** Unmutes the track locally and tells the server. */
  async unmute(): Promise<void> {
    if (!this.#muted) return;
    this.#muted = false;
    this.mediaStreamTrack.enabled = true;
    this.engine?.setTrackMuted(this, false);
  }

  /** Stops capture and releases the device. */
  stop(): void {
    this.mediaStreamTrack.stop();
  }
}
