import { NotImplementedError } from "../errors.js";
import type { SignalTarget } from "../gen/tessera/v1/signal_pb.js";

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

  constructor(target: SignalTarget, config: RTCConfiguration) {
    this.target = target;
    this.pc = new RTCPeerConnection(config);
  }

  /** Creates an offer (optionally with an ICE restart) and hands it to {@link onOffer}. */
  async negotiate(_options?: { iceRestart?: boolean }): Promise<void> {
    throw new NotImplementedError("PCTransport.negotiate");
  }

  /** Applies a remote offer and returns the local answer. */
  async handleOffer(_offer: RTCSessionDescriptionInit): Promise<RTCSessionDescriptionInit> {
    throw new NotImplementedError("PCTransport.handleOffer");
  }

  /** Applies the remote answer to the last local offer. */
  async handleAnswer(_answer: RTCSessionDescriptionInit, _id: number): Promise<void> {
    throw new NotImplementedError("PCTransport.handleAnswer");
  }

  /** Adds a remote ICE candidate, queueing it until the remote description is set. */
  async addIceCandidate(_candidate: RTCIceCandidateInit): Promise<void> {
    throw new NotImplementedError("PCTransport.addIceCandidate");
  }

  /** Closes the peer connection. */
  close(): void {
    this.pc.close();
  }
}
