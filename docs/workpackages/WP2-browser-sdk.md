# WP2: Browser SDK and demo

**Goal:** `@tessera/client`, a small framework-free TypeScript SDK implementing the client side of
`docs/PROTOCOL.md` for Chrome, Edge, Firefox and Safari (latest two versions), plus a demo app.

## Owns (exclusive)

`sdk/js/**` except `sdk/js/src/gen/**` (regenerated only in `feat(proto)` commits),
`examples/web-demo/**`, the `js` job in CI.

## Interfaces

- Consumes: the signaling protocol (`proto/tessera/v1/signal.proto`, `models.proto`,
  `docs/PROTOCOL.md` section 2), token claims (ADR-0004; the SDK never mints tokens).
- Provides: the public SDK API listed in ADR-0009, semver from 0.1.0.
- For E2E: a running `tessera` from WP1 (`go run ./cmd/tessera --dev`) and tokens from
  `go run ./cmd/tessera create-token`.

## Scope

1. `SignalClient`: WebSocket connect (URL builder exists), binary framing, request ids and
   `RequestResponse` promises, ping/pong with RTT, close-code handling, resume parameters.
2. `PCTransport` for publisher and subscriber PCs: offer/answer with `SessionDescription.id`,
   trickle queues, ICE restart, data channels `_reliable` / `_lossy`.
3. `Room` state machine (connecting, connected, reconnecting, disconnected), participant and
   track models from `ParticipantUpdate`, events from ADR-0009.
4. Publishing: `getUserMedia`/`getDisplayMedia` helpers, simulcast encodings (existing
   `computeSimulcastEncodings`), codec preference via `setCodecPreferences` (H.264 first when
   the server lists it first), DTX/stereo/RED options, mute/unmute, dynacast
   (`SubscribedQualityUpdate` → `encodings[i].active`).
5. Subscribing: auto-subscribe, `setSubscribed`, adaptive stream (ResizeObserver +
   IntersectionObserver → `UpdateTrackSettings`), `StreamStateUpdate` handling,
   `attach()/detach()` for media elements.
6. Reconnect: resume with `SyncState`, then full reconnect with exponential backoff and
   republish; token refresh via `refresh_token`.
7. Stats helpers (`getStats` summaries) and connection quality events.
8. Demo: join form, video grid, mic/camera/screen toggles, layer selector, speaker
   highlight, stats overlay.

## Milestones

| ID | Deliverable |
|----|-------------|
| M1 | SignalClient + Room connect/disconnect against WP1 M1, participants list, events (unit tests with a mock WebSocket) |
| M2 | Publish/subscribe audio+video (single layer), mute, demo shows video |
| M3 | Simulcast publish, adaptive stream, dynacast, layer selection in demo |
| M4 | Resume + full reconnect, data packets, screen share, stats |
| M5 | Bundle-size budget, docs site/TSDoc, npm publish dry run |

## Acceptance criteria

- `npm run lint`, `typecheck`, `build`, `test` green in CI; demo builds.
- Bundle size of the ESM build (min+gzip, including generated code actually used) < 60 kB,
  checked by a script in CI.
- No runtime dependencies besides `@bufbuild/protobuf`.

## Required tests

- vitest unit tests: framing, request/response correlation, ping timeout, reconnect state
  machine (fake timers), simulcast encodings, adaptive stream decisions, event emission.
- **Playwright E2E** (`npm run e2e`, Chromium and Firefox, fake media:
  Chromium `--use-fake-device-for-media-stream --use-fake-ui-for-media-stream`, Firefox
  `media.navigator.streams.fake`): starts `tessera --dev` (Go toolchain) as a webServer, two
  pages join the same room, each sees the other's video playing (`videoWidth > 0`, frames
  advancing), mute propagates, leaving updates the other page, simulcast layer switch via
  demo control changes received resolution, reconnect after `page.context().setOffline`.
- E2E job added to CI by WP2 (own job, needs Go and Playwright browsers) once WP1 M2 lands.
