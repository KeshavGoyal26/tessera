import { NotImplementedError } from "../errors.js";
import type { LocalTrack, TrackPublishOptions } from "../track/LocalTrack.js";
import type { LocalTrackPublication } from "../track/TrackPublication.js";
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

/** The participant on this device. */
export class LocalParticipant extends Participant {
  readonly #publications = new Map<string, LocalTrackPublication>();

  constructor(sid = "", identity = "") {
    super(sid, identity);
  }

  get trackPublications(): ReadonlyMap<string, LocalTrackPublication> {
    return this.#publications;
  }

  /** Publishes a local track, with simulcast for video unless disabled. */
  async publishTrack(
    _track: LocalTrack,
    _options?: TrackPublishOptions,
  ): Promise<LocalTrackPublication> {
    throw new NotImplementedError("LocalParticipant.publishTrack");
  }

  /** Unpublishes a local track; stops it unless `stopOnUnpublish` is `false`. */
  async unpublishTrack(_track: LocalTrack, _stopOnUnpublish = true): Promise<void> {
    throw new NotImplementedError("LocalParticipant.unpublishTrack");
  }

  /** Captures and publishes, or mutes, the microphone. */
  async setMicrophoneEnabled(_enabled: boolean): Promise<LocalTrackPublication | undefined> {
    throw new NotImplementedError("LocalParticipant.setMicrophoneEnabled");
  }

  /** Captures and publishes, or mutes, the camera. */
  async setCameraEnabled(_enabled: boolean): Promise<LocalTrackPublication | undefined> {
    throw new NotImplementedError("LocalParticipant.setCameraEnabled");
  }

  /** Starts or stops screen sharing. */
  async setScreenShareEnabled(_enabled: boolean): Promise<LocalTrackPublication | undefined> {
    throw new NotImplementedError("LocalParticipant.setScreenShareEnabled");
  }

  /** Sends application data to other participants over a data channel. */
  async publishData(_payload: Uint8Array, _options?: DataPublishOptions): Promise<void> {
    throw new NotImplementedError("LocalParticipant.publishData");
  }
}
