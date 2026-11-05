# Tessera status report

Final review and hardening pass, 2026-11-05. This report covers what is on `main` after the
overnight build (sessions A–C), the review session (D) and the parallel load and Docker session
(E, which owns `cmd/tessera-loadtest`, `deploy/` and `docs/perf/`). The per-work-package details
are in [PROGRESS.md](PROGRESS.md).

## Feature matrix against the original requirements

Legend: **done** = implemented and covered by automated tests; **partial** = works with gaps
that are listed; **missing** = not implemented.

| Requirement | Status | Evidence | Gaps |
|-------------|--------|----------|------|
| **WebRTC on modern browsers** | partial | Browser SDK (`sdk/js`) with Playwright e2e against a real `tessera --dev`: two-way video, mute, layer selection, data packets, screen share, reconnect, bad token. Chromium: 6/6. Firefox: two-way video and bad token pass after this session's msid fix; see the CI section for the rest. | Firefox reconnect cannot be simulated with Playwright (skipped); Firefox screen share is skipped by the suite. Safari/WebKit has never been tested. |
| **SFU with many feeds** | done | Pion for ICE/DTLS/SRTP only; own forwarding, NACK ring buffer, simulcast (VP8 + H.264) with SN/TS/picture-id rewrite, temporal-layer drop, TWCC send-side BWE, per-subscriber allocator with priorities and pause, dynacast, active speakers, connection quality, ICE-TCP, embedded TURN, NAT 1:1, resume. Go e2e suite (`test/e2e`) with real Pion clients, plus a full-system test (3 publishers, 5 subscribers, a policed link, HLS recording). | No padding probes (the allocator grows by AIMD only), single node only, inline fan-out only (no parallel shards), subscriber transceivers are not reused. |
| **HLS recording** | done | `pkg/hls` (H.264 depacketiser, fMP4, segmenter, playlists), Opus in fMP4, AAC mode and VP8 fallback through FFmpeg, local and S3 storage, coordinator and worker protocol, participant and track recordings, A/V alignment from RTCP SR. Validated with ffprobe/ffmpeg in CI (`recorder`, `go-e2e`). | No composite (grid) recording; no automated hls.js playback check; no long soak; MinIO was never run (fake S3 in tests); native Apple playback of Opus-in-fMP4 is best effort. |
| **Optimized for load** | partial | Forwarding path does 0 allocs/op (tested); 486k pkts/s/core with SRTP in micro-benchmarks (target 100k); load tester runs show 0% loss and p99 latency under 3 ms for 24- and 61-client scenarios (numbers below). | Load was only measured with server and clients on the same 4 vCPUs, so the capacity targets (250 meeting participants / 2,000 webinar subscribers per 4 vCPU) are **unverified**. No profiling under load, no benchmark regression gate yet (session E). |
| **Easy Java integration** | done | `sdk/java`: AccessToken/RoomGrant builders, Room/Recording service clients (sync and async, retries), WebhookReceiver with signature check, golden contract tests shared with Go, a cross-language test against the real Go admin server, a Spring Boot example with MockMvc tests, and a pom ready for Maven Central. | Not published to Maven Central (needs a GPG key and credentials). The cross-language test uses `adminfake`, not `tessera --dev`. |
| **Admin dashboard** | partial | `/admin` on the server port: API-key login, HttpOnly SameSite=Strict cookie, CSRF token and same-origin checks, login rate limit, live SSE updates, one hour of history, rooms/participants/tracks drill-down with actions, track quality, connectivity, recordings with hls.js playback, alerts. Backend tests run under `-race`; the frontend has typecheck, vitest and a dist drift check. Grafana dashboard JSON is included. | No browser e2e for the dashboard; history is lost on restart; any configured API key gets full admin rights (no roles); verified against synthetic stats more than real rooms. |

Also present: a Twirp-style admin API (JSON and protobuf) with per-RPC grants, signed webhooks
with retries and ordered queues, Prometheus metrics, a `/stats` JSON endpoint, docker-compose
with MinIO/Prometheus/Grafana (whether it has been built and started is session E's result),
and 11 ADRs.

## Tests and CI

CI (`.github/workflows/ci.yml`) has eight jobs: `proto`, `go` (build, vet, golangci-lint,
`go test -race ./...`, one benchmark iteration), `js`, `java`, `e2e` (Playwright, Chromium and
Firefox), `recorder` (FFmpeg-backed, fails if any test is skipped), `go-e2e` (whole `test/e2e`
suite under `-race` with ffmpeg) and `dashboard-web`.

State at the start of this session (run 45, `2ae7915`): six jobs green, two red.

- `go`: `TestFullSystem` failed ("sub0 receives media on 4 of 6 tracks"). **Root cause:** the `go`
  job has no ffmpeg. Without ffmpeg the recorded H.264 + Opus publisher in the test sent no packets
  at all, so every subscriber saw two silent tracks. The race detector had nothing to do with it;
  `go-e2e` passed only because it installs ffmpeg. **Fix:** without ffmpeg the test now sends
  synthetic H.264 IDR and Opus packets (`4564d62`). Verified locally under `-race` with ffmpeg
  hidden from `PATH`, and with the full `go test -race ./...` run.
- `e2e`: three Firefox tests timed out waiting for remote video. **Root cause (SDK):** `Room`
  used `MediaStreamTrack.id` from `ontrack` as the server's track SID. Chromium copies the msid
  track id into `MediaStreamTrack.id`; Firefox generates its own ids, so no Firefox remote track
  ever matched its publication and no video was attached. The server log line
  "subscriber offer failed: InvalidStateError: connection closed" was a side effect: a debounced
  renegotiation ran after the participant closed when the test timed out. **Fix:** the SDK reads
  the msid of the transceiver's `mid` from the remote SDP (`dd53e78`, unit tests in
  `test/sdp.test.ts`). Chromium e2e passes locally (5 passed, 1 skipped). Firefox could not be run
  locally because the session's network policy blocks the Playwright Firefox download.

After the fixes (run 46, `f93ec27`): `go`, `go-e2e`, `recorder`, `js`, `java`, `proto` and
`dashboard-web` are green. In `e2e`, Chromium was 5/5 and Firefox two-way video and bad token
passed for the first time. Two Firefox tests still failed: layer selection (the test expected a
640 px top layer, but Firefox's fake camera is smaller) and reconnect (Playwright's `setOffline`
does not interrupt Firefox's WebSocket/WebRTC traffic). Both were addressed in the tests
(`cb1bd4d`), together with the SDK rejoin fix (`c9ccc0e`) and the re-enabled data-packet test.
Locally, Chromium e2e is 6/6 twice in a row. A separate `bench` workflow (session E) runs the
benchstat regression gate.

Final CI state: see the last section of this report.

## Load-test numbers (from [perf/LOADTEST.md](perf/LOADTEST.md) and [perf/WP1.md](perf/WP1.md))

The server and the load tester shared one 4 vCPU container (loopback), so these are lower bounds
for the server and do **not** confirm the capacity targets (250 meeting participants / 2,000
webinar subscribers per 4 vCPU). Synthetic Opus plus 3-layer VP8 simulcast; CPU is in % of one
core.

| Scenario | Clients (PCs) | Server egress | Server CPU | RSS | Loss | Latency p50 / p99 | Join failures |
|----------|---------------|---------------|------------|-----|------|-------------------|---------------|
| 1 room, 2 pub + 10 sub | 12 (14) | 2.5k pkt/s, 5.2 Mbit/s | 10% | 48 MB | 0.00% | 0.39 / 0.92 ms | 0 |
| 1 room, 2 pub + 100 sub | 102 (104) | 22.5k pkt/s, 65 Mbit/s | 36% | 141 MB | 0.00% | 0.72 / 2.44 ms | 0 |
| 10 rooms × 4 pub | 40 (80) | 13.4k pkt/s, 24 Mbit/s | 48% | 298 MB | 0.00% | 0.27 / 0.89 ms | 0 |
| 50 rooms × 4 pub | 200 (400) | 63.7k pkt/s, 111 Mbit/s | 132% | 1.35 GB | 0.00% | 0.30 / 4.2 ms | 0 |

Profile under load (50 × 4): one `sendto` syscall per packet is the top hotspot (23% of CPU).
AES-GCM SRTP is only about 1–2%, and Tessera's own code is under 5%. Memory is dominated by
receive ring slabs (66% of heap). Micro-benchmarks: `BenchmarkDownTrackWriteRTP` reaches 486k
pkts/s/core with SRTP, with 0 allocs/op on the forwarding path. Session E added a benchstat
regression gate (`.github/workflows/bench.yml`), and the Docker stack (tessera, web demo,
Prometheus, Grafana) came up healthy; the recorder image and MinIO were not verified (see WP5 in
PROGRESS.md and `deploy/README.md`).

## Review findings (session D)

Fixed, each with a regression test:

| Severity | Area | Finding | Commit |
|----------|------|---------|--------|
| High | Browser SDK | Remote tracks matched by `MediaStreamTrack.id`, so no remote media at all in Firefox | `dd53e78` |
| High | Browser SDK | A full rejoin that needed more than one attempt came back with no published tracks: the republish list was rebuilt from state the failed attempt had already cleared (the "flaky" reconnect e2e) | `c9ccc0e` |
| Medium | Forwarding | `Receiver.AddDownTrack` checked `closed` outside `dtMu`. A DownTrack added while `Close` ran was never closed, which leaked the subscription and its RTCP goroutine | `f93ec27` |
| Medium | Admin API auth | `CreateRoom`/`DeleteRoom` ignored the token's room scope: a `room_create` grant scoped to room A could delete room B | `1f06c29` |
| Low | Tests/CI | `TestFullSystem` sent no media without ffmpeg | `4564d62` |
| Low | Dashboard | The failed-login map only pruned the requesting address, so addresses that failed once were kept forever (slow growth under login spraying) | see git log |
| Low | Tests | Browser data-packet e2e was skipped although the server implements forwarding; enabled (it repeats the send until the subscriber's channel is open) | `cb1bd4d` |
| Low | Docs/dev | Dashboard dev proxy pointed at the metrics port; PROTOCOL.md described 429 as rate limiting | `8628c46`, `005ff12` |

Reviewed with no high-severity issue found: JWT verification (fixed algorithm, constant-time
HMAC compare, `exp` required, nbf/exp leeway), per-RPC grants and room scoping for the
participant and recording RPCs, webhook signing (`t=..,v1=..` HMAC-SHA256, tolerance window,
multiple signatures for rotation), dashboard cookie flags, CSRF token and Origin checks, the
RTP header parser's bounds (CSRC, extensions, padding), the ring buffer's duplicate and too-old
handling, VP8 descriptor rewrite buffer sizes, NACK retransmit throttling, and the recorder's
handling of DTS going backwards or jumping (timeline cut with a discontinuity).

Logged, not fixed (lower severity):

1. **Dashboard login rate limit is per `RemoteAddr`.** Behind a reverse proxy every user shares
   the proxy's IP, so five bad logins lock everyone out for a minute. It should use a trusted
   `X-Forwarded-For` setting.
2. **Any API key gets full dashboard admin rights.** There are no roles or read-only keys.
3. `buffer.Buffer.Release` is never called, so ring slabs are never returned to the pool. The GC
   frees them, but the pool does nothing, and churn costs extra allocations.
4. TWCC feedback decoding allocates per feedback (`symbols` slices per chunk). This is not on the
   per-packet path, but it adds GC pressure with many subscribers.
5. `nackQueue.addRange` shifts the whole array per entry when full (up to 256×256 copies on a
   large gap). Cheap in absolute terms but O(n²).
6. `Forwarder.PaddingParams` does not record padding SNs in the NACK history, so a NACK for a
   padding SN could map to a stale packet. This is latent: padding probes are not enabled.
7. `transport.createOffer` can race with `Close` and log "subscriber offer failed: connection
   closed" (harmless noise; the log should be suppressed when the transport is closed).
8. Join tokens are accepted in the `access_token` query parameter (needed by browsers). Reverse
   proxies may log them, so keep TTLs short and prefer the `Authorization` header for non-browser
   clients.
9. **Data packets sent before a subscriber's data channel is open are dropped**, including
    `reliable` ones; there is no per-subscriber buffering. Right after a join a message can be lost.
10. Two layer goroutines translate under the forwarder lock but write under the DownTrack lock,
    so packets can be reordered by one position around a simulcast switch (receivers tolerate it).

## Known issues (carried over, still open)

- Safari never tested; Firefox only through CI.
- `SyncState` answers with participant state only; server-side ICE restart on resume is not
  exercised. Drain sends `leave` with DISCONNECT instead of migrating.
- Room broadcasts between admission and JoinResponse are dropped for that participant (narrow
  window).
- `congestion_control.probe` ignored.
- Recorder: `Room.active_recording` not set; composite recording unsupported.
- Java SDK not on Maven Central; npm package not published (no dry run done).
- Docker images and compose: see session E's results in PROGRESS.md (WP5) and `docs/perf/`.

## Roadmap (prioritised)

1. **Browser coverage**: confirm Firefox green in CI over several runs. Add WebKit to Playwright and
   do a manual Safari pass (H.264 simulcast, Opus-in-fMP4 playback).
2. **Capacity on real hardware**: run the load tester from separate hosts against the reference
   4 vCPU machine to check the 250 / 2,000 targets. Profile under load (SRTP, `sendmmsg`
   batching) and add the benchmark regression gate (session E started this).
3. **Operational hardening**: dashboard rate limit behind proxies, read-only
   dashboard role; TURNS; graceful drain with client migration; padding probes for faster
   bandwidth ramp-up.
4. **Release**: publish the Java SDK (Central) and the npm package; tagged Docker images; a Helm
   chart and runbook.
5. **Recording**: automated hls.js playback test, long soak, composite (grid) recording,
   `active_recording` in room state.
6. **Scale-out**: multi-node with Redis (room placement, signal relay, job queue), then cascading
   relays for very large rooms; later VP9/AV1 SVC, WHIP/WHEP and E2EE.

## Final CI state

**All eight CI jobs are green** in [run 52](https://github.com/KeshavGoyal26/tessera/actions/runs/41968153953)
(`cb1bd4d`, the first fully green run on main). Browser e2e: Chromium 6/6 (two-way video, bad token,
layer selection, data packets, reconnect, screen share). Firefox 4/4 run: two-way video, bad token,
layer selection and data packets pass; reconnect and screen share are skipped. Firefox is still a
required browser in CI; it did not need to be marked allowed-to-fail. Locally, `go test -race ./...`
also passes on the final tree. The later commits (dashboard pruning, docs) run in follow-up CI runs.
