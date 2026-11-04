import { TesseraError } from "../errors.js";
import type { LocalTrack as LocalTrackType, TrackPublishOptions } from "../track/LocalTrack.js";
import { LocalTrack } from "../track/LocalTrack.js";
import { TrackSource } from "../track/Track.js";
import type { LocalTrackPublication } from "../track/TrackPublication.js";
import type { LocalEngine } from "./engine.js";
import { Participant } from "./Participant.js";

/** Options for {@link LocalParticipant.publishData}. */
export interface DataPublishOptions {
  /** Use the reliable (ordered, retransmitted) channel. Default `true`. */
  reliable?: boolean;
  /** Recipients by identity; everyone when omitted. */
  destinationIdentities?: string[];
  /** Application-defined topic. */
  topic?: string;
}

/** Kinds accepted by {@link LocalParticipant.switchActiveDevice}. */
export type MediaDeviceKind = "audioinput" | "videoinput" | "audiooutput";

/** The participant on this device. */
export class LocalParticipant extends Participant {
  readonly #publications = new Map<string, LocalTrackPublication>();
  #engine: LocalEngine | undefined;
  /** Devices chosen with {@link switchActiveDevice}. */
  readonly activeDevices: Partial<Record<MediaDeviceKind, string>> = {};

  constructor(sid = "", identity = "") {
    super(sid, identity);
  }

  /** @internal */
  bind(engine: LocalEngine | undefined): void {
    this.#engine = engine;
  }

  get trackPublications(): ReadonlyMap<string, LocalTrackPublication> {
    return this.#publications;
  }

  /** @internal */
  addPublication(pub: LocalTrackPublication): void {
    this.#publications.set(pub.trackSid, pub);
  }

  /** @internal */
  removePublication(sid: string): LocalTrackPublication | undefined {
    const p = this.#publications.get(sid);
    this.#publications.delete(sid);
    return p;
  }

  #need(): LocalEngine {
    if (!this.#engine) throw new TesseraError("not connected to a room");
    return this.#engine;
  }

  /** Publishes a local track, with simulcast for camera video unless disabled. */
  publishTrack(track: LocalTrackType, options?: TrackPublishOptions): Promise<LocalTrackPublication> {
    return this.#need().publishTrack(track, options);
  }

  /** Unpublishes a local track; stops it unless `stopOnUnpublish` is `false`. */
  unpublishTrack(track: LocalTrackType, stopOnUnpublish = true): Promise<void> {
    return this.#need().unpublishTrack(track, stopOnUnpublish);
  }

  /** Captures and publishes, or mutes, the microphone. */
  setMicrophoneEnabled(enabled: boolean): Promise<LocalTrackPublication | undefined> {
    return this.#setSourceEnabled(TrackSource.Microphone, enabled);
  }

  /** Captures and publishes, or mutes, the camera. */
  setCameraEnabled(enabled: boolean): Promise<LocalTrackPublication | undefined> {
    return this.#setSourceEnabled(TrackSource.Camera, enabled);
  }

  async #setSourceEnabled(source: TrackSource, enabled: boolean): Promise<LocalTrackPublication | undefined> {
    const engine = this.#need();
    const existing = this.getTrackPublication(source) as LocalTrackPublication | undefined;
    if (existing) {
      if (enabled) await existing.unmute();
      else await existing.mute();
      return existing;
    }
    if (!enabled) return undefined;
    const isCam = source === TrackSource.Camera;
    const preset = engine.videoPreset;
    const constraints: MediaStreamConstraints = isCam
      ? {
          video: {
            width: { ideal: preset.width },
            height: { ideal: preset.height },
            frameRate: { ideal: preset.maxFramerate },
            ...(this.activeDevices.videoinput ? { deviceId: { exact: this.activeDevices.videoinput } } : {}),
          },
        }
      : {
          audio: {
            echoCancellation: true,
            noiseSuppression: true,
            autoGainControl: true,
            ...(this.activeDevices.audioinput ? { deviceId: { exact: this.activeDevices.audioinput } } : {}),
          },
        };
    const stream = await navigator.mediaDevices.getUserMedia(constraints);
    const mst = stream.getTracks()[0];
    if (!mst) throw new TesseraError("no media track captured");
    return engine.publishTrack(new LocalTrack(mst, source), { videoPreset: preset });
  }

  /** Starts or stops screen sharing (video, plus audio when the browser offers it). */
  async setScreenShareEnabled(enabled: boolean): Promise<LocalTrackPublication | undefined> {
    const engine = this.#need();
    const existing = this.getTrackPublication(TrackSource.ScreenShare) as LocalTrackPublication | undefined;
    if (!enabled) {
      if (existing) await this.unpublishTrack(existing.track);
      const audio = this.getTrackPublication(TrackSource.ScreenShareAudio) as LocalTrackPublication | undefined;
      if (audio) await this.unpublishTrack(audio.track);
      return undefined;
    }
    if (existing) return existing;
    const stream = await navigator.mediaDevices.getDisplayMedia({ video: true, audio: true });
    const v = stream.getVideoTracks()[0];
    if (!v) throw new TesseraError("screen capture returned no video");
    const track = new LocalTrack(v, TrackSource.ScreenShare);
    // The browser's "stop sharing" button ends the track.
    v.addEventListener("ended", () => void this.unpublishTrack(track, false).catch(() => undefined));
    const pub = await engine.publishTrack(track, { simulcast: false, name: "screen" });
    const a = stream.getAudioTracks()[0];
    if (a) {
      const at = new LocalTrack(a, TrackSource.ScreenShareAudio);
      await engine.publishTrack(at, { name: "screen_audio", source: TrackSource.ScreenShareAudio });
    }
    return pub;
  }

  /**
   * Switches the capture or output device. For inputs the published track is replaced without
   * renegotiation; for `audiooutput` the sink of all attached elements is changed.
   */
  async switchActiveDevice(kind: MediaDeviceKind, deviceId: string): Promise<void> {
    this.activeDevices[kind] = deviceId;
    if (kind === "audiooutput") {
      for (const el of document.querySelectorAll<HTMLMediaElement & { setSinkId?: (id: string) => Promise<void> }>("audio, video")) {
        await el.setSinkId?.(deviceId);
      }
      return;
    }
    const engine = this.#need();
    const source = kind === "audioinput" ? TrackSource.Microphone : TrackSource.Camera;
    const pub = this.getTrackPublication(source) as LocalTrackPublication | undefined;
    if (!pub) return;
    const stream = await navigator.mediaDevices.getUserMedia(
      kind === "audioinput" ? { audio: { deviceId: { exact: deviceId } } } : { video: { deviceId: { exact: deviceId } } },
    );
    const next = stream.getTracks()[0];
    if (!next) throw new TesseraError("no media track captured");
    next.enabled = !pub.track.isMuted;
    await engine.replaceTrack(pub.track, next);
  }

  /** Sends application data to other participants over a data channel. */
  publishData(payload: Uint8Array, options?: DataPublishOptions): Promise<void> {
    return this.#need().publishData(payload, options);
  }

  /** Updates the display name, metadata and attributes (needs the `can_update_own_metadata` grant). */
  updateInfoOnServer(update: { name?: string; metadata?: string; attributes?: Record<string, string> }): Promise<void> {
    return this.#need().updateMetadata(update);
  }
}
