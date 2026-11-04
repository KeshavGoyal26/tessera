# @tessera/client

TypeScript browser SDK for the [Tessera](../../README.md) WebRTC SFU. ESM only, no framework,
one runtime dependency ([`@bufbuild/protobuf`](https://github.com/bufbuild/protobuf-es)).
Design: [ADR-0009](../../docs/adr/0009-browser-sdk.md); wire protocol:
[ADR-0003](../../docs/adr/0003-websocket-protobuf-signaling.md).

> **Status: scaffolding.** The public API, types, protobuf encoding, URL building, simulcast
> layer computation and the event emitter work. Methods that need a server (`Room.connect`,
> publishing, subscriptions) reject with `NotImplementedError` until WP2 lands.

## Usage

```ts
import { Room, RoomEvent, VideoQuality } from "@tessera/client";

const room = await Room.connect("wss://sfu.example.com", token, {
  adaptiveStream: true,
  dynacast: true,
});

room.on(RoomEvent.TrackSubscribed, (track, publication, participant) => {
  document.querySelector("#grid")!.append(track.attach());
});
room.on(RoomEvent.Disconnected, (reason) => console.log("left", reason));

await room.localParticipant.setMicrophoneEnabled(true);
await room.localParticipant.setCameraEnabled(true);

// Manual layer control (otherwise adaptive stream picks layers from element size).
for (const participant of room.remoteParticipants.values()) {
  for (const publication of participant.trackPublications.values()) {
    publication.setVideoQuality(VideoQuality.LOW);
  }
}

await room.disconnect();
```

## Development

```sh
npm ci
npm run lint        # eslint (flat config, typescript-eslint)
npm run typecheck   # tsc --noEmit over src, test, e2e
npm test            # vitest unit tests (test/**/*.test.ts)
npm run build       # tsc -> dist/ (ESM + .d.ts)
npm run e2e         # Playwright (Chromium + Firefox with fake media); placeholder until WP2
```

### Generated protobuf code

`src/gen/tessera/v1/*_pb.ts` is generated from [`proto/`](../../proto) and checked in. After
changing a `.proto` file run:

```sh
npm run generate
```

This runs `buf generate` with [`buf.gen.yaml`](buf.gen.yaml), which uses `protoc-gen-es`
(both from devDependencies) with `target=ts,import_extension=js` and writes to `src/gen`.
Do not edit generated files by hand; they are excluded from linting.

## Browser support

Latest two versions of Chrome, Edge, Firefox and Safari (ES2022, `RTCPeerConnection` with
simulcast, `ResizeObserver`, `IntersectionObserver`). Chromium and Firefox are covered by
Playwright E2E tests; Safari is tested manually.

## License

Apache-2.0
