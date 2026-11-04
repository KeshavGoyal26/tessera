# ADR-0002: Two PeerConnections per participant

- Status: accepted
- Date: 2026-11-04
- Deciders: Tessera maintainers

## Context

A participant both publishes and subscribes. With a single PeerConnection, both client
(publishing a new track) and server (a new remote track appeared) need to renegotiate, which
causes offer/answer **glare**. "Perfect negotiation" solves glare but complicates server
state machines, and some browsers historically behaved inconsistently during rollback.
Janus uses one PC per plugin handle; mediasoup leaves it to the app (usually two
transports); LiveKit uses two PeerConnections.

## Decision

Each participant has exactly two PeerConnections:

| PC | Direction | Who offers | Contents |
|----|-----------|-----------|----------|
| **publisher** | client → server | **client** | `sendonly` transceivers for the participant's tracks, plus the reliable and lossy data channels |
| **subscriber** | server → client | **server** | `sendonly` (from the server's view) transceivers for subscribed tracks |

Rules:

1. Offers on the publisher PC only come from the client; offers on the subscriber PC only
   come from the server. There is no glare by construction.
2. The subscriber PC is created when the client joins with `can_subscribe`; the publisher PC
   is created lazily on first publish (`JoinResponse.fast_publish` lets the client create it
   eagerly to shave one RTT when it knows it will publish).
3. Server-side renegotiation of the subscriber PC is **debounced** (default 50 ms) and
   **serialized**: at most one offer is outstanding; changes made while waiting for an
   answer are folded into the next offer.
4. Transceivers on the subscriber PC are **reused** after unsubscribe (set to `inactive`, then
   recycled for the next track of the same kind) to keep SDP size bounded in large rooms.
5. Both PCs share the same ICE/DTLS settings and the same single UDP port (ADR-0005). ICE
   restarts are handled per PC (`SyncState` describes both during resume, ADR-0003).
6. When a client cannot publish (`can_publish=false`), only the subscriber PC exists.
   Hidden recorder participants (ADR-0007) never create a publisher PC.

## Consequences

- Simple server state machines: the server is always the offerer on one PC and always the
  answerer on the other.
- Two DTLS handshakes and two ICE sessions per participant (a few kB and ~1 RTT more at
  join). Acceptable; mitigated by `fast_publish` and by running both handshakes in parallel.
- Bandwidth estimation is per direction anyway (TWCC on the subscriber PC drives the
  per-subscriber allocator; TWCC feedback on the publisher PC feeds the browser's GCC).

## Alternatives considered

- **Single PC with perfect negotiation**: fewer handshakes but complex rollback handling and
  harder debugging. Rejected.
- **One PC per track**: excessive resource use (ports, DTLS handshakes). Rejected.
