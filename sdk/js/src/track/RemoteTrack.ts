import { Track } from "./Track.js";
import type { TrackSource } from "./Track.js";

/** A track received from another participant. */
export class RemoteTrack extends Track {
  #muted = false;

  constructor(mediaStreamTrack: MediaStreamTrack, sid: string, source: TrackSource) {
    super(mediaStreamTrack, source);
    this.sid = sid;
  }

  /** True when the publisher muted the track. */
  get isMuted(): boolean {
    return this.#muted;
  }

  /** @internal Applies the mute state signalled by the server. */
  setMuted(muted: boolean): void {
    this.#muted = muted;
  }
}
