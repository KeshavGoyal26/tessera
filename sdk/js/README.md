# @tessera/client

Browser SDK for [Tessera](../../README.md): join a room, publish camera, microphone and screen
share (one H.264 stream per track), subscribe on demand, and get active
speaker, connection quality and stats. Framework free; the only runtime dependency is
`@bufbuild/protobuf`. About 38 kB gzipped including generated protobuf code (budget 60 kB,
`npm run size`).

Works in current Chrome, Edge, Firefox and Safari. Protocol: [docs/PROTOCOL.md](../../docs/PROTOCOL.md),
design: [ADR-0009](../../docs/adr/0009-browser-sdk.md).

## Quick start

```ts
import { Room, RoomEvent } from "@tessera/client";

// `token` comes from your backend (see sdk/java or `tessera create-token`); the SDK never mints tokens.
const room = new Room();
room
  .on(RoomEvent.TrackSubscribed, (track) => document.body.append(track.attach()))
  .on(RoomEvent.TrackUnsubscribed, (track) => track.detach().forEach((el) => el.remove()))
  .on(RoomEvent.ActiveSpeakersChanged, (speakers) => console.log(speakers.map((p) => p.identity)));

await room.connect("wss://sfu.example.com", token);
await room.localParticipant.setCameraEnabled(true);
await room.localParticipant.setMicrophoneEnabled(true);
```

A runnable app lives in [examples/web-demo](../../examples/web-demo).

## API reference

### `Room`

| Member | Description |
|--------|-------------|
| `new Room(options?)` / `Room.connect(url, token, options?)` | `options`: `autoSubscribe` (true), `videoPreset` (`VideoPresets.h360`), `maxReconnectAttempts` (10) |
| `connect(url, token)` | Opens signaling and creates the publisher and subscriber PeerConnections. `ws(s)://` and `http(s)://` URLs work. Throws `ConnectionError` (`not_allowed`, `server_unreachable`, `unsupported_protocol`, `not_found`, `timeout`, ...) |
| `disconnect()` | Sends leave, stops published tracks, releases everything |
| `state` | `ConnectionState`: `disconnected`, `connecting`, `connected`, `reconnecting` |
| `localParticipant`, `remoteParticipants` (by identity), `activeSpeakers` | |
| `name`, `sid`, `metadata`, `isRecording` | Room state |
| `getStats()` | `{ publisher, subscriber, signalRttMs }`, each a `StatsSummary` with `send`/`receive` `{ bitrate, packets, packetsLost, lossPercent, jitterMs, fps, width, height }`, `rttMs`, `availableOutgoingBitrate`. Call about once a second; bitrate needs two samples |

### Events (`room.on(RoomEvent.X, handler)`)

`ParticipantConnected(p)`, `ParticipantDisconnected(p)`, `TrackPublished(pub, p)`,
`TrackUnpublished(pub, p)`, `TrackSubscribed(track, pub, p)`, `TrackUnsubscribed(track, pub, p)`,
`TrackMuted(pub, p)`, `TrackUnmuted(pub, p)`, `ActiveSpeakersChanged(speakers)`,
`ConnectionQualityChanged(quality, p)`, `TrackStreamStateChanged(pub, active, p)` (server paused or
resumed video for lack of bandwidth), `LocalTrackPublished`, `LocalTrackUnpublished`,
`DataReceived(payload, p, topic)`, `RoomMetadataChanged(metadata)`, `ParticipantMetadataChanged(p)`,
`RecordingStatusChanged(isRecording)`, `Reconnecting`, `Reconnected`, `Disconnected(reason)`,
`ConnectionStateChanged(state)`, `TrackSubscriptionFailed(trackSid, error)`.

### `LocalParticipant`

```ts
await lp.setCameraEnabled(true);          // capture + publish one 360p H.264 stream; false mutes
await lp.setMicrophoneEnabled(true);      // false mutes (the track stays published)
await lp.setScreenShareEnabled(true);     // getDisplayMedia; the browser's "stop sharing" unpublishes
await lp.switchActiveDevice("videoinput", deviceId);   // also "audioinput", "audiooutput"
await lp.publishTrack(new LocalTrack(mediaStreamTrack, TrackSource.Camera), { videoPreset: VideoPresets.h360 });
await lp.unpublishTrack(track);
await lp.publishData(new TextEncoder().encode("hi"), { topic: "chat", reliable: true, destinationIdentities: ["bob"] });
await lp.updateInfoOnServer({ name: "Alice", metadata: "{}" });   // needs the can_update_own_metadata grant
```

Video is published as one H.264 stream per track, scaled down to the preset (default
`VideoPresets.h360`: 640x360, 20 fps, 450 kbit/s). There is no simulcast (ADR-0012): every
subscriber receives that stream, and the server pauses it for a subscriber whose bandwidth
cannot carry it.

### Subscribing

Tracks arrive via `TrackSubscribed`; `track.attach(element?)` renders them. Control on a
`RemoteTrackPublication`: `setEnabled(false)` pauses a track without unsubscribing (for example a
hidden tile), `setSubscribed(true | false)` subscribes on demand (with `autoSubscribe: false`).

### Reconnect

On network loss the SDK reconnects with exponential backoff (300 ms doubling to 5 s): first it
resumes the session (`reconnect=1&sid=...`) and sends `SyncState`, restarting ICE if needed; if
the server no longer knows the session it rejoins and republishes your tracks. Events:
`Reconnecting`, then `Reconnected` or `Disconnected`. Fresh tokens sent by the server are used
for later reconnects.

## Development

```sh
npm ci
npm run generate   # regenerate src/gen from ../../proto (buf)
npm run lint && npm run typecheck && npm test
npm run build && npm run size
npm run e2e        # needs Go (starts `tessera --dev`) and Playwright browsers
```

End-to-end tests (`e2e/`) build the demo, start `go run ./cmd/tessera --dev` and drive two
browsers with fake media: video both ways, mute, stats, leave and
reconnect after `setOffline`. Chromium and Firefox projects exist; set `PW_CHROMIUM_PATH` to use a
preinstalled Chromium and `TESSERA_E2E_URL` to target a running server. The data-packet test
needs server support (`TESSERA_E2E_DATA=1`).
