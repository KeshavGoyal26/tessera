# Architecture Decision Records

One file per decision, numbered, never renumbered. A decision is changed by writing a new
ADR that supersedes the old one (and editing only the old one's `Status` line).

| ADR | Title | Status |
|-----|-------|--------|
| [0001](0001-language-go-and-pion-boundary.md) | Go, and Pion only for low-level protocols | accepted |
| [0002](0002-two-peerconnections-per-participant.md) | Two PeerConnections per participant | accepted |
| [0003](0003-websocket-protobuf-signaling.md) | WebSocket + protobuf signaling with resume | accepted |
| [0004](0004-api-keys-and-jwt-access-tokens.md) | API keys and HS256 JWT access tokens | accepted |
| [0005](0005-networking-udp-mux-tcp-turn.md) | Single UDP port, ICE-TCP, NAT 1:1, embedded TURN | accepted |
| [0006](0006-codecs.md) | Codec set: Opus, VP8 and H.264 simulcast | accepted |
| [0007](0007-recording-separate-binary-hls.md) | Recording as a separate binary producing CMAF HLS | accepted |
| [0008](0008-admin-api-and-java-sdk.md) | Twirp-style admin API and the Java server SDK | accepted |
| [0009](0009-browser-sdk.md) | TypeScript browser SDK | accepted |
| [0010](0010-observability-and-deployment.md) | Observability and deployment | accepted |
| [0011](0011-repository-layout-and-parallel-work.md) | Monorepo layout and ownership rules for parallel work | accepted |
| [0012](0012-edge-tier-and-private-media-servers.md) | Edge tier (SFU + TURN) in front of private media servers | accepted |

Template: [0000-template.md](0000-template.md).
