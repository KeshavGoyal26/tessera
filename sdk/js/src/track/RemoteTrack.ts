import { AdaptiveStream } from "../rtc/adaptive.js";
import type { AdaptiveSettings } from "../rtc/adaptive.js";
import { Track } from "./Track.js";
import type { TrackSource } from "./Track.js";

/** A track received from another participant. */
export class RemoteTrack extends Track {
  #muted = false;
  #adaptive: AdaptiveStream | undefined;
  /** @internal Receiver, used for stats. */
  receiver: RTCRtpReceiver | undefined;

  constructor(mediaStreamTrack: MediaStreamTrack, sid: string, source: TrackSource) {
    super(mediaStreamTrack, source);
    this.sid = sid;
  }

  get isLocal(): boolean {
    return false;
  }

  /** True when the publisher muted the track. */
  get isMuted(): boolean {
    return this.#muted;
  }

  /** @internal Applies the mute state signalled by the server. */
  setMuted(muted: boolean): void {
    this.#muted = muted;
  }

  /** @internal Enables adaptive stream: `apply` receives size/visibility based settings. */
  enableAdaptiveStream(apply: (s: AdaptiveSettings) => void): void {
    if (this.kind !== "video" || this.#adaptive) return;
    this.#adaptive = new AdaptiveStream(apply);
    for (const el of this.attachedElements) this.#adaptive.observe(el);
  }

  /** @internal Stops adaptive stream. */
  disableAdaptiveStream(): void {
    this.#adaptive?.dispose();
    this.#adaptive = undefined;
  }

  protected override onAttach(element: HTMLMediaElement): void {
    this.#adaptive?.observe(element);
  }

  protected override onDetach(element: HTMLMediaElement): void {
    this.#adaptive?.unobserve(element);
  }
}
