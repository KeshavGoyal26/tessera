import { TrackSource as ProtoTrackSource, TrackType } from "../gen/tessera/v1/models_pb.js";

/** Media kind of a track. */
export type TrackKind = "audio" | "video";

/** What a track captures. */
export enum TrackSource {
  Unknown = "unknown",
  Camera = "camera",
  Microphone = "microphone",
  ScreenShare = "screen_share",
  ScreenShareAudio = "screen_share_audio",
}

/** Width and height in pixels. */
export interface Dimensions {
  width: number;
  height: number;
}

/** Maps a protocol track type to a {@link TrackKind}; `undefined` for data or unknown. */
export function trackKindFromProto(type: TrackType): TrackKind | undefined {
  switch (type) {
    case TrackType.AUDIO:
      return "audio";
    case TrackType.VIDEO:
      return "video";
    default:
      return undefined;
  }
}

/** Maps a protocol track source to a {@link TrackSource}. */
export function trackSourceFromProto(source: ProtoTrackSource): TrackSource {
  switch (source) {
    case ProtoTrackSource.CAMERA:
      return TrackSource.Camera;
    case ProtoTrackSource.MICROPHONE:
      return TrackSource.Microphone;
    case ProtoTrackSource.SCREEN_SHARE:
      return TrackSource.ScreenShare;
    case ProtoTrackSource.SCREEN_SHARE_AUDIO:
      return TrackSource.ScreenShareAudio;
    default:
      return TrackSource.Unknown;
  }
}

/** Maps a {@link TrackSource} to its protocol value. */
export function trackSourceToProto(source: TrackSource): ProtoTrackSource {
  switch (source) {
    case TrackSource.Camera:
      return ProtoTrackSource.CAMERA;
    case TrackSource.Microphone:
      return ProtoTrackSource.MICROPHONE;
    case TrackSource.ScreenShare:
      return ProtoTrackSource.SCREEN_SHARE;
    case TrackSource.ScreenShareAudio:
      return ProtoTrackSource.SCREEN_SHARE_AUDIO;
    default:
      return ProtoTrackSource.UNSPECIFIED;
  }
}

/** A local or remote audio/video track backed by a `MediaStreamTrack`. */
export abstract class Track {
  readonly kind: TrackKind;
  readonly source: TrackSource;
  mediaStreamTrack: MediaStreamTrack;
  /** Server-assigned track sid; set once the track is published or subscribed. */
  sid: string | undefined;

  readonly #attached = new Set<HTMLMediaElement>();

  protected constructor(mediaStreamTrack: MediaStreamTrack, source: TrackSource) {
    this.kind = mediaStreamTrack.kind === "audio" ? "audio" : "video";
    this.source = source;
    this.mediaStreamTrack = mediaStreamTrack;
  }

  /** True when the track is muted (by the publisher or the server). */
  abstract get isMuted(): boolean;

  /** Elements this track is currently attached to. */
  get attachedElements(): HTMLMediaElement[] {
    return [...this.#attached];
  }

  /**
   * Renders the track in `element`, creating an `<audio>` or `<video>` element when
   * omitted.
   */
  attach(element?: HTMLMediaElement): HTMLMediaElement {
    const el = element ?? document.createElement(this.kind);
    el.autoplay = true;
    if (el instanceof HTMLVideoElement) el.playsInline = true;
    el.srcObject = new MediaStream([this.mediaStreamTrack]);
    if (this.isLocal) el.muted = true;
    this.#attached.add(el);
    this.onAttach(el);
    return el;
  }

  /** Detaches the track from `element`, or from every element when omitted. */
  detach(element?: HTMLMediaElement): HTMLMediaElement[] {
    const targets = element ? [element] : [...this.#attached];
    for (const el of targets) {
      el.srcObject = null;
      this.#attached.delete(el);
      this.onDetach(el);
    }
    return targets;
  }

  /** @internal Replaces the underlying media track (device switch) and re-renders elements. */
  replaceMediaStreamTrack(next: MediaStreamTrack): void {
    this.mediaStreamTrack = next;
    for (const el of this.#attached) el.srcObject = new MediaStream([next]);
  }

  /** Hook: an element started rendering this track. */
  protected onAttach(_element: HTMLMediaElement): void {}

  /** Hook: an element stopped rendering this track. */
  protected onDetach(_element: HTMLMediaElement): void {}

  /** True for tracks captured on this device. */
  abstract get isLocal(): boolean;
}
