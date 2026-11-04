import { ConnectionQuality } from "../gen/tessera/v1/models_pb.js";
import type { ParticipantInfo } from "../gen/tessera/v1/models_pb.js";
import type { TrackSource } from "../track/Track.js";
import type { TrackPublication } from "../track/TrackPublication.js";

/** A member of a room: the local participant or a remote one. */
export abstract class Participant {
  /** Server-assigned session id (`PA_...`). */
  sid: string;
  /** Identity from the access token; stable across sessions. */
  readonly identity: string;
  name: string;
  metadata: string;
  attributes: Record<string, string>;
  /** Latest audio level, 0 to 1. */
  audioLevel = 0;
  isSpeaking = false;
  connectionQuality: ConnectionQuality = ConnectionQuality.UNSPECIFIED;

  protected constructor(sid: string, identity: string, name = "", metadata = "") {
    this.sid = sid;
    this.identity = identity;
    this.name = name;
    this.metadata = metadata;
    this.attributes = {};
  }

  /** Track publications keyed by track sid. */
  abstract get trackPublications(): ReadonlyMap<string, TrackPublication>;

  /** Returns the first publication with the given source. */
  getTrackPublication(source: TrackSource): TrackPublication | undefined {
    for (const pub of this.trackPublications.values()) {
      if (pub.source === source) return pub;
    }
    return undefined;
  }

  /** @internal Applies server-side participant state. */
  updateInfo(info: ParticipantInfo): void {
    this.sid = info.sid;
    this.name = info.name;
    this.metadata = info.metadata;
    this.attributes = { ...info.attributes };
  }
}
