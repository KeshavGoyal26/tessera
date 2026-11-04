/** One media direction of a stats summary. */
export interface MediaStatsSummary {
  /** Bits per second since the previous sample (0 on the first sample). */
  bitrate: number;
  packets: number;
  packetsLost: number;
  /** Lost packets as a percentage of expected packets (receive side). */
  lossPercent: number;
  /** Jitter in milliseconds (receive side, worst track). */
  jitterMs: number;
  /** Frames per second of the busiest video track. */
  fps: number;
  /** Resolution of the largest video track. */
  width: number;
  height: number;
}

/** Summary of one PeerConnection. */
export interface StatsSummary {
  send: MediaStatsSummary;
  receive: MediaStatsSummary;
  /** Round-trip time of the selected candidate pair in milliseconds, if known. */
  rttMs: number | undefined;
  /** Available outgoing bitrate estimate (bits per second), if known. */
  availableOutgoingBitrate: number | undefined;
}

type Stat = Record<string, unknown> & { type: string; id: string; timestamp: number };

const empty = (): MediaStatsSummary => ({
  bitrate: 0, packets: 0, packetsLost: 0, lossPercent: 0, jitterMs: 0, fps: 0, width: 0, height: 0,
});

/**
 * Turns RTCStatsReports into summaries. Bitrate needs two samples, so keep one sampler per
 * PeerConnection and call {@link StatsSampler.sample} periodically.
 */
export class StatsSampler {
  #prev = new Map<string, { bytes: number; ts: number }>();

  /** Summarises `report`. */
  sample(report: RTCStatsReport | Iterable<unknown> | Map<string, unknown>): StatsSummary {
    const values: Stat[] = [];
    (report as Map<string, unknown>).forEach?.((v) => values.push(v as Stat));
    if (values.length === 0 && Symbol.iterator in Object(report)) {
      for (const v of report as Iterable<unknown>) values.push(v as Stat);
    }
    return summarizeStats(values, this.#prev);
  }
}

function num(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

/** @internal Exposed for tests. */
export function summarizeStats(stats: Stat[], prev: Map<string, { bytes: number; ts: number }>): StatsSummary {
  const send = empty();
  const receive = empty();
  let rtt: number | undefined;
  let aob: number | undefined;
  let recvLost = 0;
  let recvPackets = 0;

  for (const s of stats) {
    if (s.type === "outbound-rtp" && !s.isRemote) {
      send.bitrate += rate(prev, s.id, num(s.bytesSent), s.timestamp);
      send.packets += num(s.packetsSent);
      if (s.kind === "video") {
        send.fps = Math.max(send.fps, num(s.framesPerSecond));
        if (num(s.frameWidth) * num(s.frameHeight) >= send.width * send.height) {
          send.width = num(s.frameWidth);
          send.height = num(s.frameHeight);
        }
      }
    } else if (s.type === "inbound-rtp" && !s.isRemote) {
      receive.bitrate += rate(prev, s.id, num(s.bytesReceived), s.timestamp);
      receive.packets += num(s.packetsReceived);
      recvPackets += num(s.packetsReceived);
      recvLost += Math.max(0, num(s.packetsLost));
      receive.jitterMs = Math.max(receive.jitterMs, num(s.jitter) * 1000);
      if (s.kind === "video") {
        receive.fps = Math.max(receive.fps, num(s.framesPerSecond));
        if (num(s.frameWidth) * num(s.frameHeight) >= receive.width * receive.height) {
          receive.width = num(s.frameWidth);
          receive.height = num(s.frameHeight);
        }
      }
    } else if (s.type === "remote-inbound-rtp") {
      send.packetsLost += Math.max(0, num(s.packetsLost));
      send.jitterMs = Math.max(send.jitterMs, num(s.jitter) * 1000);
    } else if (s.type === "candidate-pair" && (s.nominated === true || s.state === "succeeded") && s.selected !== false) {
      if (s.currentRoundTripTime !== undefined) rtt = num(s.currentRoundTripTime) * 1000;
      if (s.availableOutgoingBitrate !== undefined) aob = num(s.availableOutgoingBitrate);
    }
  }
  receive.packetsLost = recvLost;
  receive.lossPercent = recvPackets + recvLost > 0 ? (recvLost / (recvPackets + recvLost)) * 100 : 0;
  send.lossPercent = send.packets > 0 ? (send.packetsLost / (send.packets + send.packetsLost)) * 100 : 0;
  return { send, receive, rttMs: rtt, availableOutgoingBitrate: aob };
}

function rate(prev: Map<string, { bytes: number; ts: number }>, id: string, bytes: number, ts: number): number {
  const p = prev.get(id);
  prev.set(id, { bytes, ts });
  if (!p || ts <= p.ts || bytes < p.bytes) return 0;
  return ((bytes - p.bytes) * 8 * 1000) / (ts - p.ts);
}
