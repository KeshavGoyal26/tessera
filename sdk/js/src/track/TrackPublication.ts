import type { RemoteEngine } from "../room/engine.js";
import type { TrackInfo, VideoQuality } from "../gen/tessera/v1/models_pb.js";
import type { LocalTrack } from "./LocalTrack.js";
import type { RemoteTrack } from "./RemoteTrack.js";
import { trackKindFromProto, trackSourceFromProto } from "./Track.js";
import type { Dimensions, Track, TrackKind, TrackSource } from "./Track.js";

/** A track published to the room, whether or not it is subscribed locally. */
export abstract class TrackPublication {
  readonly trackSid: string;
  readonly kind: TrackKind;
  readonly source: TrackSource;
  trackName: string;
  isMuted: boolean;
  /** Whether the publisher sends simulcast layers. */
  simulcasted: boolean;
  /** Published video dimensions, when known. */
  dimensions: Dimensions | undefined;

  protected constructor(info: TrackInfo) {
    this.trackSid = info.sid;
    this.kind = trackKindFromProto(info.type) ?? "video";
    this.source = trackSourceFromProto(info.source);
    this.trackName = info.name;
    this.isMuted = info.muted;
    this.simulcasted = info.simulcast;
    this.dimensions = info.width > 0 ? { width: info.width, height: info.height } : undefined;
  }

  /** The media track, when available locally. */
  abstract get track(): Track | undefined;
}

/** A track published by the local participant. */
export class LocalTrackPublication extends TrackPublication {
  readonly #track: LocalTrack;

  constructor(info: TrackInfo, track: LocalTrack) {
    super(info);
    this.#track = track;
  }

  get track(): LocalTrack {
    return this.#track;
  }

  /** Mutes the published track. */
  mute(): Promise<void> {
    return this.#track.mute();
  }

  /** Unmutes the published track. */
  unmute(): Promise<void> {
    return this.#track.unmute();
  }
}

/** A track published by a remote participant. */
export class RemoteTrackPublication extends TrackPublication {
  #track: RemoteTrack | undefined;
  #engine: RemoteEngine | undefined;
  #manual = false;
  /** Server-reported forwarding state; `false` while paused for lack of bandwidth. */
  streamActive = true;
  subscribed = true;

  constructor(info: TrackInfo, engine?: RemoteEngine) {
    super(info);
    this.#engine = engine;
  }

  get track(): RemoteTrack | undefined {
    return this.#track;
  }

  /** @internal */
  setTrack(track: RemoteTrack | undefined): void {
    this.#track = track;
  }

  /** True when the track is subscribed and its media has arrived. */
  get isSubscribed(): boolean {
    return this.#track !== undefined;
  }

  /** True once quality or size was set explicitly, which disables adaptive stream for the track. */
  get hasManualSettings(): boolean {
    return this.#manual;
  }

  /** Subscribes to or unsubscribes from the track. */
  setSubscribed(subscribed: boolean): void {
    this.subscribed = subscribed;
    this.#engine?.sendSubscription([this.trackSid], subscribed);
  }

  /** Sets the maximum simulcast layer to receive. Disables adaptive stream sizing for this track. */
  setVideoQuality(quality: VideoQuality): void {
    this.#manual = true;
    this.#track?.disableAdaptiveStream();
    this.#engine?.sendTrackSettings({ trackSids: [this.trackSid], quality, disabled: false });
  }

  /** Tells the server the rendered size so it forwards the closest layer. */
  setVideoDimensions(dimensions: Dimensions): void {
    this.#manual = true;
    this.#track?.disableAdaptiveStream();
    this.#engine?.sendTrackSettings({
      trackSids: [this.trackSid],
      width: Math.ceil(dimensions.width),
      height: Math.ceil(dimensions.height),
      disabled: false,
    });
  }

  /** Pauses (`false`) or resumes (`true`) forwarding without unsubscribing. */
  setEnabled(enabled: boolean): void {
    this.#engine?.sendTrackSettings({ trackSids: [this.trackSid], disabled: !enabled });
  }

  /** @internal Applies server-side track info. */
  updateInfo(info: TrackInfo): void {
    this.isMuted = info.muted;
    this.trackName = info.name;
    this.simulcasted = info.simulcast;
    if (info.width > 0) this.dimensions = { width: info.width, height: info.height };
  }
}
