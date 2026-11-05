# Tessera vs LiveKit: same-machine load comparison

Measured 2026-11-05. **Read the caveats first**: this is one noisy 4-vCPU container running the server and
both load generators at once. It tells you how the two servers behave under *this* synthetic load, not
how either performs in production.

## Machine

Cloud container, 4 vCPU Intel Xeon @ 2.10 GHz (x86-64), 15 GB RAM, Linux 6.18.44, go1.27.3 (Tessera) and
the go1.26.10 toolchain that LiveKit's go.mod selects. Loopback networking, server and load generator on
the same four cores. Tessera built from `main` (`go build ./cmd/...`); LiveKit server v1.13.11 and
`livekit-cli` v2.19.3 (load tester package) built from source.

## Installing LiveKit (what worked, what did not)

- `go install github.com/livekit/livekit-server/cmd/server@latest` is **refused by Go**: the module's go.mod
  contains `replace` directives (LiveKit's Pion forks). Workaround: copy the module out of the module cache
  and build it as the main module:

  ```bash
  go mod download -json github.com/livekit/livekit-server@v1.13.11   # note "Dir"
  cp -r <Dir>/. /tmp/lk/src && chmod -R u+w /tmp/lk/src
  (cd /tmp/lk/src && go build -o /tmp/lk/bin/livekit-server ./cmd/server)
  ```

- `go install github.com/livekit/livekit-cli/v2/cmd/lk@latest` **fails**: `lk` needs cgo and the PortAudio git
  submodule, which is not in the module zip (also not with `CGO_ENABLED=0`, the packages are excluded).
  The load tester itself (`pkg/loadtester`, the code behind `lk perf load-test`) has no such dependency, so
  `docs/perf/scripts/lk-loadtest.go.txt` is a 30-line `main` around it with the same flags. Its sample media
  (`pkg/provider/resources/*.ivf|h264|ogg`) are Git-LFS pointers in the module zip; they were fetched from
  `https://media.githubusercontent.com/media/livekit/livekit-cli/v2.19.3/pkg/provider/resources/<file>` into a
  copy of the module, which is used through a `replace` directive. Without that the tester prints
  "IVF signature mismatch" and publishes nothing.

## Method

`docs/perf/scripts/run_compare.sh <tessera|tessera-nocc|livekit> <meeting-a|rooms-b|audio-c>` starts the
server, runs the matching generator for 15 s ramp-up + 30 s steady state, samples the **server process**
once per second from `/proc/<pid>/stat` (utime+stime) and `/proc/<pid>/status` (VmRSS), and the loopback
interface from `/proc/net/dev`. Reported values are averages over the last 25 s before the generator exits.
Both servers are sampled by the same script, in the same way.

Server configs are in `docs/perf/scripts/configs/`: LiveKit and Tessera both on port 7880, node IP
127.0.0.1, warn-level logging, defaults otherwise. `tessera-nocc` is Tessera with
`rtc.congestion_control.enabled: false`.

| | Tessera (`tessera-loadtest`) | LiveKit (`lk-loadtest`, = `lk perf load-test`) |
|---|---|---|
| Codec | Opus + VP8 simulcast (3 layers) | Opus + VP8 simulcast (3 layers, `--video-codec vp8`, `--video-resolution high`) |
| Layer bitrates | `-video-kbps 150,600,2000` (synthetic, 30 fps, keyframe/2 s) | pre-encoded loops, 180p ~150, 360p ~600, 720p ~2000 kbit/s |
| Audio | synthetic Opus-sized frames every 20 ms | looped real Opus files |
| Subscribers | listen-only, subscribe to everything (`-publish-only`) | subscribe per `--layout`; ask for layers by tile size |
| Latency | per-packet send timestamp, one-way | not reported by the tool |

Scenarios (the same in both):

- **A** `meeting-a`: 1 room, 2 publishers (audio+video), 50 listen-only subscribers.
  Tessera `-rooms 1 -publishers 2 -subscribers 50 -publish-only`; LiveKit `--video-publishers 2 --audio-publishers 2 --subscribers 50`.
- **B** `rooms-b`: 10 rooms, each 2 publishers + 2 subscribers (4 participants per room on Tessera; LiveKit's tester
  cannot make a publisher that both publishes audio+video and subscribes, so it runs 2 video + 2 audio publishers + 2
  subscribers per room; every subscriber receives 2 audio + 2 video tracks in both).
  Ten `lk-loadtest` processes run concurrently, one per room.
- **C** `audio-c`: 1 room, audio only, 10 publishers + 40 subscribers (50 participants);
  LiveKit `--layout 5x5` so that subscribers take all 10 tracks (the default `speaker` layout subscribes to 6).

## Results

Server columns are the server process (CPU in percent of one core; 400% would be the whole machine). `lo pkt/s`
and `lo Mbit/s` are all datagrams on the loopback interface in both directions (ingress + egress), identical
measurement for both servers. "Client rx" is what the load generator received.

| Scenario | Server | CPU % of 1 core | RSS MB | lo pkt/s | lo Mbit/s | Client rx Mbit/s | Loss | CPU % per 10k lo pkt/s |
|---|---|---|---|---|---|---|---|---|
| A 2 pub + 50 sub | Tessera, default (congestion control on) | 25.4 | 92 | 16.0k | 78.5 | 66.0 | 0 | 15.8 |
| | Tessera, congestion control off | 26.7 | 78 | 30.8k | 226.4 | 207.3 | 0 | 8.7 |
| | LiveKit | 35.5 | 182 | 21.5k | 149.7 | 121.4 | 0 | 16.5 |
| B 10 rooms x (2 pub + 2 sub) | Tessera, default | 33.6 | 159 | 14.6k | 89.7 | 26.3 | 0 | 23.1 |
| | Tessera, congestion control off | 36.2 | 145 | 20.5k | 149.0 | 82.8 | 0 | 17.7 |
| | LiveKit | 43.1 | 199 | 13.9k | 96.8 | 48.2 | 0 | 31.0 |
| C audio, 10 pub + 40 sub | Tessera, default | 28.8 | 92 | 21.7k | 24.7 | 16.0 | 0 | 13.3 |
| | Tessera, congestion control off | 23.6 | 79 | 21.3k | 23.0 | 14.7 | 0 | 11.1 |
| | LiveKit | 45.6 | 191 | 21.5k | 18.2 | 7.6 | 0 | 21.2 |

Tessera client-side latency (one-way through the SFU, p50 / p99): A 0.77 / 2.74 ms, B 0.28 / 0.80 ms,
C 0.86 / 3.27 ms; first packet p95 about 70 ms; join p95 3 ms; no connect failures. **LiveKit's tester does
not report latency, so there is no like-for-like latency number** and none is claimed.

Run-to-run spread, from three repeats of the LiveKit and congestion-control-off rows: CPU within about +-3 points (LiveKit
audio-c ranged 31 to 46%), RSS within +-3%, traffic within 1%.

### Reading the table

1. **Per unit of traffic Tessera used less CPU and about half the memory.** The comparison is fairest in the
   "congestion control off" rows, because only there Tessera forwards the same kind of load as LiveKit; per 10k
   datagrams Tessera needed 8.7 / 17.7 / 11.1 CPU-percent against LiveKit's 16.5 / 31.0 / 21.2. In scenario A
   Tessera was forwarding 1.7x the client-received bitrate for 0.75x the CPU.
2. **The two servers did not forward the same amount of video.** LiveKit picks layers from the tester's tile
   sizes and its own allocator; Tessera's default allocator is driven only by its bandwidth estimate. The default
   rows show Tessera delivering *less* video than LiveKit (66 vs 121 Mbit/s in A, 26 vs 48 in B) on a path with
   unlimited capacity. That is a quality gap, see "Open".
3. LiveKit does more work per packet than Tessera in several places (speaker detection, connection quality,
   dynacast, telemetry, richer signalling); some of the CPU difference is features, not efficiency.
4. Tessera numbers include congestion-control overhead (TWCC feedback processing, allocation) in the default rows.

## Findings and fixes made during this work

| | Before | After |
|---|---|---|
| **BWE collapse** (`pkg/rtc/bwe`): the estimator clamped its estimate *down* to 1.5x the acknowledged rate + 100 kbit/s. A subscriber that only received audio fell to ~190 kbit/s, the allocator paused all video and, without probing, the acknowledged rate could never rise again. Scenario B delivered **audio only** (2.0 Mbit/s received by 20 subscribers). | B: 2.0 Mbit/s client rx, no video | B: 26.3 Mbit/s with video; A: 17.1 -> 66.0 Mbit/s |
| **Batched UDP egress** (`pkg/rtc/udpbatch.go`): `sendto` per datagram was 55% of server CPU (flat, `Syscall6`). Datagrams are now queued and flushed with `sendmmsg`. Scenario A, congestion control off, 30.8k pkt/s: | 29.2 % CPU (2 runs: 29.9, 28.6) | 26.4 % CPU (26.6, 26.3), about -10 % |

CPU profile (`/debug/pprof/profile?seconds=15` at steady state of scenario A, congestion control off,
`go tool pprof -top`): before, 3.85 s of CPU samples in 15 s, 54.5 % flat in `Syscall6` (sendto), 77 % of
samples under `ReceiverImpl.fanout` (the forwarding goroutine blocked in `sendto`). After, 3.44 s (-11 %),
`Syscall6` 54 % but now inside the writer goroutine (`sendmmsg`), and `fanout` is down to 25 %. Tessera's own
forwarding code (`Forwarder`, `GetTranslation`, buffer) is below 5 %; AES-GCM SRTP is about 6 %.
On loopback the kernel also delivers each datagram to the receiving socket inside the sender's syscall, so
`sendmmsg` saves only the per-call overhead; on a real NIC the saving should be larger, but that was not measured.
`TESSERA_UDP_NOBATCH=1` disables the batching for A/B runs. `go test -bench UDPWrite ./pkg/rtc/`:
2626 ns/op direct vs 2259 ns/op batched per 1 kB datagram on the calling side.

The benchmark gate (`docs/perf/benchgate.sh`) reports `DownTrackWriteRTPGCM` +49 % against the committed baseline on this
container, but the same number is measured with and without these changes (about 520 ns now vs 348 ns in the
baseline, a different machine), so it is machine drift; CI hardware is unaffected.

## Caveats

- **Different load generators.** Tessera's tester sends synthetic RTP payloads; LiveKit's replays real encoded
  media through the LiveKit Go SDK. Different packetisation (Tessera: <= 1100-byte payloads, LiveKit: real encoded
  frames, packetised by the LiveKit SDK), different audio sizes, different subscriber behaviour. Bitrates are
  matched only nominally. Client CPU use also differs and competes with the server for the same cores.
- **Loopback, one machine, four cores shared by server and clients.** No real network, no NIC offload, no queueing
  delay or loss. Kernel loopback work is charged to the sending process.
- **Not the same forwarded load** (see "Reading the table", point 2). Compare per-packet or per-Mbit cost, not raw CPU.
- Each cell is one 25 s window; three repeats showed the spread given above. No confidence intervals.
- The LiveKit configuration is its defaults plus `node_ip: 127.0.0.1`; no LiveKit tuning was attempted, nor any
  Tessera tuning beyond the two fixes above. LiveKit v1.13.11 was current when built; Tessera is at `main`.
- Tessera and LiveKit were never run at the same time. Memory is RSS at the end of the window.

## Open

- **Without probing, Tessera's allocator cannot climb to the top simulcast layer** when the subscriber is only
  receiving lower layers: the increase limit (1.5x acknowledged rate) is smaller than the jump 600 -> 2000 kbit/s.
  `rtc.congestion_control.probe` is accepted but not implemented (no padding probes). In scenario A subscribers
  settle on the middle layers (66 Mbit/s of a possible 207). Implementing padding probes is the real fix; raising
  the multiplier was not done because it could not be tested on a constrained link here.
- Real-link behaviour (loss, delay), separate hosts, and a latency measurement for LiveKit remain unmeasured.

## Reproduce

```bash
go build -o bin/ ./cmd/...
# LiveKit server and tester: see "Installing LiveKit" above, then
cp docs/perf/scripts/configs/*.yaml /tmp/lk/
DUR=30 docs/perf/scripts/run_compare.sh tessera meeting-a /tmp/lk/out   # also tessera-nocc, livekit; rooms-b, audio-c
```
