# ADR-0005: Single UDP port, ICE-TCP fallback, NAT 1:1 and embedded TURN

- Status: accepted
- Date: 2026-11-04
- Deciders: Tessera maintainers

## Context

Media servers often run in containers and clouds behind 1:1 NAT, and clients sit behind
restrictive firewalls. A port range per connection (Janus/Kurento default) is painful to
open in firewalls and Kubernetes. Some enterprise networks block all UDP.

## Decision

1. **Single UDP port mux** (default `7882/udp`): all ICE/DTLS/SRTP traffic of all
   PeerConnections on the node is demultiplexed by ICE ufrag/remote address using
   `ice.NewMultiUDPMuxDefault` bound to every configured interface address. Optionally a
   port range may be configured instead (debugging only).
2. **ICE-TCP passive candidates** on a single port (default `7881/tcp`) via
   `ice.NewTCPMuxDefault`; TCP candidates get lower priority so they are only used when UDP
   fails. RFC 4571 framing.
3. **NAT 1:1 IPs**: `rtc.node_ip` (explicit) or `rtc.use_external_ip` (discover via STUN at
   start-up) is advertised as host candidates (`SetNAT1To1IPs` with `ICECandidateTypeHost`),
   so no server-reflexive gathering per session is needed. IPv4 and IPv6 are both supported.
4. **ICE-lite is not used**; the server runs full ICE so that it works behind NAT 1:1 and
   with ICE-TCP, but candidate gathering is limited to the mux sockets (no per-session
   ports, no mDNS on the server).
5. **Embedded TURN** (`pion/turn/v4`), disabled by default, enabled with `turn.enabled`:
   - `turn/udp` on 3478, `turn/tcp` on 3478, `turns` (TLS over TCP) on 5349 or 443
     (when a dedicated IP is available, 443 traverses the most firewalls).
   - Credentials are **ephemeral**: per-session username `<expiry>:<participant sid>` and
     HMAC-SHA1 password derived from a TURN secret (RFC 7635-style REST credentials), sent in
     `JoinResponse.ice_servers`. The TURN relay only permits peers that are this node's own
     media addresses (no open relay).
   - External TURN servers may be configured instead or in addition.
6. Socket tuning: large `SO_RCVBUF`/`SO_SNDBUF` (default 25 MB, configurable), and on Linux
   `recvmmsg`/`sendmmsg` batching via `golang.org/x/net/ipv4` in a later optimisation
   milestone (WP5), behind the same mux interface.
7. Default ports summary: `7880/tcp` HTTP (signal, admin API, metrics may be split off),
   `7881/tcp` ICE-TCP, `7882/udp` media, `3478/udp+tcp` TURN, `5349/tcp` TURNS.

## Consequences

- Firewall and Kubernetes configuration is three or four ports regardless of load.
- One UDP socket per interface becomes a potential bottleneck under very high packet rates;
  mitigated by `SO_REUSEPORT` sharding over N sockets (WP5) and batching.
- Embedded TURN shares CPU with the SFU; very large deployments should run TURN separately.

## Alternatives considered

- **Port range per PeerConnection**: classic, simple, but firewall-hostile. Optional only.
- **ICE-lite**: cheaper, but breaks some NAT 1:1 and TCP scenarios.
- **Mandatory external coturn**: one more component to deploy for small installations.
