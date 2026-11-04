# Tessera

Tessera is a WebRTC SFU (Selective Forwarding Unit) media server written from scratch in Go. It
records to HLS and comes with a Java server SDK and a TypeScript browser SDK.

> **Status: Phase 0 complete (architecture, contracts, scaffolding).** The protocol, interfaces
> and project layout are in place, and the components are being implemented in parallel
> work packages. Most server features currently return `not implemented`. See
> [docs/PROGRESS.md](docs/PROGRESS.md).

## Features (target for v1)

- **SFU for many feeds per room**: many publishers and subscribers per room, two PeerConnections
  per participant, no glare.
- **Browsers**: Chrome, Edge, Firefox and Safari (latest two versions).
- **Codecs**: Opus (DTX, in-band FEC, optional RED), VP8 and H.264, with simulcast for both.
- **Adaptive forwarding**: simulcast layer switching with RTP sequence-number, timestamp and
  VP8 picture-id rewriting; NACK from per-track ring buffers; PLI/FIR throttling; RTCP SR/RR;
  TWCC send-side bandwidth estimation driving per-subscriber layer allocation; dynacast;
  active speaker detection.
- **Networking**: one UDP port for all media, ICE-TCP fallback, NAT 1:1, embedded TURN
  (UDP/TCP/TLS).
- **Recording**: a separate `tessera-recorder` service that joins rooms as a hidden subscriber
  and writes fMP4/CMAF HLS. H.264 and Opus are remuxed without transcoding; AAC mode and VP8
  sources go through FFmpeg. Output goes to local disk or S3-compatible storage.
- **Integration**: HS256 JWT access tokens; a Twirp-style admin API (JSON or protobuf);
  HMAC-signed webhooks; a Java 17 SDK whose only runtime dependency is protobuf-java; a small
  framework-free ESM browser SDK.
- **Operations**: `log/slog`, Prometheus metrics, pprof, health and readiness endpoints,
  graceful drain, Docker images and docker-compose.
- **Performance targets**: at least 100k forwarded packets/s per core, zero allocations per
  forwarded packet, under 2 ms added p99 latency, and at least 250 meeting participants per
  4-vCPU node. See [ARCHITECTURE.md §9](docs/ARCHITECTURE.md#9-performance-targets).

## Quick start (development)

```bash
# Server (Go 1.27; the toolchain is downloaded automatically if needed)
go run ./cmd/tessera --dev              # HTTP :7880, media :7882/udp, metrics :6789
go run ./cmd/tessera create-token --room demo --identity alice

# Browser SDK and demo
cd sdk/js && npm ci && npm run build && cd ../../examples/web-demo && npm ci && npm run dev

# Java SDK
cd sdk/java && mvn -B verify

# Full stack (server, recorder, MinIO)
cp deploy/.env.example deploy/.env
docker compose -f deploy/docker-compose.yml --env-file deploy/.env up --build
```

Java usage preview:

```java
String token = AccessToken.builder()
    .apiKey("devkey").secret(secret)
    .identity("alice")
    .grants(RoomGrant.builder().room("demo").roomJoin(true).build())
    .build()
    .toJwt();
```

See [sdk/java/README.md](sdk/java/README.md) for the exact API.

## Repository map

| Path | Contents | Owner |
|------|----------|-------|
| `cmd/tessera` | SFU server binary | WP1 |
| `cmd/tessera-recorder` | HLS recorder binary | WP4 |
| `cmd/tessera-loadtest` | Load generator | WP5 |
| `pkg/rtc/...` | SFU engine: transports, receivers, downtracks, buffer, simulcast, bwe, speaker, turn | WP1 |
| `pkg/room`, `pkg/signal`, `pkg/auth`, `pkg/config`, `pkg/client` | Rooms, WebSocket signaling, tokens, config, Go client | WP1 |
| `pkg/admin`, `pkg/webhook` | Twirp admin API, webhooks | WP3 |
| `pkg/hls`, `pkg/recorder`, `pkg/storage` | fMP4/HLS muxer, recording coordinator and worker, storage | WP4 |
| `pkg/metrics`, `pkg/errs`, `pkg/version` | Metrics/health/pprof, shared errors, version | shared |
| `proto/tessera/v1` | Protobuf schemas (signaling, admin, recording, webhooks) | shared |
| `gen/go` | Generated Go protobuf code | shared |
| `sdk/js` | `@tessera/client` browser SDK | WP2 |
| `sdk/java` | Java server SDK | WP3 |
| `examples/web-demo`, `examples/java-spring` | Demo apps | WP2, WP3 |
| `deploy/` | Dockerfile, docker-compose, example configs | WP5 |
| `docs/` | Architecture, ADRs, protocol, work packages, progress | shared |

## Documentation

- [Architecture](docs/ARCHITECTURE.md): components, hot path, memory, scaling, performance targets
- [Protocol](docs/PROTOCOL.md): signaling, admin API, webhooks, recorder worker protocol
- [Architecture decision records](docs/adr/README.md)
- [Work packages](docs/workpackages/README.md) and [progress](docs/PROGRESS.md)

## Development

`make lint test-race` (Go), `make js`, `make java`, `make proto` (buf + protoc-gen-go +
protoc-gen-es). CI runs the same checks on every push to `main`.

Tessera takes design inspiration from Kurento, Janus, OpenVidu, LiveKit, Pion, mediasoup and
ion-sfu. It contains no code from them.

## License

Apache-2.0 (to be confirmed by the owner; a LICENSE file will be added).
