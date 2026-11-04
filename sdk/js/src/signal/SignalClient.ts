import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import type { MessageInitShape } from "@bufbuild/protobuf";
import { DisconnectReason } from "../gen/tessera/v1/models_pb.js";
import { SignalRequestSchema, SignalResponseSchema } from "../gen/tessera/v1/signal_pb.js";
import type {
  AddTrackRequestSchema,
  JoinResponse,
  SignalRequest,
  SignalResponse,
  SignalTarget,
  UpdateTrackSettingsSchema,
} from "../gen/tessera/v1/signal_pb.js";
import { ConnectionError, NotImplementedError, TesseraError } from "../errors.js";
import { protocolVersion, version } from "../version.js";

/** Options for {@link SignalClient.connect} and {@link buildSignalUrl}. */
export interface SignalConnectOptions {
  /** Subscribe to every published track automatically. */
  autoSubscribe: boolean;
  /** Signaling protocol version; defaults to the SDK's {@link protocolVersion}. */
  protocol?: number;
  /** Resume an existing session instead of joining (requires `sid`). */
  reconnect?: boolean;
  /** Participant sid of the session to resume. */
  sid?: string;
}

/** Init shape of an add-track request, as accepted by `create()`. */
export type AddTrackInit = MessageInitShape<typeof AddTrackRequestSchema>;
/** Init shape of subscriber track settings, as accepted by `create()`. */
export type TrackSettingsInit = MessageInitShape<typeof UpdateTrackSettingsSchema>;
/** Body of a {@link SignalRequest}: its `message` oneof. */
export type SignalRequestBody = MessageInitShape<typeof SignalRequestSchema>["message"];

/**
 * Builds the WebSocket URL of the signaling endpoint (`GET /rtc`).
 *
 * `url` may use `ws:`, `wss:`, `http:` or `https:` (HTTP schemes are mapped to their
 * WebSocket counterparts) and may contain a base path, which is kept.
 *
 * @throws {TesseraError} if `url` is not a valid absolute URL with a supported scheme,
 *   or if `reconnect` is set without `sid`.
 */
export function buildSignalUrl(url: string, token: string, opts: SignalConnectOptions): string {
  let u: URL;
  try {
    u = new URL(url);
  } catch (err) {
    throw new TesseraError(`invalid server URL: ${url}`, { cause: err });
  }
  switch (u.protocol) {
    case "http:":
      u.protocol = "ws:";
      break;
    case "https:":
      u.protocol = "wss:";
      break;
    case "ws:":
    case "wss:":
      break;
    default:
      throw new TesseraError(`unsupported URL scheme: ${u.protocol}`);
  }
  u.pathname = `${u.pathname.replace(/\/+$/, "")}/rtc`;
  u.hash = "";

  const q = u.searchParams;
  q.set("access_token", token);
  q.set("protocol", String(opts.protocol ?? protocolVersion));
  q.set("auto_subscribe", opts.autoSubscribe ? "1" : "0");
  q.set("sdk", "js");
  q.set("version", version);
  if (opts.reconnect) {
    if (!opts.sid) throw new TesseraError("reconnect requires the participant sid");
    q.set("reconnect", "1");
    q.set("sid", opts.sid);
  }
  return u.toString();
}

/** Encodes a client request as one binary WebSocket frame. */
export function encodeRequest(request: SignalRequest): Uint8Array<ArrayBuffer> {
  return toBinary(SignalRequestSchema, request);
}

/** Decodes one binary WebSocket frame sent by the server. */
export function decodeResponse(data: Uint8Array | ArrayBuffer): SignalResponse {
  return fromBinary(SignalResponseSchema, data instanceof Uint8Array ? data : new Uint8Array(data));
}

/**
 * Signaling connection to a Tessera server: one WebSocket carrying binary protobuf
 * frames (ADR-0003).
 */
export class SignalClient {
  /** Called for every message received from the server. */
  onMessage: ((message: SignalResponse) => void) | undefined;
  /** Called when the WebSocket closes, with the close reason if any. */
  onClose: ((reason: string) => void) | undefined;

  #ws: WebSocket | undefined;

  /** True while the WebSocket is open. */
  get isConnected(): boolean {
    return this.#ws?.readyState === WebSocket.OPEN;
  }

  /**
   * Opens the WebSocket and waits for the server's `JoinResponse`.
   *
   * @throws {ConnectionError} when the connection fails or is rejected.
   */
  async connect(url: string, token: string, opts: SignalConnectOptions): Promise<JoinResponse> {
    buildSignalUrl(url, token, opts);
    throw new NotImplementedError("SignalClient.connect");
  }

  /** Sends an offer for the publisher PeerConnection. */
  sendOffer(offer: RTCSessionDescriptionInit, id: number): void {
    this.#send({ case: "offer", value: { type: offer.type, sdp: offer.sdp ?? "", id } });
  }

  /** Sends an answer for the subscriber PeerConnection. */
  sendAnswer(answer: RTCSessionDescriptionInit, id: number): void {
    this.#send({ case: "answer", value: { type: answer.type, sdp: answer.sdp ?? "", id } });
  }

  /** Sends one local ICE candidate, or end-of-candidates when `final` is set. */
  sendTrickle(candidate: RTCIceCandidateInit, target: SignalTarget, final = false): void {
    this.#send({
      case: "trickle",
      value: { candidateInit: JSON.stringify(candidate), target, final },
    });
  }

  /** Announces a track before it is negotiated on the publisher PeerConnection. */
  sendAddTrack(request: AddTrackInit): void {
    this.#send({ case: "addTrack", value: request });
  }

  /** Mutes or unmutes a published track. */
  sendMute(trackSid: string, muted: boolean): void {
    this.#send({ case: "mute", value: { sid: trackSid, muted } });
  }

  /** Subscribes to or unsubscribes from tracks. */
  sendSubscription(trackSids: string[], subscribe: boolean): void {
    this.#send({ case: "subscription", value: { trackSids, subscribe } });
  }

  /** Updates subscriber preferences (quality, size, pause) for subscribed tracks. */
  sendTrackSettings(settings: TrackSettingsInit): void {
    this.#send({ case: "trackSetting", value: settings });
  }

  /** Tells the server this participant is leaving. */
  sendLeave(reason: DisconnectReason = DisconnectReason.CLIENT_INITIATED): void {
    this.#send({ case: "leave", value: { reason } });
  }

  /** Sends a keep-alive ping carrying the last measured round-trip time. */
  sendPing(rttMs = 0): void {
    this.#send({
      case: "ping",
      value: { timestamp: BigInt(Date.now()), rtt: BigInt(Math.round(rttMs)) },
    });
  }

  /** Closes the WebSocket without sending a leave request. */
  close(): void {
    this.#ws?.close();
    this.#ws = undefined;
  }

  #send(message: SignalRequestBody): void {
    const ws = this.#ws;
    if (ws?.readyState !== WebSocket.OPEN) {
      throw new ConnectionError("not_connected", "signal connection is not open");
    }
    ws.send(encodeRequest(create(SignalRequestSchema, { message })));
  }
}
