import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import type { MessageInitShape } from "@bufbuild/protobuf";
import { ConnectionError, TesseraError } from "../errors.js";
import { EventEmitter, RoomEvent } from "../events.js";
import type { ConnectionQuality } from "../gen/tessera/v1/models_pb.js";
import {
  DisconnectReason,
  ParticipantInfo_State,
  TrackType,
  VideoQuality,
} from "../gen/tessera/v1/models_pb.js";
import type { ParticipantInfo, TrackInfo, VideoLayerSchema } from "../gen/tessera/v1/models_pb.js";
import {
  DataPacketSchema,
  DataPacket_Kind,
  LeaveRequest_Action,
  SignalTarget,
  StreamState,
} from "../gen/tessera/v1/signal_pb.js";
import type {
  ClientConfiguration,
  JoinResponse,
  SignalResponse,
  SubscribedQualityUpdate,
} from "../gen/tessera/v1/signal_pb.js";
import { PCTransport } from "../rtc/PCTransport.js";
import { VideoPresets, computeSimulcastEncodings } from "../rtc/simulcast.js";
import type { VideoPreset } from "../rtc/simulcast.js";
import { msidForMid } from "../rtc/sdp.js";
import { StatsSampler } from "../rtc/stats.js";
import type { StatsSummary } from "../rtc/stats.js";
import { SignalClient } from "../signal/SignalClient.js";
import type { CloseInfo, TrackSettingsInit } from "../signal/SignalClient.js";
import type { LocalTrack } from "../track/LocalTrack.js";
import type { TrackPublishOptions } from "../track/LocalTrack.js";
import { RemoteTrack } from "../track/RemoteTrack.js";
import { TrackSource, trackSourceToProto } from "../track/Track.js";
import { LocalTrackPublication } from "../track/TrackPublication.js";
import type { RemoteTrackPublication, TrackPublication } from "../track/TrackPublication.js";
import type { LocalEngine, RemoteEngine } from "./engine.js";
import { LocalParticipant } from "./LocalParticipant.js";
import type { DataPublishOptions } from "./LocalParticipant.js";
import type { Participant } from "./Participant.js";
import { RemoteParticipant } from "./RemoteParticipant.js";

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
  /** Preferred video codec (`"video/H264"`, `"video/VP8"`). H.264 is preferred while the room records. */
  videoCodec?: string;
  /** Maximum reconnect attempts before giving up. Default `10`. */
  maxReconnectAttempts?: number;
}

/** Replaceable collaborators; tests inject fakes. */
export interface RoomDeps {
  createSignal(): SignalClient;
  createTransport(target: SignalTarget, config: RTCConfiguration): PCTransport;
  sleep(ms: number): Promise<void>;
}

const defaultDeps: RoomDeps = {
  createSignal: () => new SignalClient(),
  createTransport: (t, c) => new PCTransport(t, c),
  sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
};

/** Delay before reconnect attempt `attempt` (0-based): 300 ms doubling up to 5 s. */
export function reconnectDelay(attempt: number): number {
  return Math.min(300 * 2 ** attempt, 5000);
}

/** Listener signatures of {@link Room} events. */
export interface RoomEventMap {
  [RoomEvent.ParticipantConnected]: (participant: RemoteParticipant) => void;
  [RoomEvent.ParticipantDisconnected]: (participant: RemoteParticipant) => void;
  [RoomEvent.TrackPublished]: (publication: RemoteTrackPublication, participant: RemoteParticipant) => void;
  [RoomEvent.TrackUnpublished]: (publication: RemoteTrackPublication, participant: RemoteParticipant) => void;
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
  [RoomEvent.ConnectionQualityChanged]: (quality: ConnectionQuality, participant: Participant) => void;
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
  [RoomEvent.LocalTrackPublished]: (publication: LocalTrackPublication, participant: LocalParticipant) => void;
  [RoomEvent.LocalTrackUnpublished]: (publication: LocalTrackPublication, participant: LocalParticipant) => void;
  /** The server paused (`active=false`) or resumed forwarding of a subscribed track. */
  [RoomEvent.TrackStreamStateChanged]: (
    publication: RemoteTrackPublication,
    active: boolean,
    participant: RemoteParticipant,
  ) => void;
  [RoomEvent.TrackSubscriptionFailed]: (trackSid: string, error: string) => void;
  [RoomEvent.ParticipantMetadataChanged]: (participant: Participant) => void;
  [RoomEvent.RecordingStatusChanged]: (recording: boolean) => void;
  [RoomEvent.ConnectionStateChanged]: (state: ConnectionState) => void;
}

interface LocalEntry {
  track: LocalTrack;
  options: TrackPublishOptions;
  cid: string;
  sid: string;
  stream: MediaStream;
  transceiver: RTCRtpTransceiver | undefined;
  info: TrackInfo | undefined;
}

interface PendingTrack {
  track: MediaStreamTrack;
  receiver: RTCRtpReceiver;
  streamId: string;
}

const RID_QUALITY: Record<string, VideoQuality> = { q: VideoQuality.LOW, h: VideoQuality.MEDIUM, f: VideoQuality.HIGH };

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
export class Room extends EventEmitter<RoomEventMap> implements LocalEngine, RemoteEngine {
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
  /** True while the room is being recorded. */
  isRecording = false;

  #deps: RoomDeps;
  #state = ConnectionState.Disconnected;
  #signal: SignalClient | undefined;
  #pub: PCTransport | undefined;
  #sub: PCTransport | undefined;
  #url = "";
  #token = "";
  #join: JoinResponse | undefined;
  #intentional = false;
  #reconnecting = false;
  #bySid = new Map<string, RemoteParticipant>();
  #pendingTracks = new Map<string, PendingTrack>();
  /** Tracks to publish again after a full rejoin; survives failed rejoin attempts. */
  #republish: { track: LocalTrack; options: TrackPublishOptions }[] | undefined;
  #speaking = new Map<string, number>();
  #waiters = new Map<string, { resolve: (i: TrackInfo) => void; reject: (e: unknown) => void; timer: ReturnType<typeof setTimeout> }>();
  #local = new Map<LocalTrack, LocalEntry>();
  #dc: { reliable: RTCDataChannel; lossy: RTCDataChannel } | undefined;
  #pubSampler = new StatsSampler();
  #subSampler = new StatsSampler();
  #subscribed = new Set<string>();

  constructor(options: RoomOptions = {}, deps: Partial<RoomDeps> = {}) {
    super();
    this.options = { ...options };
    this.#deps = { ...defaultDeps, ...deps };
    this.localParticipant.bind(this);
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

  get videoPreset(): VideoPreset {
    return this.options.videoPreset ?? VideoPresets.h720;
  }

  /**
   * Joins the room: opens signaling and creates both PeerConnections. Resolves once the
   * join response has been processed.
   *
   * @throws {ConnectionError} when the server cannot be reached or rejects the token.
   */
  async connect(url: string, token: string): Promise<void> {
    if (this.#state !== ConnectionState.Disconnected) throw new TesseraError("room is already connected");
    this.#url = url;
    this.#token = token;
    this.#intentional = false;
    this.#setState(ConnectionState.Connecting);
    try {
      await this.#startSession();
    } catch (err) {
      this.#cleanup();
      this.#setState(ConnectionState.Disconnected);
      throw err;
    }
    this.#setState(ConnectionState.Connected);
  }

  /** Leaves the room and releases all resources. Safe to call in any state. */
  async disconnect(): Promise<void> {
    if (this.#state === ConnectionState.Disconnected) return;
    this.#intentional = true;
    try {
      this.#signal?.sendLeave();
    } catch {
      /* socket already gone */
    }
    for (const e of this.#local.values()) e.track.stop();
    this.#teardown(DisconnectReason.CLIENT_INITIATED);
  }

  /** Returns send/receive statistics of both PeerConnections. Call periodically for bitrates. */
  async getStats(): Promise<{ publisher?: StatsSummary; subscriber?: StatsSummary; signalRttMs: number }> {
    const out: { publisher?: StatsSummary; subscriber?: StatsSummary; signalRttMs: number } = {
      signalRttMs: this.#signal?.rtt ?? 0,
    };
    if (this.#pub) out.publisher = this.#pubSampler.sample(await this.#pub.pc.getStats());
    if (this.#sub) out.subscriber = this.#subSampler.sample(await this.#sub.pc.getStats());
    return out;
  }

  // ---- LocalEngine ----

  async publishTrack(track: LocalTrack, options: TrackPublishOptions = {}): Promise<LocalTrackPublication> {
    const pub = this.#pub;
    const signal = this.#signal;
    if (!pub || !signal || this.#state === ConnectionState.Disconnected) throw new TesseraError("not connected to a room");
    if (this.#local.has(track)) throw new TesseraError("track is already published");
    const mst = track.mediaStreamTrack;
    const isVideo = track.kind === "video";
    const source = options.source ?? (track.source !== TrackSource.Unknown ? track.source : isVideo ? TrackSource.Camera : TrackSource.Microphone);
    const preset = options.videoPreset ?? this.videoPreset;
    const settings = mst.getSettings();
    const width = settings.width ?? preset.width;
    const height = settings.height ?? preset.height;
    const simulcast = isVideo && (options.simulcast ?? source === TrackSource.Camera);
    const encodings: RTCRtpEncodingParameters[] | undefined = isVideo
      ? simulcast
        ? computeSimulcastEncodings(width, height, preset)
        : [{ maxBitrate: preset.maxBitrate, maxFramerate: preset.maxFramerate }]
      : undefined;
    const layers: MessageInitShape<typeof VideoLayerSchema>[] = simulcast
      ? (encodings ?? []).map((e) => ({
          rid: e.rid ?? "",
          quality: RID_QUALITY[e.rid ?? "f"] ?? VideoQuality.HIGH,
          width: Math.round(width / (e.scaleResolutionDownBy ?? 1)),
          height: Math.round(height / (e.scaleResolutionDownBy ?? 1)),
          bitrate: e.maxBitrate ?? 0,
        }))
      : [];
    const cid = mst.id;
    const stream = new MediaStream([mst]);
    const entry: LocalEntry = { track, options, cid, sid: "", stream, transceiver: undefined, info: undefined };
    const published = this.#waitPublished(cid);
    signal.sendAddTrack({
      cid,
      name: options.name ?? source,
      type: isVideo ? TrackType.VIDEO : TrackType.AUDIO,
      width: isVideo ? width : 0,
      height: isVideo ? height : 0,
      muted: track.isMuted,
      disableDtx: options.dtx === false,
      red: options.red ?? false,
      source: trackSourceToProto(source),
      layers,
      stream: stream.id,
    });
    const info = await published;
    entry.sid = info.sid;
    entry.info = info;
    const transceiver = pub.pc.addTransceiver(mst, {
      direction: "sendonly",
      streams: [stream],
      ...(encodings ? { sendEncodings: encodings } : {}),
    });
    entry.transceiver = transceiver;
    if (isVideo) this.#preferCodec(transceiver, options.videoCodec);
    this.#local.set(track, entry);
    track.sid = info.sid;
    track.engine = this;
    const publication = new LocalTrackPublication(info, track);
    this.localParticipant.addPublication(publication);
    try {
      await pub.negotiate();
    } catch (err) {
      this.#local.delete(track);
      this.localParticipant.removePublication(info.sid);
      throw err;
    }
    if (track.isMuted) this.#signal?.sendMute(info.sid, true);
    this.emit(RoomEvent.LocalTrackPublished, publication, this.localParticipant);
    return publication;
  }

  async unpublishTrack(track: LocalTrack, stop: boolean): Promise<void> {
    const entry = this.#local.get(track);
    if (!entry) return;
    this.#local.delete(track);
    const publication = this.localParticipant.removePublication(entry.sid);
    try {
      this.#signal?.sendUnpublish(entry.sid);
    } catch {
      /* disconnected */
    }
    if (entry.transceiver && this.#pub && this.#pub.pc.signalingState !== "closed") {
      this.#pub.pc.removeTrack(entry.transceiver.sender);
      await this.#pub.negotiate().catch(() => undefined);
    }
    track.engine = undefined;
    if (stop) track.stop();
    if (publication) this.emit(RoomEvent.LocalTrackUnpublished, publication, this.localParticipant);
  }

  setTrackMuted(track: LocalTrack, muted: boolean): void {
    const entry = this.#local.get(track);
    if (!entry) return;
    try {
      this.#signal?.sendMute(entry.sid, muted);
    } catch {
      /* resent after reconnect */
    }
    const pub = this.localParticipant.trackPublications.get(entry.sid);
    if (pub) {
      pub.isMuted = muted;
      this.emit(muted ? RoomEvent.TrackMuted : RoomEvent.TrackUnmuted, pub, this.localParticipant);
    }
  }

  async replaceTrack(track: LocalTrack, next: MediaStreamTrack): Promise<void> {
    const entry = this.#local.get(track);
    const old = track.mediaStreamTrack;
    if (entry?.transceiver) await entry.transceiver.sender.replaceTrack(next);
    track.replaceMediaStreamTrack(next);
    old.stop();
  }

  async publishData(payload: Uint8Array, options: DataPublishOptions = {}): Promise<void> {
    const reliable = options.reliable ?? true;
    const dc = await this.#ensureDataChannels();
    const channel = reliable ? dc.reliable : dc.lossy;
    if (channel.readyState !== "open") await openOrTimeout(channel);
    const pkt = create(DataPacketSchema, {
      kind: reliable ? DataPacket_Kind.RELIABLE : DataPacket_Kind.LOSSY,
      destinationIdentities: options.destinationIdentities ?? [],
      topic: options.topic ?? "",
      payload,
    });
    channel.send(toBinary(DataPacketSchema, pkt));
  }

  async updateMetadata(update: { name?: string; metadata?: string; attributes?: Record<string, string> }): Promise<void> {
    if (!this.#signal) throw new TesseraError("not connected to a room");
    await this.#signal.updateMetadata(update);
  }

  // ---- RemoteEngine ----

  sendSubscription(trackSids: string[], subscribe: boolean): void {
    this.#signal?.sendSubscription(trackSids, subscribe);
  }

  sendTrackSettings(settings: TrackSettingsInit): void {
    this.#signal?.sendTrackSettings(settings);
  }

  // ---- session ----

  async #startSession(): Promise<void> {
    const signal = this.#deps.createSignal();
    this.#signal = signal;
    signal.onMessage = (m) => this.#onSignal(m);
    signal.onClose = (info) => this.#onSignalClose(info);
    const join = await signal.connect(this.#url, this.#token, { autoSubscribe: this.options.autoSubscribe ?? true });
    this.#join = join;
    signal.startPing(join.pingInterval, join.pingTimeout);
    this.#createTransports(join.iceServers, join.clientConfiguration);
    this.sid = join.room?.sid ?? "";
    this.#applyRoom(join.room);
    if (join.participant) this.localParticipant.updateInfo(join.participant);
    for (const info of join.otherParticipants) this.#upsertRemote(info);
  }

  #createTransports(iceServers: JoinResponse["iceServers"], cfg: ClientConfiguration | undefined): void {
    const config: RTCConfiguration = {
      iceServers: iceServers.map((s) => ({
        urls: s.urls,
        ...(s.username ? { username: s.username, credential: s.credential } : {}),
      })),
      iceTransportPolicy: cfg?.forceRelay ? "relay" : "all",
      bundlePolicy: "max-bundle",
    };
    const pub = this.#deps.createTransport(SignalTarget.PUBLISHER, config);
    const sub = this.#deps.createTransport(SignalTarget.SUBSCRIBER, config);
    this.#pub = pub;
    this.#sub = sub;
    pub.onOffer = (offer, id) => this.#signal?.sendOffer(offer, id);
    pub.onIceCandidate = (c) => this.#trickle(c, SignalTarget.PUBLISHER);
    sub.onIceCandidate = (c) => this.#trickle(c, SignalTarget.SUBSCRIBER);
    const onState = (state: RTCPeerConnectionState) => {
      if (state === "failed" && this.#state === ConnectionState.Connected) void this.#reconnect(false);
    };
    pub.onConnectionState = onState;
    sub.onConnectionState = onState;
    sub.pc.ontrack = (ev) => this.#onTrack(ev);
    sub.pc.ondatachannel = (ev) => {
      ev.channel.binaryType = "arraybuffer";
      ev.channel.onmessage = (m) => this.#onData(m.data as ArrayBuffer);
    };
  }

  #trickle(c: RTCIceCandidateInit | null, target: SignalTarget): void {
    try {
      if (c) this.#signal?.sendTrickle(c, target);
      else this.#signal?.sendTrickle({ candidate: "" }, target, true);
    } catch {
      /* signal down; ICE restart on resume regathers */
    }
  }

  #onSignal(msg: SignalResponse): void {
    const m = msg.message;
    switch (m.case) {
      case "offer": {
        const sub = this.#sub;
        if (!sub) return;
        sub
          .handleOffer({ type: "offer", sdp: m.value.sdp })
          .then((answer) => this.#signal?.sendAnswer(answer, m.value.id))
          .catch((err: unknown) => this.#fail(err));
        return;
      }
      case "answer":
        void this.#pub?.handleAnswer({ type: "answer", sdp: m.value.sdp }, m.value.id).catch((err: unknown) => this.#fail(err));
        return;
      case "trickle": {
        const t = m.value.target === SignalTarget.PUBLISHER ? this.#pub : this.#sub;
        if (!t || !m.value.candidateInit) return;
        try {
          const init = JSON.parse(m.value.candidateInit) as RTCIceCandidateInit;
          if (init.candidate) void t.addIceCandidate(init);
        } catch {
          /* malformed candidate */
        }
        return;
      }
      case "update":
        for (const info of m.value.participants) this.#onParticipantInfo(info);
        return;
      case "trackPublished": {
        const w = this.#waiters.get(m.value.cid);
        if (w && m.value.track) {
          this.#waiters.delete(m.value.cid);
          clearTimeout(w.timer);
          w.resolve(m.value.track);
        }
        return;
      }
      case "trackUnpublished": {
        const entry = [...this.#local.values()].find((e) => e.sid === m.value.trackSid);
        if (entry) void this.unpublishTrack(entry.track, false);
        return;
      }
      case "mute": {
        const entry = [...this.#local.values()].find((e) => e.sid === m.value.sid);
        if (entry) void (m.value.muted ? entry.track.mute() : entry.track.unmute());
        return;
      }
      case "leave":
        this.#onServerLeave(m.value.action, m.value.reason);
        return;
      case "speakersChanged":
        this.#onSpeakers(m.value.speakers);
        return;
      case "roomUpdate":
        this.#applyRoom(m.value.room);
        return;
      case "connectionQuality":
        for (const u of m.value.updates) {
          const p = this.#participantBySid(u.participantSid);
          if (p && p.connectionQuality !== u.quality) {
            p.connectionQuality = u.quality;
            this.emit(RoomEvent.ConnectionQualityChanged, u.quality, p);
          }
        }
        return;
      case "streamStateUpdate":
        for (const s of m.value.streamStates) {
          const p = this.#bySid.get(s.participantSid);
          const pub = p?.trackPublications.get(s.trackSid);
          if (p && pub) {
            pub.streamActive = s.state !== StreamState.PAUSED;
            this.emit(RoomEvent.TrackStreamStateChanged, pub, pub.streamActive, p);
          }
        }
        return;
      case "subscribedQualityUpdate":
        if (this.options.dynacast !== false) void this.#applyDynacast(m.value);
        return;
      case "subscriptionResponse":
        this.emit(RoomEvent.TrackSubscriptionFailed, m.value.trackSid, String(m.value.err));
        return;
      case "refreshToken":
        this.#token = m.value;
        return;
      case "error":
        if (m.value.message) this.emit(RoomEvent.TrackSubscriptionFailed, "", m.value.message);
        return;
      default:
        return;
    }
  }

  #onSignalClose(info: CloseInfo): void {
    if (this.#intentional || this.#state === ConnectionState.Disconnected) return;
    switch (info.code) {
      case 4009:
        this.#teardown(DisconnectReason.DUPLICATE_IDENTITY);
        return;
      case 4003:
        this.#teardown(DisconnectReason.PARTICIPANT_REMOVED);
        return;
      case 4010:
      case 1000:
        this.#teardown(DisconnectReason.UNSPECIFIED);
        return;
      default:
        void this.#reconnect(false);
    }
  }

  #onServerLeave(action: LeaveRequest_Action, reason: DisconnectReason): void {
    if (action === LeaveRequest_Action.RESUME) void this.#reconnect(false);
    else if (action === LeaveRequest_Action.RECONNECT) void this.#reconnect(true);
    else this.#teardown(reason);
  }

  #fail(err: unknown): void {
    if (this.#state === ConnectionState.Connected) void this.#reconnect(false);
    else if (!this.#intentional) console.warn("tessera: negotiation failed", err);
  }

  // ---- participants ----

  #participantBySid(sid: string): Participant | undefined {
    if (sid === this.localParticipant.sid) return this.localParticipant;
    return this.#bySid.get(sid);
  }

  #onParticipantInfo(info: ParticipantInfo): void {
    if (info.identity === this.localParticipant.identity) {
      const before = this.localParticipant.metadata;
      this.localParticipant.updateInfo(info);
      if (before !== info.metadata) this.emit(RoomEvent.ParticipantMetadataChanged, this.localParticipant);
      return;
    }
    if (info.state === ParticipantInfo_State.DISCONNECTED) {
      this.#removeRemote(info.identity);
      return;
    }
    this.#upsertRemote(info);
  }

  #upsertRemote(info: ParticipantInfo): void {
    const existing = this.remoteParticipants.get(info.identity);
    if (!existing) {
      const p = new RemoteParticipant(info, this);
      this.remoteParticipants.set(p.identity, p);
      this.#bySid.set(p.sid, p);
      this.emit(RoomEvent.ParticipantConnected, p);
      for (const pub of p.trackPublications.values()) this.emit(RoomEvent.TrackPublished, pub, p);
    } else {
      const meta = existing.metadata + "\0" + existing.name;
      this.#bySid.delete(existing.sid);
      const { added, removed, changed } = existing.updateInfo(info);
      this.#bySid.set(existing.sid, existing);
      if (meta !== existing.metadata + "\0" + existing.name) this.emit(RoomEvent.ParticipantMetadataChanged, existing);
      for (const pub of added) this.emit(RoomEvent.TrackPublished, pub, existing);
      for (const pub of removed) {
        this.#unsubscribe(pub, existing);
        this.emit(RoomEvent.TrackUnpublished, pub, existing);
      }
      for (const pub of changed) {
        pub.track?.setMuted(pub.isMuted);
        this.emit(pub.isMuted ? RoomEvent.TrackMuted : RoomEvent.TrackUnmuted, pub, existing);
      }
    }
    this.#drainPending();
  }

  #removeRemote(identity: string): void {
    const p = this.remoteParticipants.get(identity);
    if (!p) return;
    this.remoteParticipants.delete(identity);
    this.#bySid.delete(p.sid);
    this.#speaking.delete(p.sid);
    for (const pub of p.trackPublications.values()) {
      this.#unsubscribe(pub, p);
      this.emit(RoomEvent.TrackUnpublished, pub, p);
    }
    this.emit(RoomEvent.ParticipantDisconnected, p);
  }

  #applyRoom(room: JoinResponse["room"]): void {
    if (!room) return;
    this.name = room.name;
    if (room.metadata !== this.metadata) {
      this.metadata = room.metadata;
      this.emit(RoomEvent.RoomMetadataChanged, room.metadata);
    }
    if (room.activeRecording !== this.isRecording) {
      this.isRecording = room.activeRecording;
      this.emit(RoomEvent.RecordingStatusChanged, this.isRecording);
    }
  }

  // ---- subscribing ----

  #onTrack(ev: RTCTrackEvent): void {
    const stream = ev.streams[0];
    // The server's track SID is the msid track id. Chromium copies it into
    // MediaStreamTrack.id, Firefox does not, so read it from the remote SDP.
    const msid = msidForMid(this.#sub?.pc.remoteDescription?.sdp, ev.transceiver?.mid ?? null);
    const sid = msid?.trackId ?? ev.track.id;
    const pending: PendingTrack = { track: ev.track, receiver: ev.receiver, streamId: msid?.streamId ?? stream?.id ?? "" };
    this.#pendingTracks.set(sid, pending);
    stream?.addEventListener("removetrack", (e) => {
      if (e.track !== ev.track) return;
      const p = this.#bySid.get(pending.streamId);
      const pub = p?.trackPublications.get(sid);
      if (p && pub) this.#unsubscribe(pub, p);
      this.#pendingTracks.delete(sid);
    });
    this.#drainPending();
  }

  #drainPending(): void {
    for (const [sid, pt] of this.#pendingTracks) {
      const p = this.#bySid.get(pt.streamId);
      const pub = p?.trackPublications.get(sid);
      if (!p || !pub) continue;
      this.#pendingTracks.delete(sid);
      const rt = new RemoteTrack(pt.track, sid, pub.source);
      rt.receiver = pt.receiver;
      rt.setMuted(pub.isMuted);
      pub.setTrack(rt);
      this.#subscribed.add(sid);
      if (this.options.adaptiveStream !== false && rt.kind === "video") {
        rt.enableAdaptiveStream((s) => {
          if (pub.hasManualSettings) return;
          this.sendTrackSettings({ trackSids: [sid], disabled: s.disabled, width: s.width, height: s.height });
        });
      }
      this.emit(RoomEvent.TrackSubscribed, rt, pub, p);
    }
  }

  #unsubscribe(pub: RemoteTrackPublication, p: RemoteParticipant): void {
    const t = pub.track;
    this.#pendingTracks.delete(pub.trackSid);
    this.#subscribed.delete(pub.trackSid);
    if (!t) return;
    t.disableAdaptiveStream();
    pub.setTrack(undefined);
    this.emit(RoomEvent.TrackUnsubscribed, t, pub, p);
  }

  #onSpeakers(speakers: { participantSid: string; level: number; active: boolean }[]): void {
    for (const s of speakers) {
      const p = this.#participantBySid(s.participantSid);
      if (!p) continue;
      p.audioLevel = s.level;
      p.isSpeaking = s.active;
      if (s.active) this.#speaking.set(s.participantSid, s.level);
      else this.#speaking.delete(s.participantSid);
    }
    this.activeSpeakers = [...this.#speaking.entries()]
      .sort((a, b) => b[1] - a[1])
      .map(([sid]) => this.#participantBySid(sid))
      .filter((p): p is Participant => p !== undefined);
    this.emit(RoomEvent.ActiveSpeakersChanged, this.activeSpeakers);
  }

  // ---- publishing helpers ----

  #waitPublished(cid: string): Promise<TrackInfo> {
    return new Promise<TrackInfo>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.#waiters.delete(cid);
        reject(new ConnectionError("timeout", "server did not confirm the published track"));
      }, 10_000);
      this.#waiters.set(cid, { resolve, reject, timer });
    });
  }

  #preferCodec(tr: RTCRtpTransceiver, requested: string | undefined): void {
    const want =
      requested ??
      this.options.videoCodec ??
      (this.isRecording ? "video/H264" : undefined) ??
      this.#join?.enabledPublishCodecs.find((c) => c.mime.toLowerCase().startsWith("video/"))?.mime;
    if (!want || typeof RTCRtpSender === "undefined" || !tr.setCodecPreferences) return;
    const caps = RTCRtpSender.getCapabilities("video");
    if (!caps) return;
    const w = want.toLowerCase();
    const sorted = [...caps.codecs].sort((a, b) => Number(b.mimeType.toLowerCase() === w) - Number(a.mimeType.toLowerCase() === w));
    try {
      tr.setCodecPreferences(sorted);
    } catch {
      /* the browser rejects the list: keep its default */
    }
  }

  async #applyDynacast(update: SubscribedQualityUpdate): Promise<void> {
    const entry = [...this.#local.values()].find((e) => e.sid === update.trackSid);
    const sender = entry?.transceiver?.sender;
    if (!sender) return;
    const enabled = new Map<string, boolean>();
    for (const q of update.subscribedQualities) {
      const rid = q.quality === VideoQuality.LOW ? "q" : q.quality === VideoQuality.MEDIUM ? "h" : "f";
      enabled.set(rid, q.enabled);
    }
    const params = sender.getParameters();
    let changed = false;
    for (const enc of params.encodings) {
      const on = enabled.get(enc.rid ?? "f");
      if (on !== undefined && enc.active !== on) {
        enc.active = on;
        changed = true;
      }
    }
    if (changed) await sender.setParameters(params).catch(() => undefined);
  }

  async #ensureDataChannels(): Promise<{ reliable: RTCDataChannel; lossy: RTCDataChannel }> {
    if (this.#dc) return this.#dc;
    const pub = this.#pub;
    if (!pub) throw new TesseraError("not connected to a room");
    const reliable = pub.pc.createDataChannel("_reliable", { ordered: true });
    const lossy = pub.pc.createDataChannel("_lossy", { ordered: false, maxRetransmits: 0 });
    this.#dc = { reliable, lossy };
    await pub.negotiate();
    return this.#dc;
  }

  #onData(data: ArrayBuffer): void {
    let pkt;
    try {
      pkt = fromBinary(DataPacketSchema, new Uint8Array(data));
    } catch {
      return;
    }
    const p = pkt.participantIdentity ? this.remoteParticipants.get(pkt.participantIdentity) : undefined;
    this.emit(RoomEvent.DataReceived, pkt.payload, p, pkt.topic || undefined);
  }

  // ---- reconnect ----

  async #reconnect(forceFull: boolean): Promise<void> {
    if (this.#reconnecting || this.#intentional || this.#state === ConnectionState.Disconnected) return;
    this.#reconnecting = true;
    this.#setState(ConnectionState.Reconnecting);
    this.emit(RoomEvent.Reconnecting);
    const max = this.options.maxReconnectAttempts ?? 10;
    let full = forceFull;
    try {
      for (let attempt = 0; attempt < max; attempt++) {
        if (this.#intentional) return;
        try {
          if (full) await this.#fullReconnect();
          else await this.#resume();
          this.#setState(ConnectionState.Connected);
          this.emit(RoomEvent.Reconnected);
          return;
        } catch (err) {
          if (err instanceof ConnectionError) {
            if (err.code === "not_allowed" || err.code === "unsupported_protocol" || err.code === "not_found") break;
            if (err.code === "resume_failed") full = true;
          }
          if (attempt === 1) full = true; // resume keeps failing: rejoin
          await this.#deps.sleep(reconnectDelay(attempt));
        }
      }
      if (!this.#intentional) this.#teardown(DisconnectReason.SIGNAL_TIMEOUT);
    } finally {
      this.#reconnecting = false;
    }
  }

  async #resume(): Promise<void> {
    const old = this.#signal;
    old?.close();
    const signal = this.#deps.createSignal();
    this.#signal = signal;
    signal.onMessage = (m) => this.#onSignal(m);
    signal.onClose = (info) => this.#onSignalClose(info);
    await signal.resume(this.#url, this.#token, { autoSubscribe: this.options.autoSubscribe ?? true, sid: this.localParticipant.sid });
    signal.startPing(this.#join?.pingInterval ?? 5, this.#join?.pingTimeout ?? 15);
    const stale = (t: PCTransport | undefined) => !t || ["failed", "disconnected", "closed"].includes(t.pc.connectionState);
    const restart = stale(this.#pub) || (this.#sub !== undefined && stale(this.#sub) && this.#sub.pc.connectionState !== "new");
    const answer = this.#sub?.lastAnswer;
    signal.sendSyncState({
      ...(answer ? { answer: { type: answer.type, sdp: answer.sdp ?? "", id: 0 } } : {}),
      subscription: { trackSids: [...this.#subscribed], subscribe: true },
      publishTracks: [...this.#local.values()]
        .filter((e) => e.info)
        .map((e) => ({ cid: e.cid, track: e.info })),
      trackSidsSubscribed: [...this.#subscribed],
    });
    if (restart && this.#pub) await this.#pub.negotiate({ iceRestart: true });
    for (const e of this.#local.values()) if (e.track.isMuted) signal.sendMute(e.sid, true);
  }

  async #fullReconnect(): Promise<void> {
    // Keep the list across attempts: a failed attempt has already cleared #local, so a retry
    // that rebuilt it from #local would rejoin without any tracks.
    this.#republish ??= [...this.#local.values()].map((e) => ({ track: e.track, options: e.options }));
    const tracks = this.#republish;
    this.#closeSession();
    for (const t of tracks) {
      t.track.engine = undefined;
      const sid = t.track.sid;
      if (sid) this.localParticipant.removePublication(sid);
    }
    this.#local.clear();
    for (const identity of [...this.remoteParticipants.keys()]) this.#removeRemote(identity);
    await this.#startSession();
    for (const t of tracks) {
      if (t.track.mediaStreamTrack.readyState === "ended") continue;
      await this.publishTrack(t.track, t.options);
    }
    this.#republish = undefined;
  }

  // ---- teardown ----

  #closeSession(): void {
    this.#signal?.close();
    this.#signal = undefined;
    this.#pub?.close();
    this.#sub?.close();
    this.#pub = this.#sub = undefined;
    this.#dc = undefined;
    for (const w of this.#waiters.values()) {
      clearTimeout(w.timer);
      w.reject(new ConnectionError("cancelled", "connection closed"));
    }
    this.#waiters.clear();
  }

  #cleanup(): void {
    this.#republish = undefined;
    this.#closeSession();
    for (const identity of [...this.remoteParticipants.keys()]) this.#removeRemote(identity);
    for (const e of this.#local.values()) e.track.engine = undefined;
    this.#local.clear();
    this.#pendingTracks.clear();
    this.#bySid.clear();
    this.#speaking.clear();
    this.#subscribed.clear();
    this.activeSpeakers = [];
  }

  #teardown(reason: DisconnectReason): void {
    if (this.#state === ConnectionState.Disconnected) return;
    this.#intentional = true;
    this.#cleanup();
    this.#setState(ConnectionState.Disconnected);
    this.emit(RoomEvent.Disconnected, reason);
  }

  #setState(state: ConnectionState): void {
    if (this.#state === state) return;
    this.#state = state;
    this.emit(RoomEvent.ConnectionStateChanged, state);
  }
}

function openOrTimeout(ch: RTCDataChannel): Promise<void> {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new ConnectionError("timeout", "data channel did not open")), 10_000);
    ch.addEventListener("open", () => {
      clearTimeout(t);
      resolve();
    }, { once: true });
  });
}
