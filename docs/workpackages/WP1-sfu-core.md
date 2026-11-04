# WP1: SFU core

**Goal:** a production-quality single-node SFU: signaling, rooms, the forwarding engine with
simulcast, NACK, PLI, RTCP, TWCC/BWE-driven allocation, speakers, dynacast, TURN, plus the Go
client used by everybody else.

## Owns (exclusive)

`pkg/rtc/**` (`rtc`, `buffer`, `simulcast`, `bwe`, `speaker`, `turn`, `types`), `pkg/room`,
`pkg/signal`, `pkg/auth`, `pkg/config`, `pkg/client`, `cmd/tessera`, `test/e2e/**`
(new: Go end-to-end tests), the `go` job in CI.

## Interfaces

Provided (contracts others build on; extend additively only):

| Interface | Consumers |
|-----------|-----------|
| `room.Admin` | WP3 admin API, WP4 Coordinator |
| `room.Manager` / `signal.SessionHandler` | `cmd/tessera` |
| `client.Client`, `client.RemoteTrack` (satisfies `recorder.RTPSource`) | WP4 Worker, WP5 load tester, E2E |
| `auth.Verifier`, `auth.KeyProvider`, `auth.AccessToken`, `auth.RoomGrant` claim names | WP3, WP4, SDKs |
| `config.Config`, `config.LoadYAML`, `config.NewLogger` | all binaries |
| Signaling behaviour exactly as in `docs/PROTOCOL.md` section 2 | WP2 browser SDK |

Consumed: `webhook.Notifier` (WP3; use `webhook.Nop` in tests), `pkg/errs`, `pkg/metrics`
registry, `gen/go/tessera/v1`.

## Scope

1. **Signaling** (`pkg/signal`): WebSocket via `github.com/coder/websocket`, token check and
   `Admit` before upgrade, protobuf binary and protojson text framing, bounded write queue,
   ping/pong and timeouts, close codes, per-session rate limit.
2. **Rooms** (`pkg/room`): room lifecycle (auto-create, empty/departure timeouts), participants
   (duplicate identity, permissions, hidden/recorder), track publish/unpublish/mute,
   auto-subscribe and explicit subscriptions, subscriber renegotiation (debounced,
   serialized, transceiver reuse), data packets, resume within `resume_window` and
   `SyncState` reconciliation, drain, webhooks for room/participant/track events, all of
   `room.Admin`.
3. **Engine** (`pkg/rtc`): `SettingEngine` with UDP mux + ICE-TCP mux + NAT 1:1, media engine
   with Opus/RED/VP8/H.264 (+RTX) and header extensions from ADR-0006, TWCC feedback
   interceptor for publishers only, `Transport`, `Receiver`, `DownTrack` per ARCHITECTURE.md.
4. **Buffer**: ring of `ExtPacket` slots, own RTP header parser (no allocation), SN/TS unwrap,
   VP8 descriptor and H.264 keyframe parsing, audio level, loss/jitter stats, NACK generator
   towards publishers, fuzz tests.
5. **Simulcast forwarder**: layer selection, keyframe-gated switching, SN/TS munging, VP8
   picture-id/TL0PICIDX/KEYIDX rewriting, temporal-layer dropping, NACK translation, padding.
6. **RTCP**: NACK responder with throttling and retransmit budget, PLI/FIR aggregation and
   throttling, SR generation per DownTrack from publisher SR mapping, RR to publishers.
7. **BWE and allocator**: TWCC send-side estimator (trendline + loss), probing, per-subscriber
   allocator with priorities, hysteresis, pause/resume and `StreamStateUpdate`.
8. **Speakers, connection quality, dynacast** (`SubscribedQualityUpdate`).
9. **TURN** (`pkg/rtc/turn`): pion/turn UDP/TCP/TLS, ephemeral credentials, relay
   restricted to node addresses.
10. **Go client** (`pkg/client`): full `client.Client` including simulcast publish, resume.
11. Metrics for everything above (collectors reading per-track atomics, never per packet).

## Milestones

| ID | Deliverable | Unblocks |
|----|-------------|----------|
| M1 | Signaling + rooms + single-layer audio/video forwarding (no simulcast), `room.Admin` implemented, `create-token`, Go client can publish/subscribe | WP3 live tests |
| M2 | Go client complete (publish simulcast, subscribe, callbacks, `RemoteTrack.Read`), resume, data packets, hidden recorder participants | WP2 E2E, WP4 live jobs |
| M3 | Simulcast forwarder + munging, NACK responder/generator, PLI/FIR, SR/RR, dynacast, speakers | WP5 |
| M4 | TWCC estimator + allocator + probing + pause, connection quality | WP5 BWE scenarios |
| M5 | TURN, ICE-TCP, NAT 1:1 verified, drain/migration, hardening and fuzzing | release |

## Acceptance criteria

- `go test -race ./...` green; `golangci-lint` clean.
- Benchmarks with `-benchmem` showing **0 allocs/op**: `BenchmarkBufferWrite`,
  `BenchmarkForwarderGetTranslation`, `BenchmarkDownTrackWriteRTP`,
  `BenchmarkReceiverFanout/{1,10,100,1000}`. Throughput of `BenchmarkDownTrackWriteRTP`
  (with SRTP) ≥ 100k packets/s/core on the CI runner class (report numbers in PROGRESS).
- Fuzz targets for RTP header parsing, VP8 descriptor, H.264 keyframe detection, RTCP
  handling run ≥ 1 min each without findings (`go test -fuzz`, locally; seeds checked in).
- Protocol behaviour matches `docs/PROTOCOL.md`; deviations require a `docs:` commit first.

## Required tests

- Unit: table-driven munging tests (SN wrap at 65535, TS wrap, switch up/down, loss across a
  switch, out-of-order, duplicate, VP8 7→15-bit picture id, temporal drop), NACK
  translation, PLI throttling, allocator decisions for synthetic estimates, speaker
  hysteresis, token/grant checks, config validation.
- **E2E** in `test/e2e` (build tag none; uses real Pion clients via `pkg/client` and the real
  server on loopback with random ports):
  1. two participants publish audio+video and receive each other's media (RTP payload
     integrity check);
  2. **simulcast switching**: publisher sends 3 layers with synthetic VP8 keyframes; a
     subscriber requests LOW then HIGH via `UpdateTrackSettings`; assert continuous SN,
     monotonic TS and continuous picture ids at the receiver across switches;
  3. **NACK**: drop selected packets on the subscriber path (Pion interceptor or packet-drop
     shim), assert retransmissions arrive with the original munged SN and payload;
  4. **PLI**: subscriber sends PLI; publisher observes exactly one PLI within the throttle
     window even with N subscribers sending PLIs;
  5. **BWE downgrade**: constrain the subscriber link (shaped `net.PacketConn` wrapper in the
     SettingEngine) to below HIGH bitrate; assert the allocator moves to a lower layer within
     5 s and `StreamStateUpdate` is sent when even LOW does not fit;
  6. resume after WebSocket drop without new track SIDs; duplicate identity kick;
  7. permissions: `can_publish=false` cannot publish, hidden participants are invisible.
- E2E tests must run in < 3 minutes total under `-race`.
