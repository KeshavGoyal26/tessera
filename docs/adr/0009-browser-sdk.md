# ADR-0009: TypeScript browser SDK

> **Amended 2026-11-08 (ADR-0012):** simulcast, dynacast and adaptive stream are removed from the
> SDK on `main`: it publishes one H.264 stream per track (default 360p) and subscribes on demand.

- Status: accepted
- Date: 2026-11-04
- Deciders: Tessera maintainers

## Context

Browser integrators need a small, framework-free SDK that hides the two-PeerConnection
model, simulcast configuration, adaptive streaming and reconnection.

## Decision

1. Package `@tessera/client` in `sdk/js`: TypeScript (strict), **ESM only**, target ES2022,
   no framework, built with `tsc` (library) and `vite` (demo). Single runtime dependency:
   `@bufbuild/protobuf` (protobuf-es v2). Budget: **< 60 kB minified + gzipped** including
   generated protobuf code (checked in CI by WP2).
2. Generated code from `proto/` lives in `sdk/js/src/gen` (checked in, regenerated with
   `npm run generate`, which runs `buf generate` with `protoc-gen-es` from devDependencies).
3. Public surface (event-emitter based, typed events):
   `Room.connect(url, token, options)`, `room.localParticipant.publishTrack(track, options)`,
   `unpublishTrack`, `setMicrophoneEnabled`, `setCameraEnabled`, `setScreenShareEnabled`,
   `RemoteTrackPublication.setSubscribed / setVideoQuality / setVideoDimensions / setEnabled`,
   events: `participantConnected`, `participantDisconnected`, `trackPublished`,
   `trackSubscribed`, `trackUnsubscribed`, `trackMuted`, `activeSpeakersChanged`,
   `connectionQualityChanged`, `reconnecting`, `reconnected`, `disconnected`.
4. Behaviour owned by the SDK: simulcast encodings (`q/h/f` RIDs) per resolution preset,
   **adaptive stream** (subscribe to the layer matching the rendered element size, pause
   when the element is hidden, using `ResizeObserver` and `IntersectionObserver`),
   **dynacast** (honour `SubscribedQualityUpdate` by toggling `encodings[i].active`),
   codec preference (H.264 first when the server lists it first), resume and full
   reconnect with backoff, ping/pong.
5. Supported browsers: latest two versions of Chrome, Edge, Firefox and Safari. Tests:
   vitest unit tests (jsdom-free where possible) and Playwright E2E against a real Tessera
   server with fake media (`--use-fake-device-for-media-stream` in Chromium,
   `media.navigator.streams.fake` in Firefox). WebKit is tested manually until Playwright's
   WebKit WebRTC support is reliable in CI.
6. Demo app in `examples/web-demo` (vite, plain TS): join form, grid, mute/unmute, layer
   indicators, stats overlay.

## Consequences

- Small bundle and no framework lock-in; React/Vue bindings can be layered later.
- The SDK duplicates some logic of the Go client (`pkg/client`); both are tested against the
  same server behaviour.

## Alternatives considered

- **CommonJS + ESM dual package**: unnecessary for modern bundlers; adds build complexity.
- **Hand-written JSON protocol for the browser**: would drift from `proto/`.
