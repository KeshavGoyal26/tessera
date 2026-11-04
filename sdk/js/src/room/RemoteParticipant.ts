import type { ParticipantInfo } from "../gen/tessera/v1/models_pb.js";
import { RemoteTrackPublication } from "../track/TrackPublication.js";
import type { RemoteEngine } from "./engine.js";
import { Participant } from "./Participant.js";

/** Another participant in the room. */
export class RemoteParticipant extends Participant {
  readonly #publications = new Map<string, RemoteTrackPublication>();
  readonly #engine: RemoteEngine | undefined;

  constructor(info: ParticipantInfo, engine?: RemoteEngine) {
    super(info.sid, info.identity, info.name, info.metadata);
    this.#engine = engine;
    this.updateInfo(info);
  }

  get trackPublications(): ReadonlyMap<string, RemoteTrackPublication> {
    return this.#publications;
  }

  /**
   * @internal Applies server-side participant state, including its track list.
   * @returns the publications added and removed by this update.
   */
  override updateInfo(info: ParticipantInfo): { added: RemoteTrackPublication[]; removed: RemoteTrackPublication[]; changed: RemoteTrackPublication[] } {
    super.updateInfo(info);
    const added: RemoteTrackPublication[] = [];
    const removed: RemoteTrackPublication[] = [];
    const changed: RemoteTrackPublication[] = [];
    const seen = new Set<string>();
    for (const t of info.tracks) {
      seen.add(t.sid);
      const existing = this.#publications.get(t.sid);
      if (!existing) {
        const pub = new RemoteTrackPublication(t, this.#engine);
        this.#publications.set(t.sid, pub);
        added.push(pub);
      } else {
        const wasMuted = existing.isMuted;
        existing.updateInfo(t);
        if (wasMuted !== existing.isMuted) changed.push(existing);
      }
    }
    for (const [sid, pub] of this.#publications) {
      if (!seen.has(sid)) {
        this.#publications.delete(sid);
        removed.push(pub);
      }
    }
    return { added, removed, changed };
  }
}
