import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import type { MessageInitShape } from "@bufbuild/protobuf";
import { DisconnectReason } from "../gen/tessera/v1/models_pb.js";
import { SignalRequestSchema, SignalResponseSchema } from "../gen/tessera/v1/signal_pb.js";
import { RequestResponse_Reason } from "../gen/tessera/v1/signal_pb.js";
import type {
  AddTrackRequestSchema,
  JoinResponse,
  ReconnectResponse,
  SignalRequest,
  SignalResponse,
  SignalTarget,
  UpdateTrackSettingsSchema,
 SyncStateSchema } from "../gen/tessera/v1/signal_pb.js";
import { ConnectionError, TesseraError } from "../errors.js";
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

/** Why the signaling WebSocket closed. */
export interface CloseInfo {
  /** WebSocket close code (4001 auth expired, 4003 revoked, 4008 ping timeout, 4009 duplicate). */
  code: number;
  reason: string;
  /** True when {@link SignalClient.close} initiated it. */
  local: boolean;
}

/** Timing knobs of {@link SignalClient}; mainly for tests. */
export interface SignalTimeouts {
  /** Wait for the first server message after the upgrade. Default 15 s. */
  joinMs: number;
  /** Wait for a `RequestResponse`. Default 10 s. */
  requestMs: number;
}

const DEFAULT_TIMEOUTS: SignalTimeouts = { joinMs: 15_000, requestMs: 10_000 };
const SUBPROTOCOL = "tessera.v1";
/** Local close code used when the server stops answering pings. */
export const PING_TIMEOUT_CODE = 4008;

interface PendingRequest {
  resolve: () => void;
  reject: (err: unknown) => void;
  timer: ReturnType<typeof setTimeout>;
}

/**
 * Signaling connection to a Tessera server: one WebSocket carrying binary protobuf
 * frames (ADR-0003).
 */
export class SignalClient {
  /** Called for every message received from the server, except join/reconnect/request responses. */
  onMessage: ((message: SignalResponse) => void) | undefined;
  /** Called once when the WebSocket closes. */
  onClose: ((info: CloseInfo) => void) | undefined;
  /** Last measured signaling round-trip time in milliseconds. */
  rtt = 0;

  readonly timeouts: SignalTimeouts;
  #ws: WebSocket | undefined;
  #nextRequestId = 1;
  readonly #pending = new Map<number, PendingRequest>();
  #pingTimer: ReturnType<typeof setInterval> | undefined;
  #lastReceived = 0;
  #closedLocally = false;

  constructor(timeouts: Partial<SignalTimeouts> = {}) {
    this.timeouts = { ...DEFAULT_TIMEOUTS, ...timeouts };
  }

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
    const msg = await this.#open(url, token, opts);
    if (msg.message.case !== "join") {
      this.close();
      throw new ConnectionError("internal", `expected join response, got ${msg.message.case}`);
    }
    return msg.message.value;
  }

  /**
   * Resumes an existing session (`reconnect=1&sid=...`) and waits for the `ReconnectResponse`.
   *
   * @throws {ConnectionError} with code `resume_failed` when the server no longer knows the session.
   */
  async resume(url: string, token: string, opts: SignalConnectOptions): Promise<ReconnectResponse> {
    const msg = await this.#open(url, token, { ...opts, reconnect: true });
    if (msg.message.case !== "reconnect") {
      this.close();
      throw new ConnectionError("internal", `expected reconnect response, got ${msg.message.case}`);
    }
    return msg.message.value;
  }

  /** Starts the keep-alive: pings every `intervalSec`, closes when silent for `timeoutSec`. */
  startPing(intervalSec: number, timeoutSec: number): void {
    this.stopPing();
    const interval = Math.max(1, intervalSec || 5) * 1000;
    const timeout = Math.max(2, timeoutSec || 15) * 1000;
    this.#pingTimer = setInterval(() => {
      if (!this.isConnected) return;
      if (Date.now() - this.#lastReceived > timeout) {
        this.#handleClose({ code: PING_TIMEOUT_CODE, reason: "ping timeout", local: false });
        return;
      }
      this.sendPing(this.rtt);
    }, interval);
  }

  /** Stops the keep-alive timer. */
  stopPing(): void {
    if (this.#pingTimer !== undefined) clearInterval(this.#pingTimer);
    this.#pingTimer = undefined;
  }

  /** Sends an offer for the publisher PeerConnection. */
  sendOffer(offer: RTCSessionDescriptionInit, id: number): void {
    this.send({ case: "offer", value: { type: offer.type, sdp: offer.sdp ?? "", id } });
  }

  /** Sends an answer for the subscriber PeerConnection. */
  sendAnswer(answer: RTCSessionDescriptionInit, id: number): void {
    this.send({ case: "answer", value: { type: answer.type, sdp: answer.sdp ?? "", id } });
  }

  /** Sends one local ICE candidate, or end-of-candidates when `final` is set. */
  sendTrickle(candidate: RTCIceCandidateInit, target: SignalTarget, final = false): void {
    this.send({
      case: "trickle",
      value: { candidateInit: JSON.stringify(candidate), target, final },
    });
  }

  /** Announces a track before it is negotiated on the publisher PeerConnection. */
  /** Asks to publish a track; rejects with the server's reason when it refuses. */
  sendAddTrack(request: AddTrackInit): Promise<void> {
    return this.request({ case: "addTrack", value: request });
  }

  /** Mutes or unmutes a published track. */
  sendMute(trackSid: string, muted: boolean): void {
    this.send({ case: "mute", value: { sid: trackSid, muted } });
  }

  /** Removes a published track. */
  sendUnpublish(trackSid: string): void {
    this.send({ case: "unpublishTrack", value: { trackSid } });
  }

  /** Subscribes to or unsubscribes from tracks. */
  sendSubscription(trackSids: string[], subscribe: boolean): void {
    this.send({ case: "subscription", value: { trackSids, subscribe } });
  }

  /** Updates subscriber preferences (quality, size, pause) for subscribed tracks. */
  sendTrackSettings(settings: TrackSettingsInit): void {
    this.send({ case: "trackSetting", value: settings });
  }

  /** Sends the client state after a resume. */
  sendSyncState(state: MessageInitShape<typeof SyncStateSchema>): void {
    this.send({ case: "syncState", value: state });
  }

  /** Updates own name, metadata and attributes; resolves once the server accepted it. */
  updateMetadata(update: { name?: string; metadata?: string; attributes?: Record<string, string> }): Promise<void> {
    return this.request({ case: "updateMetadata", value: update });
  }

  /** Tells the server this participant is leaving. */
  sendLeave(reason: DisconnectReason = DisconnectReason.CLIENT_INITIATED): void {
    this.send({ case: "leave", value: { reason } });
  }

  /** Sends a keep-alive ping carrying the last measured round-trip time. */
  sendPing(rttMs = 0): void {
    this.send({
      case: "ping",
      value: { timestamp: BigInt(Date.now()), rtt: BigInt(Math.round(rttMs)) },
    });
  }

  /**
   * Sends a request with a request id and resolves when the server answers `REASON_OK`.
   *
   * @throws {TesseraError} when the server rejects the request or does not answer in time.
   */
  request(body: SignalRequestBody): Promise<void> {
    const requestId = this.#nextRequestId++;
    return new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.#pending.delete(requestId);
        reject(new ConnectionError("timeout", "signal request timed out"));
      }, this.timeouts.requestMs);
      this.#pending.set(requestId, { resolve, reject, timer });
      try {
        this.#sendWithId(body, requestId);
      } catch (err) {
        clearTimeout(timer);
        this.#pending.delete(requestId);
        reject(err);
      }
    });
  }

  /** Sends a request without waiting for an answer. */
  send(body: SignalRequestBody): void {
    this.#sendWithId(body, 0);
  }

  /** Closes the WebSocket without sending a leave request. */
  close(): void {
    this.#closedLocally = true;
    this.stopPing();
    const ws = this.#ws;
    this.#ws = undefined;
    this.#failPending(new ConnectionError("cancelled", "signal connection closed"));
    if (ws && ws.readyState <= WebSocket.OPEN) {
      ws.onmessage = ws.onclose = ws.onerror = null;
      ws.close(1000);
    }
  }

  #open(url: string, token: string, opts: SignalConnectOptions): Promise<SignalResponse> {
    const target = buildSignalUrl(url, token, opts);
    this.close();
    this.#closedLocally = false;
    return new Promise<SignalResponse>((resolve, reject) => {
      let settled = false;
      let opened = false;
      const ws = new WebSocket(target, SUBPROTOCOL);
      ws.binaryType = "arraybuffer";
      this.#ws = ws;
      const fail = (err: ConnectionError) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        if (this.#ws === ws) this.#ws = undefined;
        ws.onmessage = ws.onclose = ws.onerror = ws.onopen = null;
        try {
          ws.close();
        } catch {
          /* already closed */
        }
        reject(err);
      };
      const timer = setTimeout(
        () => fail(new ConnectionError("timeout", "timed out waiting for the join response")),
        this.timeouts.joinMs,
      );
      ws.onopen = () => {
        opened = true;
        this.#lastReceived = Date.now();
      };
      ws.onerror = () => {
        // The browser hides the HTTP status; the close handler probes for it.
      };
      ws.onclose = (ev) => {
        if (!settled) {
          void this.#classifyFailure(target, opened, opts.reconnect === true).then(fail);
          return;
        }
        this.#handleClose({ code: ev.code, reason: ev.reason, local: this.#closedLocally });
      };
      ws.onmessage = (ev: MessageEvent) => {
        this.#lastReceived = Date.now();
        let msg: SignalResponse;
        try {
          msg = decodeResponse(ev.data as ArrayBuffer);
        } catch {
          return; // undecodable frames are ignored for forward compatibility
        }
        if (!settled) {
          settled = true;
          clearTimeout(timer);
          resolve(msg);
          return;
        }
        this.#dispatch(msg);
      };
    });
  }

  #dispatch(msg: SignalResponse): void {
    switch (msg.message.case) {
      case "pong": {
        const sent = Number(msg.message.value.lastPingTimestamp);
        if (sent > 0) this.rtt = Math.max(0, Date.now() - sent);
        return;
      }
      case "requestResponse": {
        const r = msg.message.value;
        const p = this.#pending.get(r.requestId);
        if (!p) return;
        this.#pending.delete(r.requestId);
        clearTimeout(p.timer);
        if (r.reason === RequestResponse_Reason.OK || r.reason === RequestResponse_Reason.UNSPECIFIED) p.resolve();
        else p.reject(new TesseraError(`request rejected: ${RequestResponse_Reason[r.reason] ?? r.reason} ${r.message}`));
        return;
      }
      default:
        this.onMessage?.(msg);
    }
  }

  #handleClose(info: CloseInfo): void {
    if (!this.#ws && !info.local && this.onClose === undefined) return;
    const ws = this.#ws;
    this.#ws = undefined;
    this.stopPing();
    this.#failPending(new ConnectionError("not_connected", "signal connection closed"));
    if (ws) {
      ws.onmessage = ws.onclose = ws.onerror = null;
      try {
        ws.close();
      } catch {
        /* ignore */
      }
    }
    if (!info.local) this.onClose?.(info);
  }

  #failPending(err: unknown): void {
    for (const p of this.#pending.values()) {
      clearTimeout(p.timer);
      p.reject(err);
    }
    this.#pending.clear();
  }

  #sendWithId(message: SignalRequestBody, requestId: number): void {
    const ws = this.#ws;
    if (ws?.readyState !== WebSocket.OPEN) {
      throw new ConnectionError("not_connected", "signal connection is not open");
    }
    ws.send(encodeRequest(create(SignalRequestSchema, { requestId, message })));
  }

  /** Best-effort lookup of the HTTP status of a failed upgrade (works when CORS allows it). */
  async #classifyFailure(target: string, opened: boolean, resuming: boolean): Promise<ConnectionError> {
    if (opened) return new ConnectionError("server_unreachable", "connection closed before the join response");
    try {
      const probe = target.replace(/^ws/, "http").replace(/([?&])access_token=[^&]*/, "$1access_token=" + new URL(target).searchParams.get("access_token"));
      const ctl = new AbortController();
      const t = setTimeout(() => ctl.abort(), 3000);
      const res = await fetch(probe, { signal: ctl.signal });
      clearTimeout(t);
      const status = res.status;
      let msg = "";
      try {
        msg = ((await res.json()) as { msg?: string }).msg ?? "";
      } catch {
        /* not JSON */
      }
      if (status === 401 || status === 403) return new ConnectionError("not_allowed", msg || `rejected (HTTP ${status})`, { status });
      if (status === 426) return new ConnectionError("unsupported_protocol", msg || "unsupported protocol", { status });
      if (status === 409 && resuming) return new ConnectionError("resume_failed", msg || "session cannot be resumed", { status });
      if (status === 404) return new ConnectionError("not_found", msg || "room not found", { status });
      if (status === 429) return new ConnectionError("rate_limited", msg || "rate limited", { status });
      if (status === 503) return new ConnectionError("server_unreachable", msg || "server unavailable", { status });
    } catch {
      /* unreachable, or blocked by CORS */
    }
    // Unreachable (network down, server restarting): keep trying; only the server can say a
    // session is gone (409 above).
    return new ConnectionError("server_unreachable", "could not open the signaling connection");
  }
}
