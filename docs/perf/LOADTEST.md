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

## Scaling run (2026-11-05, WP5 session E)

Same machine and server config as above (server and load tester share 4 vCPUs, loopback, `go1.27.3`,
`tessera` built from main at `2ae7915`). Each row is a 30-40 s steady-state window after ramp-up; server
columns are from `GET :6789/stats` sampled mid-run, client columns from the load tester's JSON report.
Every publisher sends 1 Opus + 1 VP8 (3 simulcast layers) track. Server CPU is process CPU in percent of
one core (400% = the whole machine, which the clients also use).

| Scenario | Clients (PCs) | Server egress | Server CPU | RSS | Goroutines | GC pause p99 | Loss | Latency p50 / p99 | Join failures | First packet p95 |
|----------|---------------|---------------|------------|-----|------------|--------------|------|-------------------|---------------|------------------|
| 1 room, 2 pub + 10 sub | 12 (14) | 2.5k pkt/s, 5.2 Mbit/s | 10% | 48 MB | 429 | 0.16 ms | 0.00% | 0.39 / 0.92 ms | 0 | 502 ms |
| 1 room, 2 pub + 50 sub | 52 (54) | 11.4k pkt/s, 30 Mbit/s | 22% | 84 MB | 1.6k | 0.20 ms | 0.00% | 0.61 / 1.75 ms | 0 | 67 ms |
| 1 room, 2 pub + 100 sub | 102 (104) | 22.5k pkt/s, 65 Mbit/s | 36% | 141 MB | 3.1k | 0.20 ms | 0.00% | 0.72 / 2.44 ms | 0 | 73 ms |
| 10 rooms x 4 pub (full mesh) | 40 (80) | 13.4k out / 10.3k in pkt/s, 24 Mbit/s | 48% | 298 MB | 2.3k | 0.23 ms | 0.00% | 0.27 / 0.89 ms | 0 | 336 ms |
| 50 rooms x 4 pub (full mesh) | 200 (400) | 63.7k out / 51.4k in pkt/s, 111 Mbit/s | 132% | 1.35 GB | 11.3k | 5.2 ms | 0.00% | 0.30 / 4.2 ms | 0 | 211 ms |

Reproduce (`lt.yaml` as above; secret `devsecret-devsecret-devsecret-0000`):

```bash
go build -o bin/ ./cmd/...
bin/tessera --config lt.yaml &
bin/tessera-loadtest -rooms 50 -publishers 4 -duration 40s -ramp-up 25s -loopback \
  -api-key devkey -secret devsecret-devsecret-devsecret-0000 -json out.json
curl -s localhost:6789/debug/pprof/profile?seconds=15 -o cpu.pprof   # during the run
```

Findings:

- No loss, no join failures and sub-5 ms p99 forwarding latency up to 200 clients / 400 PeerConnections /
  1,200 DownTracks, with the load generator competing for the same four cores. CPU scales roughly linearly
  with egress packets (about 20 to 28 k egress pkt/s per core for the whole server including ICE, DTLS and
  RTCP; the clients' own Pion stacks take the rest of the machine), so the 250 meeting / 2,000 webinar targets
  still need a separate load-generator host to be confirmed (P5/P6/P7 not claimed here).
- Memory is dominated by per-track receive buffers, not by subscribers: 100 subscribers cost ~1 MB each,
  while 50 rooms x 4 publishers x 4 receive streams cost about 1.3 GB.
- The only GC pressure shows at the largest run (pause p99 5.2 ms, caused by the ~700 MB live heap
  being scanned); everything smaller stays under 0.25 ms.
- Not covered: churn (join/leave loops), recording load (P11), TCP/TURN transport, separate hosts.

### CPU profile under load (50 rooms x 4 publishers, 15 s, 18.6 s of samples on 4 cores)

`go tool pprof -top -cum bin/tessera cpu.pprof`:

| Share of CPU (cum) | Where | Comment |
|--------------------|-------|---------|
| 54% | `rtc.(*ReceiverImpl).readLoop` -> `fanout` | the forwarding loop; includes everything below |
| 39% | `rtc.(*DownTrackImpl).WriteRTP` -> `writeLocked` | per-subscriber rewrite plus send |
| 34% | `pion/srtp.(*SessionSRTP).writeRTP` | SRTP protect (AES-GCM, ~1.2% flat) plus the socket write |
| 23% | `syscall.sendto` via `ice.(*udpMuxedConn).WriteToAddrPort` (`rtc.countingConn`) | one `sendto` per packet: **the top hotspot** (23% flat in `Syscall6`) |
| 10% | `TrackRemote.Read` / `srtp.decrypt` / TWCC sender interceptor (8%) | ingress side |
| ~6% | Go scheduler (`selectgo`, `chansend`, `futex`) and `packetio.Buffer` | channel hand-off in Pion's mux and ICE paths |
| ~3% | `buffer.(*Buffer).Write`, `Forwarder.GetTranslation`, `bwe.OnPacketSent` | Tessera's own code is under 5% of the total |

Heap at the same moment (`inuse_space`, 700 MB): 459 MB (66%) `buffer.getSlab` (one `slots * MaxPacketSize`
slab per receive stream), 20 MB `bwe.NewEstimator`, 19 MB `simulcast.NewForwarder`.

### Hotspot recommendations (not applied)

Nothing was changed: every hotspot lies in `pkg/rtc` (session D's area) or in Pion internals.

1. **Batch UDP writes** (`sendmmsg` / `WriteBatch`) in `rtc.countingConn`. `sendto` is the largest single
   cost (23% of CPU) and one syscall per RTP packet is the reason. Needs the ICE UDP mux to expose a batching
   hook (Pion `ice.UDPMux` wraps one `net.PacketConn`); expected to cut server CPU by roughly 10 to 15%.
2. **Shrink or lazily size the receive ring.** The slab is allocated at full `slots * MaxPacketSize` for every
   stream, including audio and the low simulcast layers, which dominates RSS (1.35 GB for 800 streams). Size
   audio rings smaller (an Opus packet is < 300 bytes), or use size-classed slots, to cut memory 2x to 4x and
   the GC pause at scale.
3. **Reduce channel hops** in the Pion mux / ICE path (about 6%): not fixable in Tessera without forking Pion.
4. The SRTP AES-GCM profile is already the default and costs only ~1.2% flat here, so crypto is no longer
   the bottleneck it looked like in the micro-benchmarks.

## Not done yet (next steps)

- Run the server and load tester on separate machines to measure P5/P6/P7 capacity on the reference
  machine (target 250 meeting participants / 2,000 webinar subscribers per 4 vCPU).
- Churn scenario and recording load (P11) are not scripted yet.
