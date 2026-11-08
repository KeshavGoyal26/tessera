import type { LocalEngine } from "../room/engine.js";
import type { VideoPreset } from "../rtc/video.js";
import { Track, TrackSource } from "./Track.js";

/** Options for publishing a local track. */
export interface TrackPublishOptions {
  /** Track name visible to other participants. */
  name?: string;
  /** Encoder limits (video only). One stream per track: no simulcast. */
  videoPreset?: VideoPreset;
  /** Disable Opus DTX (audio only). */
  dtx?: boolean;
  /** Send Opus as RED (redundant audio) where the browser supports it (audio only). Default true. */
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
