import { create } from "@bufbuild/protobuf";
import type { MessageInitShape } from "@bufbuild/protobuf";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ConnectionError, TesseraError } from "../src/errors.js";
import { RoomEvent } from "../src/events.js";
import {
  ConnectionQuality,
  DisconnectReason,
  ParticipantInfo_State,
  ParticipantInfoSchema,
  TrackInfoSchema,
  TrackType,
  TrackSource as PTrackSource,
} from "../src/gen/tessera/v1/models_pb.js";
import {
  JoinResponseSchema,
  LeaveRequest_Action,
  ReconnectResponseSchema,
  SignalResponseSchema,
  SignalTarget,
} from "../src/gen/tessera/v1/signal_pb.js";
import type { PCTransport } from "../src/rtc/PCTransport.js";
import { ConnectionState, Room, reconnectDelay } from "../src/room/Room.js";
import { SignalClient } from "../src/signal/SignalClient.js";
import type { SignalRequestBody } from "../src/signal/SignalClient.js";
import { LocalTrack } from "../src/track/LocalTrack.js";
import { TrackSource } from "../src/track/Track.js";

class FakeSignal extends SignalClient {
  static all: FakeSignal[] = [];
  static failResume = 0;
  static failConnect = 0;
  sent: SignalRequestBody[] = [];
  closed = false;
  constructor() {
    super();
    FakeSignal.all.push(this);
  }
  override async connect() {
    if (FakeSignal.failConnect-- > 0) throw new ConnectionError("server_unreachable", "down");
    return create(JoinResponseSchema, {
      room: { sid: "RM_1", name: "demo", metadata: "m0" },
      participant: { sid: "PA_me", identity: "me" },
      otherParticipants: [bob],
      pingInterval: 5,
      pingTimeout: 15,
      enabledPublishCodecs: [{ mime: "video/VP8" }],
    });
  }
  override async resume() {
    if (FakeSignal.failResume-- > 0) throw new ConnectionError("resume_failed", "gone");
    return create(ReconnectResponseSchema, {});
  }
  override startPing() {}
  override send(body: SignalRequestBody) {
    this.sent.push(body);
  }
  override close() {
    this.closed = true;
  }
  emit(m: MessageInitShape<typeof SignalResponseSchema>["message"]) {
    this.onMessage?.(create(SignalResponseSchema, { message: m }));
  }
  cases() {
    return this.sent.map((s) => s?.case);
  }
}

const bob = create(ParticipantInfoSchema, {
  sid: "PA_bob",
  identity: "bob",
  name: "Bob",
  state: ParticipantInfo_State.ACTIVE,
  tracks: [{ sid: "TR_v", type: TrackType.VIDEO, source: PTrackSource.CAMERA, name: "cam" }],
});

function fakeTransport(target: SignalTarget) {
  const t = {
    target,
    pc: {
      connectionState: "connected",
      signalingState: "stable",
      addTransceiver: vi.fn(() => ({ sender: { getParameters: () => ({ encodings: [] }), setParameters: vi.fn() }, setCodecPreferences: vi.fn() })),
      removeTrack: vi.fn(),
      getStats: async () => new Map(),
    },
    negotiate: vi.fn(async () => {}),
    handleOffer: vi.fn(async () => ({ type: "answer" as const, sdp: "A" })),
    handleAnswer: vi.fn(async () => {}),
    addIceCandidate: vi.fn(async () => {}),
    close: vi.fn(),
    lastAnswer: undefined,
    onOffer: undefined,
    onIceCandidate: undefined,
    onConnectionState: undefined,
  };
  return t as unknown as PCTransport & typeof t;
}

let transports: ReturnType<typeof fakeTransport>[] = [];
const sleeps: number[] = [];

function newRoom(opts = {}) {
  return new Room(opts, {
    createSignal: () => new FakeSignal(),
    createTransport: (target) => {
      const t = fakeTransport(target);
      transports.push(t);
      return t;
    },
    sleep: async (ms) => {
      sleeps.push(ms);
    },
  });
}

const sig = () => FakeSignal.all[FakeSignal.all.length - 1]!;

beforeEach(() => {
  FakeSignal.all = [];
  FakeSignal.failResume = 0;
  FakeSignal.failConnect = 0;
  transports = [];
  sleeps.length = 0;
});

describe("Room connect", () => {
  it("joins, exposes participants and emits events", async () => {
    const room = newRoom();
    const seen: string[] = [];
    room.on(RoomEvent.ParticipantConnected, (p) => seen.push(`connected:${p.identity}`));
    room.on(RoomEvent.TrackPublished, (pub) => seen.push(`published:${pub.trackSid}`));
    room.on(RoomEvent.ConnectionStateChanged, (s) => seen.push(s));
    await room.connect("ws://h", "tok");
    expect(room.state).toBe(ConnectionState.Connected);
    expect(room.name).toBe("demo");
    expect(room.metadata).toBe("m0");
    expect(room.localParticipant.identity).toBe("me");
    expect(room.remoteParticipants.get("bob")?.trackPublications.size).toBe(1);
    expect(seen).toEqual(["connecting", "connected:bob", "published:TR_v", "connected"]);
    expect(transports.map((t) => t.target)).toEqual([SignalTarget.PUBLISHER, SignalTarget.SUBSCRIBER]);
  });

  it("cleans up and rethrows when the join fails", async () => {
    FakeSignal.failConnect = 1;
    const room = newRoom();
    await expect(room.connect("ws://h", "t")).rejects.toBeInstanceOf(ConnectionError);
    expect(room.state).toBe(ConnectionState.Disconnected);
    await expect(room.connect("ws://h", "t")).resolves.toBeUndefined();
  });

  it("refuses a second connect", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    await expect(room.connect("ws://h", "t")).rejects.toBeInstanceOf(TesseraError);
  });

  it("disconnect sends leave, closes transports and emits once", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    const events: unknown[] = [];
    room.on(RoomEvent.Disconnected, (r) => events.push(r));
    const s = sig();
    await room.disconnect();
    await room.disconnect();
    expect(s.cases()).toContain("leave");
    expect(transports.every((t) => t.close.mock.calls.length === 1)).toBe(true);
    expect(events).toEqual([DisconnectReason.CLIENT_INITIATED]);
    expect(room.remoteParticipants.size).toBe(0);
  });
});

describe("signal messages", () => {
  it("handles participant updates, track changes and departures", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    const ev: string[] = [];
    room.on(RoomEvent.ParticipantConnected, (p) => ev.push(`join:${p.identity}`));
    room.on(RoomEvent.ParticipantDisconnected, (p) => ev.push(`leave:${p.identity}`));
    room.on(RoomEvent.TrackPublished, (p) => ev.push(`pub:${p.trackSid}`));
    room.on(RoomEvent.TrackUnpublished, (p) => ev.push(`unpub:${p.trackSid}`));
    room.on(RoomEvent.TrackMuted, (p) => ev.push(`mute:${p.trackSid}`));
    const s = sig();

    s.emit({ case: "update", value: { participants: [create(ParticipantInfoSchema, { sid: "PA_c", identity: "carol", state: ParticipantInfo_State.ACTIVE })] } });
    const bobMuted = create(ParticipantInfoSchema, {
      ...bob,
      tracks: [create(TrackInfoSchema, { sid: "TR_v", type: TrackType.VIDEO, muted: true }), create(TrackInfoSchema, { sid: "TR_a", type: TrackType.AUDIO })],
    });
    s.emit({ case: "update", value: { participants: [bobMuted] } });
    s.emit({ case: "update", value: { participants: [create(ParticipantInfoSchema, { ...bob, state: ParticipantInfo_State.DISCONNECTED })] } });
    expect(ev).toEqual(["join:carol", "pub:TR_a", "mute:TR_v", "unpub:TR_v", "unpub:TR_a", "leave:bob"]);
    expect([...room.remoteParticipants.keys()]).toEqual(["carol"]);
  });

  it("tracks active speakers loudest first and clears them", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    const lists: string[][] = [];
    room.on(RoomEvent.ActiveSpeakersChanged, (l) => lists.push(l.map((p) => p.identity)));
    sig().emit({ case: "speakersChanged", value: { speakers: [{ participantSid: "PA_bob", level: 0.4, active: true }, { participantSid: "PA_me", level: 0.9, active: true }] } });
    sig().emit({ case: "speakersChanged", value: { speakers: [{ participantSid: "PA_me", level: 0, active: false }] } });
    expect(lists).toEqual([["me", "bob"], ["bob"]]);
    expect(room.remoteParticipants.get("bob")?.isSpeaking).toBe(true);
    expect(room.localParticipant.isSpeaking).toBe(false);
  });

  it("emits connection quality changes only on change", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    const q: ConnectionQuality[] = [];
    room.on(RoomEvent.ConnectionQualityChanged, (x) => q.push(x));
    const u = { updates: [{ participantSid: "PA_bob", quality: ConnectionQuality.POOR, score: 1 }] };
    sig().emit({ case: "connectionQuality", value: u });
    sig().emit({ case: "connectionQuality", value: u });
    expect(q).toEqual([ConnectionQuality.POOR]);
  });

  it("answers subscriber offers and forwards remote candidates", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    const sub = transports[1]!;
    sig().emit({ case: "offer", value: { type: "offer", sdp: "O", id: 7 } });
    await vi.waitFor(() => expect(sig().cases()).toContain("answer"));
    expect(sub.handleOffer).toHaveBeenCalledWith({ type: "offer", sdp: "O" });
    sig().emit({ case: "trickle", value: { candidateInit: JSON.stringify({ candidate: "candidate:1 1 udp 1 1.2.3.4 5 typ host" }), target: SignalTarget.SUBSCRIBER, final: false } });
    expect(sub.addIceCandidate).toHaveBeenCalledOnce();
  });

  it("updates room metadata and recording state", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    const ev: unknown[] = [];
    room.on(RoomEvent.RoomMetadataChanged, (m) => ev.push(m));
    room.on(RoomEvent.RecordingStatusChanged, (r) => ev.push(r));
    sig().emit({ case: "roomUpdate", value: { room: { name: "demo", metadata: "m1", activeRecording: true } } });
    expect(ev).toEqual(["m1", true]);
    expect(room.isRecording).toBe(true);
  });
});

describe("publishing", () => {
  const mst = (kind: "video" | "audio") =>
    ({ id: `cid-${kind}`, kind, enabled: true, readyState: "live", stop: vi.fn(), getSettings: () => ({ width: 1280, height: 720 }) }) as unknown as MediaStreamTrack;

  beforeEach(() => {
    vi.stubGlobal("MediaStream", class { id = "stream-1"; constructor(public tracks: unknown[] = []) {} });
  });

  it("announces simulcast layers with add_track, then negotiates", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    const s = sig();
    const track = new LocalTrack(mst("video"), TrackSource.Camera);
    const p = room.localParticipant.publishTrack(track);
    const add = s.sent.find((x) => x?.case === "addTrack")!;
    if (add.case !== "addTrack") throw new Error();
    const req = add.value as { cid: string; layers: { rid: string }[]; source: PTrackSource };
    expect(req.cid).toBe("cid-video");
    expect(req.layers.map((l) => l.rid)).toEqual(["q", "h", "f"]);
    expect(req.source).toBe(PTrackSource.CAMERA);
    s.emit({ case: "trackPublished", value: { cid: "cid-video", track: create(TrackInfoSchema, { sid: "TR_new", type: TrackType.VIDEO }) } });
    const pub = await p;
    expect(pub.trackSid).toBe("TR_new");
    const pubT = transports[0]!;
    const enc = (pubT.pc.addTransceiver.mock.calls[0] as unknown[])[1] as { sendEncodings: { rid: string }[] };
    expect(enc.sendEncodings.map((e) => e.rid)).toEqual(["q", "h", "f"]);
    expect(pubT.negotiate).toHaveBeenCalled();
    expect(room.localParticipant.trackPublications.size).toBe(1);
  });

  it("prefers H.264 while the room records and VP8 otherwise", async () => {
    const codecs = [{ mimeType: "video/VP8" }, { mimeType: "video/H264" }, { mimeType: "video/AV1" }];
    vi.stubGlobal("RTCRtpSender", { getCapabilities: () => ({ codecs }) });
    for (const [recording, first] of [[true, "video/H264"], [false, "video/VP8"]] as const) {
      FakeSignal.all = [];
      transports = [];
      const room = newRoom();
      await room.connect("ws://h", "t");
      if (recording) sig().emit({ case: "roomUpdate", value: { room: { name: "demo", activeRecording: true } } });
      const track = new LocalTrack(mst("video"), TrackSource.Camera);
      const p = room.localParticipant.publishTrack(track);
      sig().emit({ case: "trackPublished", value: { cid: "cid-video", track: create(TrackInfoSchema, { sid: "TR_x", type: TrackType.VIDEO }) } });
      await p;
      const tr = transports[0]!.pc.addTransceiver.mock.results[0]!.value as { setCodecPreferences: ReturnType<typeof vi.fn> };
      const prefs = tr.setCodecPreferences.mock.calls[0]![0] as { mimeType: string }[];
      expect(prefs[0]!.mimeType).toBe(first);
    }
  });

  it("sends mute to the server", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    const track = new LocalTrack(mst("audio"), TrackSource.Microphone);
    const p = room.localParticipant.publishTrack(track);
    sig().emit({ case: "trackPublished", value: { cid: "cid-audio", track: create(TrackInfoSchema, { sid: "TR_a", type: TrackType.AUDIO }) } });
    await p;
    await track.mute();
    expect(sig().sent.at(-1)).toEqual({ case: "mute", value: { sid: "TR_a", muted: true } });
    expect(track.mediaStreamTrack.enabled).toBe(false);
  });

  it("rejects publishing when disconnected", async () => {
    const room = newRoom();
    await expect(room.localParticipant.publishTrack(new LocalTrack(mst("audio")))).rejects.toBeInstanceOf(TesseraError);
  });
});

describe("reconnect", () => {
  it("resumes on an unexpected close and emits Reconnecting/Reconnected", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    const ev: string[] = [];
    room.on(RoomEvent.Reconnecting, () => ev.push("reconnecting"));
    room.on(RoomEvent.Reconnected, () => ev.push("reconnected"));
    sig().onClose?.({ code: 1006, reason: "", local: false });
    await vi.waitFor(() => expect(room.state).toBe(ConnectionState.Connected));
    expect(ev).toEqual(["reconnecting", "reconnected"]);
    expect(FakeSignal.all).toHaveLength(2);
    expect(sig().cases()).toContain("syncState");
    expect(transports).toHaveLength(2); // transports are kept on resume
  });

  it("falls back to a full rejoin when resume is impossible", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    const left: string[] = [];
    room.on(RoomEvent.ParticipantDisconnected, (p) => left.push(p.identity));
    FakeSignal.failResume = 1;
    sig().onClose?.({ code: 1006, reason: "", local: false });
    await vi.waitFor(() => expect(room.state).toBe(ConnectionState.Connected));
    expect(transports).toHaveLength(4); // new publisher and subscriber
    expect(left).toEqual(["bob"]);
    expect(room.remoteParticipants.has("bob")).toBe(true); // re-added by the new join
    expect(sleeps).toEqual([reconnectDelay(0)]);
  });

  it("republishes local tracks when an earlier rejoin attempt failed", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    vi.stubGlobal("MediaStream", class { id = "stream-1"; constructor(public tracks: unknown[] = []) {} });
    const audio = { id: "cid-audio", kind: "audio", enabled: true, readyState: "live", stop: vi.fn(), getSettings: () => ({}) } as unknown as MediaStreamTrack;
    const track = new LocalTrack(audio, TrackSource.Microphone);
    const p = room.localParticipant.publishTrack(track);
    sig().emit({ case: "trackPublished", value: { cid: "cid-audio", track: create(TrackInfoSchema, { sid: "TR_a", type: TrackType.AUDIO }) } });
    await p;
    FakeSignal.failResume = 99;
    FakeSignal.failConnect = 1; // first rejoin fails, the second succeeds
    sig().onClose?.({ code: 1006, reason: "", local: false });
    await vi.waitFor(() => expect(sig().cases()).toContain("addTrack"));
    sig().emit({ case: "trackPublished", value: { cid: "cid-audio", track: create(TrackInfoSchema, { sid: "TR_a2", type: TrackType.AUDIO }) } });
    await vi.waitFor(() => expect(room.state).toBe(ConnectionState.Connected));
    expect([...room.localParticipant.trackPublications.keys()]).toEqual(["TR_a2"]);
    FakeSignal.failResume = 0;
  });

  it("gives up after the maximum attempts and disconnects", async () => {
    const room = newRoom({ maxReconnectAttempts: 3 });
    await room.connect("ws://h", "t");
    FakeSignal.failResume = 99;
    FakeSignal.failConnect = 99;
    const reasons: unknown[] = [];
    room.on(RoomEvent.Disconnected, (r) => reasons.push(r));
    sig().onClose?.({ code: 1006, reason: "", local: false });
    await vi.waitFor(() => expect(room.state).toBe(ConnectionState.Disconnected));
    expect(reasons).toEqual([DisconnectReason.SIGNAL_TIMEOUT]);
    expect(sleeps).toEqual([300, 600, 1200]);
  });

  it("does not retry on duplicate identity", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    const reasons: unknown[] = [];
    room.on(RoomEvent.Disconnected, (r) => reasons.push(r));
    sig().onClose?.({ code: 4009, reason: "dup", local: false });
    expect(room.state).toBe(ConnectionState.Disconnected);
    expect(reasons).toEqual([DisconnectReason.DUPLICATE_IDENTITY]);
    expect(FakeSignal.all).toHaveLength(1);
  });

  it("follows a server leave with action RESUME and DISCONNECT", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    sig().emit({ case: "leave", value: { action: LeaveRequest_Action.RESUME, reason: DisconnectReason.UNSPECIFIED, canReconnect: true } });
    await vi.waitFor(() => expect(FakeSignal.all).toHaveLength(2));
    await vi.waitFor(() => expect(room.state).toBe(ConnectionState.Connected));
    sig().emit({ case: "leave", value: { action: LeaveRequest_Action.DISCONNECT, reason: DisconnectReason.ROOM_DELETED, canReconnect: false } });
    expect(room.state).toBe(ConnectionState.Disconnected);
  });

  it("uses a refreshed token for reconnects", async () => {
    const room = newRoom();
    await room.connect("ws://h", "t");
    sig().emit({ case: "refreshToken", value: "fresh" });
    sig().onClose?.({ code: 1006, reason: "", local: false });
    await vi.waitFor(() => expect(room.state).toBe(ConnectionState.Connected));
  });
});

describe("errors", () => {
  it("carries name and code", () => {
    const err = new ConnectionError("not_allowed", "denied", { status: 401 });
    expect(err).toBeInstanceOf(TesseraError);
    expect(err.name).toBe("ConnectionError");
    expect(err.code).toBe("not_allowed");
    expect(err.status).toBe(401);
  });
});

describe("reconnectDelay", () => {
  it("doubles and caps at 5 s", () => {
    expect([0, 1, 2, 3, 4, 5, 9].map(reconnectDelay)).toEqual([300, 600, 1200, 2400, 4800, 5000, 5000]);
  });
});
