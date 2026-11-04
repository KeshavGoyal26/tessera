import { ConnectionError } from "../errors.js";
import type { SignalTarget } from "../gen/tessera/v1/signal_pb.js";

const ANSWER_TIMEOUT_MS = 15_000;

/**
 * Wraps one `RTCPeerConnection`. A participant has two: the publisher (client offers) and
 * the subscriber (server offers).
 */
export class PCTransport {
  /** The wrapped peer connection. */
  readonly pc: RTCPeerConnection;
  /** Which of the participant's two connections this is. */
  readonly target: SignalTarget;

  /** Called with each local offer that must be sent to the server. */
  onOffer: ((offer: RTCSessionDescriptionInit, id: number) => void) | undefined;
  /** Called with each local ICE candidate; `null` signals end-of-candidates. */
  onIceCandidate: ((candidate: RTCIceCandidateInit | null) => void) | undefined;
  /** Called when the connection state changes. */
  onConnectionState: ((state: RTCPeerConnectionState) => void) | undefined;

  #offerId = 0;
  #negotiating = false;
  #renegotiate = false;
  #restartIce = false;
  #answerWaiter: { id: number; resolve: () => void; reject: (e: unknown) => void } | undefined;
  #pendingCandidates: RTCIceCandidateInit[] = [];
  #chain: Promise<unknown> = Promise.resolve();
  #closed = false;
  /** Last local answer (subscriber), kept for `SyncState`. */
  lastAnswer: RTCSessionDescriptionInit | undefined;

  constructor(target: SignalTarget, config: RTCConfiguration) {
    this.target = target;
    this.pc = new RTCPeerConnection(config);
    this.pc.onicecandidate = (ev) => this.onIceCandidate?.(ev.candidate ? ev.candidate.toJSON() : null);
    this.pc.onconnectionstatechange = () => this.onConnectionState?.(this.pc.connectionState);
  }

  /** Id of the latest local offer. */
  get offerId(): number {
    return this.#offerId;
  }

  /**
   * Creates an offer (optionally with an ICE restart), hands it to {@link onOffer} and waits
   * for the answer. Calls made while a negotiation runs are coalesced into one more round.
   */
  async negotiate(options?: { iceRestart?: boolean }): Promise<void> {
    if (options?.iceRestart) this.#restartIce = true;
    if (this.#negotiating) {
      this.#renegotiate = true;
      return;
    }
    this.#negotiating = true;
    try {
      do {
        this.#renegotiate = false;
        const iceRestart = this.#restartIce;
        this.#restartIce = false;
        const offer = await this.pc.createOffer({ iceRestart });
        await this.pc.setLocalDescription(offer);
        const id = ++this.#offerId;
        const answered = new Promise<void>((resolve, reject) => {
          const timer = setTimeout(
            () => reject(new ConnectionError("timeout", "no answer to publisher offer")),
            ANSWER_TIMEOUT_MS,
          );
          this.#answerWaiter = {
            id,
            resolve: () => {
              clearTimeout(timer);
              resolve();
            },
            reject: (e) => {
              clearTimeout(timer);
              reject(e instanceof Error ? e : new Error(String(e)));
            },
          };
        });
        this.onOffer?.({ type: "offer", sdp: this.pc.localDescription?.sdp ?? offer.sdp }, id);
        await answered;
      } while (this.#renegotiate && !this.#closed);
    } finally {
      this.#negotiating = false;
      this.#answerWaiter = undefined;
    }
  }

  /** Applies a remote offer and returns the local answer. */
  handleOffer(offer: RTCSessionDescriptionInit): Promise<RTCSessionDescriptionInit> {
    return this.#serial(async () => {
      await this.pc.setRemoteDescription(offer);
      await this.#flushCandidates();
      const answer = await this.pc.createAnswer();
      await this.pc.setLocalDescription(answer);
      this.lastAnswer = { type: "answer", sdp: this.pc.localDescription?.sdp ?? answer.sdp };
      return this.lastAnswer;
    });
  }

  /** Applies the remote answer to the last local offer; stale answers are ignored. */
  async handleAnswer(answer: RTCSessionDescriptionInit, id: number): Promise<void> {
    if (id !== 0 && id < this.#offerId) return;
    await this.#serial(async () => {
      await this.pc.setRemoteDescription(answer);
      await this.#flushCandidates();
    });
    if (this.#answerWaiter && (id === 0 || id >= this.#answerWaiter.id)) this.#answerWaiter.resolve();
  }

  /** Adds a remote ICE candidate, queueing it until the remote description is set. */
  async addIceCandidate(candidate: RTCIceCandidateInit): Promise<void> {
    if (!this.pc.remoteDescription) {
      this.#pendingCandidates.push(candidate);
      return;
    }
    try {
      await this.pc.addIceCandidate(candidate);
    } catch {
      // A candidate for a superseded generation is harmless.
    }
  }

  /** Rejects a pending negotiation, e.g. when the signal connection is lost. */
  abortNegotiation(reason: unknown): void {
    this.#answerWaiter?.reject(reason);
  }

  /** Closes the peer connection. */
  close(): void {
    this.#closed = true;
    this.abortNegotiation(new ConnectionError("cancelled", "transport closed"));
    this.pc.onicecandidate = this.pc.onconnectionstatechange = null;
    this.pc.close();
  }

  async #flushCandidates(): Promise<void> {
    const list = this.#pendingCandidates;
    this.#pendingCandidates = [];
    for (const c of list) {
      try {
        await this.pc.addIceCandidate(c);
      } catch {
        /* ignore */
      }
    }
  }

  #serial<T>(fn: () => Promise<T>): Promise<T> {
    const run = this.#chain.then(fn, fn);
    this.#chain = run.catch(() => undefined);
    return run;
  }
}
