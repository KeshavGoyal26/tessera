import type { LocalTrack, TrackPublishOptions } from "../track/LocalTrack.js";
import type { LocalTrackPublication } from "../track/TrackPublication.js";
import type { TrackSettingsInit } from "../signal/SignalClient.js";
import type { VideoPreset } from "../rtc/simulcast.js";
import type { DataPublishOptions } from "./LocalParticipant.js";

/** @internal What a {@link LocalParticipant} needs from its room. */
export interface LocalEngine {
  publishTrack(track: LocalTrack, options?: TrackPublishOptions): Promise<LocalTrackPublication>;
  unpublishTrack(track: LocalTrack, stop: boolean): Promise<void>;
  setTrackMuted(track: LocalTrack, muted: boolean): void;
  publishData(payload: Uint8Array, options?: DataPublishOptions): Promise<void>;
  updateMetadata(update: { name?: string; metadata?: string; attributes?: Record<string, string> }): Promise<void>;
  replaceTrack(track: LocalTrack, next: MediaStreamTrack): Promise<void>;
  readonly videoPreset: VideoPreset;
}

/** @internal What remote publications need from their room. */
export interface RemoteEngine {
  sendSubscription(trackSids: string[], subscribe: boolean): void;
  sendTrackSettings(settings: TrackSettingsInit): void;
}
