# ADR-0003: WebSocket + protobuf signaling with resume

- Status: accepted
- Date: 2026-11-04
- Deciders: Tessera maintainers

## Context

WebRTC leaves signaling to the application. Options include JSON over WebSocket (Janus,
mediasoup apps), protobuf over WebSocket (LiveKit), HTTP + SSE, and WHIP/WHEP. We need a
schema that is shared by Go, TypeScript and Java, evolves safely, is compact, and supports
reconnecting without tearing down media.

## Decision

1. **Transport**: one WebSocket per participant at `GET /rtc` (TLS terminated by Tessera or
   a proxy). Every frame is one **binary** protobuf message: `tessera.v1.SignalRequest`
   (client to server) or `tessera.v1.SignalResponse` (server to client). For debugging, a
   client may pass `format=json` and exchange text frames carrying the canonical protojson
   encoding of the same messages.
2. **Schema**: defined in `proto/tessera/v1/signal.proto` and `models.proto`, built with
   **buf** (lint `STANDARD`, breaking-change check `FILE` against `main`). Generated Go code is
   checked in under `gen/go/tessera/v1`; TypeScript (protobuf-es) is checked in under
   `sdk/js/src/gen`; Java is generated at build time by Maven from `proto/`.
3. **Versioning**: the protobuf package is `tessera.v1`. A separate integer **protocol
   version** (`protocol` query parameter, currently `1`) gates behaviour changes that are
   compatible on the wire but not semantically (for example a new negotiation step). The
   server advertises its supported range in `ServerInfo`; clients sending an unsupported
   version get HTTP 426 before upgrade. Breaking schema changes require `tessera.v2`.
4. **Authentication**: the access token (ADR-0004) is passed as the `access_token` query
   parameter (browsers cannot set WebSocket headers) or as `Authorization: Bearer` for
   non-browser clients. The server validates the token *before* upgrading.
5. **Correlation**: every `SignalRequest` may carry a client-chosen `request_id`. Requests that
   can fail are answered with `RequestResponse{request_id, reason}`; errors not tied to a
   request use `ErrorResponse`.
6. **Liveness**: the client sends `Ping` every `ping_interval` (from `JoinResponse`); the
   server answers with `Pong` and closes the session after `ping_timeout` without pings.
7. **Reconnect and resume**: two levels.
   - *Resume* (signal connection lost, media path may still be fine): the client reconnects
     with `reconnect=1&sid=<participant sid>`. The server keeps a disconnected participant for
     `resume_window` (default 15 s), replies `ReconnectResponse`, the client performs an ICE
     restart on whichever PC is unhealthy and sends `SyncState` (current SDPs, subscriptions,
     published tracks). The server reconciles and resumes without new track SIDs.
   - *Full reconnect* (resume window expired, node lost, `LeaveRequest{action=RECONNECT}`):
     the client joins again from scratch and republishes; the server treats it as a new
     session for the same identity.
   - A server can tell clients to move (`LeaveRequest{action=RESUME|RECONNECT}`) during
     drains and deploys.
8. **Ordering**: messages on one WebSocket are processed in order per participant. Server
   writes go through one bounded per-session queue (writes never block room goroutines; a
   full queue closes the session with `ErrorResponse{code=SLOW_CONSUMER}`).

Full message flow and semantics: [PROTOCOL.md](../PROTOCOL.md).

## Consequences

- One schema drives all SDKs; buf breaking-change checks protect clients in the field.
- Binary protobuf frames are smaller and cheaper to parse than JSON. JSON mode keeps
  debugging easy.
- Resume keeps calls alive across brief network changes (Wi-Fi to cellular) without
  re-subscribing every track.

## Alternatives considered

- **JSON-only signaling**: easy to read but no shared schema and no compatibility tooling.
- **WHIP/WHEP**: great for ingest/playback interop but has no room model, no events and no
  subscriptions. May be added later as an extra ingress/egress, not as the primary protocol.
- **HTTP/2 + SSE**: two channels to correlate, weaker browser support for bidirectional use.
