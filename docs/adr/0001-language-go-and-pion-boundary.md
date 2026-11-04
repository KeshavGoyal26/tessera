# ADR-0001: Go, and Pion only for low-level protocols

- Status: accepted
- Date: 2026-11-04
- Deciders: Tessera maintainers

## Context

Tessera is a WebRTC SFU that must forward a very large number of packets per second with
predictable latency, run many connections per node, and stay easy to operate. Existing
servers show the design space:

| Project | Language | Notes relevant to us |
|---------|----------|----------------------|
| mediasoup | C++ worker + Node/Rust control | Fastest per core; worker-per-core model; complex build |
| Janus | C, plugin architecture | Very mature, manual memory management, plugin model is rigid |
| Kurento / OpenVidu (legacy) | C++ / GStreamer | Media-pipeline model; heavy CPU per stream; transcoding-friendly |
| LiveKit | Go on Pion | Proven at scale; own SFU layer on top of Pion transports |
| ion-sfu | Go on Pion | Simple, readable; showed the cost of naive per-packet allocations |
| Pion | Go WebRTC stack | Spec-complete transports; its high-level `TrackLocalStaticRTP` is not built for SFU fan-out |

## Decision

1. The server, recorder and load tester are written in **Go**, latest stable (Go 1.27.x at the
   time of writing; `go.mod` pins `go 1.27.0` and `toolchain go1.27.3`). CI uses the
   version from `go.mod`.
2. **Pion is used only for low-level protocol machinery**:
   `pion/webrtc/v4` (PeerConnection, SDP, transceivers, `SettingEngine`), `pion/ice/v4`,
   `pion/dtls/v3`, `pion/srtp/v3`, `pion/sctp`, `pion/rtp`, `pion/rtcp`, `pion/interceptor`
   (only the TWCC *feedback generator* towards publishers and header-extension plumbing) and
   `pion/turn/v4`.
3. **Everything above that is written from scratch** in this repository:
   signaling, rooms and participants, the forwarding engine (`Receiver` and `DownTrack`),
   per-track packet ring buffers, the NACK responder, PLI/FIR handling and throttling,
   RTCP SR/RR generation, simulcast layer selection with RTP sequence-number, timestamp and
   VP8 picture-id / tl0picidx rewriting, H.264 keyframe detection, the send-side bandwidth
   estimator driven by TWCC feedback, the per-subscriber stream allocator, active-speaker
   detection from the RFC 6464 audio-level extension, and dynacast.
4. We **do not** use Pion's `TrackLocalStaticRTP`, its NACK responder/generator interceptors,
   its `gcc`/pacer interceptors or its report interceptors on the forwarding path. Each
   `DownTrack` implements `webrtc.TrackLocal` itself and writes through the bound
   `TrackLocalWriter`, so one forwarded packet costs one header rewrite and one SRTP
   encryption with no intermediate channel hops.
5. Design inspiration may come from the projects in the table above. **No code is copied**;
   contributors must not paste code from them (licences differ and the point is to own
   every line of the hot path).

## Consequences

- Go gives us fast iteration, a single static binary, great profiling (pprof, execution
  traces) and easy concurrency. The price is GC: the hot path must be allocation-free in
  steady state (pooled buffers, value types, no closures or interfaces boxed per packet).
  This is a hard, benchmark-enforced rule (see ARCHITECTURE.md, "Performance targets").
- Owning the SFU layer means owning its bugs. The forwarding engine needs extensive unit
  tests (munging edge cases: wrap-around, layer switches, out-of-order and lost packets) and
  end-to-end tests with real Pion clients (WP1).
- Pion upgrades are low-risk because we touch only stable low-level APIs.

## Alternatives considered

- **Rust (webrtc-rs or str0m)**: excellent performance and no GC, but slower team velocity,
  less mature WebRTC stack, and a smaller hiring pool. Rejected for now.
- **C++ (mediasoup-style worker)**: best raw throughput, but a two-language system with a
  hard build and memory-safety risks. Rejected.
- **Building on Pion high-level APIs only**: ion-sfu showed that this leads to per-packet
  allocations and limited control over munging and RTCP. Rejected.
- **Forking LiveKit**: violates the "from scratch" requirement.
