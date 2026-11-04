import { EventEmitter, RoomEvent } from "../events.js";
import type { ConnectionQuality, DisconnectReason } from "../gen/tessera/v1/models_pb.js";
import { NotImplementedError } from "../errors.js";
import type { VideoPreset } from "../rtc/simulcast.js";
import type { RemoteTrack } from "../track/RemoteTrack.js";
import type { RemoteTrackPublication, TrackPublication } from "../track/TrackPublication.js";
import { LocalParticipant } from "./LocalParticipant.js";
import type { Participant } from "./Participant.js";
import type { RemoteParticipant } from "./RemoteParticipant.js";

/** State of the room connection. */
export enum ConnectionState {
  Disconnected = "disconnected",
  Connecting = "connecting",
  Connected = "connected",
  Reconnecting = "reconnecting",
}

/** Options for {@link Room} and {@link Room.connect}. */
export interface RoomOptions {
  /** Subscribe to every published track automatically. Default `true`. */
  autoSubscribe?: boolean;
  /**
   * Subscribe to the layer matching each attached element's size and pause tracks whose
   * elements are hidden. Default `true`.
   */
  adaptiveStream?: boolean;
  /** Stop encoding simulcast layers nobody subscribes to. Default `true`. */
  dynacast?: boolean;
  /** Default capture preset for the camera. Default `VideoPresets.h720`. */
  videoPreset?: VideoPreset;
  /** Maximum reconnect attempts before giving up. Default `10`. */
  maxReconnectAttempts?: number;
}

/** Listener signatures of {@link Room} events. */
export interface RoomEventMap {
  [RoomEvent.ParticipantConnected]: (participant: RemoteParticipant) => void;
  [RoomEvent.ParticipantDisconnected]: (participant: RemoteParticipant) => void;
  [RoomEvent.TrackPublished]: (
    publication: RemoteTrackPublication,
    participant: RemoteParticipant,
  ) => void;
  [RoomEvent.TrackUnpublished]: (
    publication: RemoteTrackPublication,
    participant: RemoteParticipant,
  ) => void;
  [RoomEvent.TrackSubscribed]: (
    track: RemoteTrack,
    publication: RemoteTrackPublication,
    participant: RemoteParticipant,
  ) => void;
  [RoomEvent.TrackUnsubscribed]: (
    track: RemoteTrack,
    publication: RemoteTrackPublication,
    participant: RemoteParticipant,
  ) => void;
  [RoomEvent.TrackMuted]: (publication: TrackPublication, participant: Participant) => void;
  [RoomEvent.TrackUnmuted]: (publication: TrackPublication, participant: Participant) => void;
  /** Currently active speakers, loudest first. */
  [RoomEvent.ActiveSpeakersChanged]: (speakers: Participant[]) => void;
  [RoomEvent.ConnectionQualityChanged]: (
    quality: ConnectionQuality,
    participant: Participant,
  ) => void;
  [RoomEvent.Reconnecting]: () => void;
  [RoomEvent.Reconnected]: () => void;
  [RoomEvent.Disconnected]: (reason?: DisconnectReason) => void;
  /** `participant` is undefined for data sent by the server. */
  [RoomEvent.DataReceived]: (
    payload: Uint8Array,
    participant: RemoteParticipant | undefined,
    topic: string | undefined,
  ) => void;
  [RoomEvent.RoomMetadataChanged]: (metadata: string) => void;
}

/**
 * A connection to one Tessera room.
 *
 * @example
 * ```ts
 * const room = await Room.connect("wss://sfu.example.com", token);
 * room.on(RoomEvent.TrackSubscribed, (track) => grid.append(track.attach()));
 * await room.localParticipant.setCameraEnabled(true);
 * ```
 */
export class Room extends EventEmitter<RoomEventMap> {
  readonly options: Readonly<RoomOptions>;
  readonly localParticipant = new LocalParticipant();
  /** Remote participants keyed by identity. */
  readonly remoteParticipants = new Map<string, RemoteParticipant>();
  /** Active speakers, loudest first. */
  activeSpeakers: Participant[] = [];
  /** Room sid, set once connected. */
  sid = "";
  name = "";
  metadata = "";

  #state = ConnectionState.Disconnected;

  constructor(options: RoomOptions = {}) {
    super();
    this.options = { ...options };
  }

  /** Creates a room and connects it. */
  static async connect(url: string, token: string, options?: RoomOptions): Promise<Room> {
    const room = new Room(options);
    await room.connect(url, token);
    return room;
  }

  /** Current connection state. */
  get state(): ConnectionState {
    return this.#state;
  }

  /**
   * Joins the room: opens signaling, negotiates both PeerConnections and resolves once
   * the join response has been processed.
   *
   * @throws {ConnectionError} when the server cannot be reached or rejects the token.
   */
  async connect(_url: string, _token: string): Promise<void> {
    throw new NotImplementedError("Room.connect");
  }

  /** Leaves the room and releases all resources. Safe to call in any state. */
  async disconnect(): Promise<void> {
    if (this.#state === ConnectionState.Disconnected) return;
    this.#state = ConnectionState.Disconnected;
    this.emit(RoomEvent.Disconnected);
  }
}
