# Load test results

Tool: `cmd/tessera-loadtest` (synthetic RTP, no encoder; see the package comment for the media model).
Each publisher sends Opus (20 ms frames) and one VP8 track with 3 simulcast layers (~90 / ~300 /
~550 kbit/s of synthetic payload at 30 fps, keyframe every 2 s). Payloads carry the send time, so the
receivers measure one-way latency through the SFU (load tester and server share the clock).

## Machine

Cloud container: 4 vCPU Intel Xeon @ 2.10 GHz (x86-64), 15 GB RAM, Linux 6.18, go1.27.3. **The load tester
ran on the same 4 vCPUs as the server** (loopback networking), so the clients' Pion stacks (DTLS, SRTP,
interceptors) consume most of the CPU and the numbers below are a lower bound for the server, not
the ARCHITECTURE.md section 9 reference-machine figures.

Server config: `tessera --config lt.yaml` with `development: true`, `rtc.ips: [127.0.0.1]`,
`logging.level: warn`, everything else default (congestion control on).

## Results (2026-11-04)

| Scenario | Command | Clients | Server egress | Server CPU | RSS | Loss | Latency p50 / p99 | Join p95 | First packet p95 |
|----------|---------|---------|---------------|------------|-----|------|-------------------|----------|------------------|
| Meeting: 3 rooms × 8 publishers (each receives 7 videos + 7 audios) | `--rooms 3 --publishers 8 --duration 30s --ramp-up 10s --loopback` | 24 (48 PCs, 336 DownTracks) | 18.4k pkt/s, ~55 Mbit/s | 56% of one core | 212 MB | 0.00% | 0.36 / 2.43 ms | 3 ms | 503 ms |
| Webinar: 1 publisher, 60 listen-only subscribers | `--rooms 1 --publishers 1 --subscribers 60 --duration 30s --ramp-up 10s --loopback` | 61 (62 PCs, 120 DownTracks) | 6.7k pkt/s, ~10 Mbit/s | 21% of one core | 213 MB | 0.00% | 0.84 / 2.96 ms | 4 ms | 77 ms |

CPU, RSS, GC pause (p99 0.66 ms in both runs) and packet rates come from the server's
`GET :6789/stats` during the run; the client-side columns come from the load tester's JSON report.
"Join" is WebSocket connect to JoinResponse; "first packet" is connect to the first RTP packet on any
subscribed track.

Observations:

- Throughput grows during the meeting run (12 → 55 Mbit/s): subscribers start on low simulcast layers and the
  allocator upgrades them as the TWCC estimate grows (AIMD, 3 s upgrade hold), as designed.
- Roughly 33k egress pkt/s per server core including ICE, DTLS, SRTP, RTCP and BWE, on a machine shared with
  the clients. The micro-benchmarks (`docs/perf/WP1.md`) give 390k to 490k pkt/s per core for the
  forwarding path alone, so most server CPU here goes to per-packet SRTP and Pion's socket/interceptor
  path, not to Tessera's forwarding code.

## Not done yet (next steps)

- Run the server and load tester on separate machines to measure P5/P6/P7 capacity on the reference
  machine (target 250 meeting participants / 2,000 webinar subscribers per 4 vCPU).
- Profile under load (`/debug/pprof/profile` on the metrics port) and fix the top hotspots; first
  candidates from the micro-benchmark profile: AES-GCM SRTP profile, `sendmmsg` batching.
- Churn scenario and recording load (P11) are not scripted yet.
