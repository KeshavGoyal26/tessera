import type { ParticipantInfo } from "../gen/tessera/v1/models_pb.js";
import { RemoteTrackPublication } from "../track/TrackPublication.js";
import { Participant } from "./Participant.js";

/** Another participant in the room. */
export class RemoteParticipant extends Participant {
  readonly #publications = new Map<string, RemoteTrackPublication>();

  constructor(info: ParticipantInfo) {
    super(info.sid, info.identity, info.name, info.metadata);
    this.updateInfo(info);
  }

  get trackPublications(): ReadonlyMap<string, RemoteTrackPublication> {
    return this.#publications;
  }

  /** @internal Applies server-side participant state, including its track list. */
  override updateInfo(info: ParticipantInfo): void {
    super.updateInfo(info);
    const seen = new Set<string>();
    for (const t of info.tracks) {
      seen.add(t.sid);
      if (!this.#publications.has(t.sid)) this.#publications.set(t.sid, new RemoteTrackPublication(t));
    }
    for (const sid of this.#publications.keys()) {
      if (!seen.has(sid)) this.#publications.delete(sid);
    }
  }
}
