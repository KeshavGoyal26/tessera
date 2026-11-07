# ADR-0012: Edge tier (SFU + TURN) in front of private media servers

- Status: proposed
- Date: 2026-11-08
- Deciders: Tessera maintainers

## Context

Use case and constraints for production:

- **Roles.** Presenters only publish (camera, mic; every presenter is recorded). Viewers only
  watch. Both belong to a **group** (`groupId`). A viewer may watch any presenter of its group
  on demand, typically **10 at a time out of hundreds**, and switches often. Who watches whom
  changes during the session and groups overlap (supervisors).
- **Scale.** About 10,000 concurrent users across many groups; a group can be larger than one
  server.
- **Compliance.** Internet-facing components must run on machines separate from the media
  servers. The edge may decrypt media (confirmed: the rule is about exposure, not content).
- **Priorities.** Stability on par with LiveKit, simplicity, no transcoding. No deadline.

Today (ADR-0002, single node) every participant connects to the one server hosting the room,
and that server is public. Measured on GCP n2-standard-4: about 450 recorded presenters at 66%
CPU, recording in process (ADR-0007, amended).

Spreading a group's presenters over servers and letting each viewer connect to every server
that holds one of its presenters was rejected: a viewer of 9 servers needs 9 WebSocket and 9
PeerConnections (and 9 TURN allocations behind strict firewalls), its bandwidth is split
between 9 independent congestion controllers, and every switch to a presenter on a new server
costs a new connection (1-3 s).

## Decision

### Components

```
                   public                          |              private
 presenter --WS--> edge: WebSocket proxy ----------|--> media server: receive + record
 presenter ==UDP/TCP/TLS 443==> edge: TURN relay ==|==> media server (only this subnet)
 viewer ----WS---> edge: SFU (signaling)           |
 viewer <==media== edge: SFU <======= relay =======|=== media server (edge subscribes)
                                                   |
 any node <-------------------- Redis (registry, placement, group state) -------> any node
```

1. **Media servers (private).** Today's `tessera`, unchanged in its core: presenters' sessions,
   receive buffers, simulcast, in-process recording, webhooks. No public address.
2. **Edges (public).** Same binary, `role: edge`. Three jobs:
   - **Viewer SFU.** Viewers connect to one edge, with one WebSocket and one subscriber
     PeerConnection, whatever servers their presenters are on. The edge forwards, allocates
     bandwidth (one congestion controller per viewer) and handles NACK/PLI exactly as a media
     server does today.
   - **TURN for presenters.** Embedded TURN (ADR-0005) whose relay permission is the media-server
     subnet only. Presenters are given this edge's TURN credentials and forced to relay
     (`force_relay`), so their media reaches the private media server through the edge.
   - **Signaling proxy for presenters.** The presenter's WebSocket terminates on the edge, which
     proxies it to the media server's private URL (bytes only; the media server stays the
     signaling peer).
3. **Redis.** Never on the media path. A Redis outage stops new joins and new relay
   subscriptions; sessions in progress continue.
4. **HTTPS load balancer** in front of the edges for `/rtc/route` and the admin API.

### Redis state

| Key | Value | Lifetime |
|---|---|---|
| `node:<id>` | role, public URL, private URL, CPU, sessions, recordings, draining | refreshed every 2 s, expires after 10 s |
| `pres:<group>:<identity>` | media node, participant sid, track sids and layers | refreshed by the media node every 10 s, deleted on leave |
| channel `group:<group>` | presenter joined / published / muted / left (the ParticipantInfo, with its version) | pub/sub |
| `rec:<id>` | RecordingInfo, so any node answers Get/List | about 7 days |
| `journal:<id>` | crash journal (moved from local disk), so a surviving node closes recordings of a dead one | until the recording ends |

### Flows

**Presenter join.**

1. The client calls `GET /rtc/route?access_token=...` on any edge.
2. The edge picks the least-loaded media server that is not draining and has recording
   capacity.
3. It replies with its own WebSocket URL plus a routing ticket naming that server.
4. The client connects. The edge proxies the WebSocket, and the JoinResponse carries the
   edge's TURN credentials with `force_relay`.
5. The media server writes `pres:` and publishes the join on `group:<group>`.

**Viewer join.**

1. `/rtc/route` picks an edge, preferring one that already serves the viewer's group (group
   affinity), so presenters are relayed to fewer edges.
2. The edge builds a **virtual room** for the group from the `pres:` keys, then follows the
   group channel.
3. The viewer gets a normal JoinResponse with the group's presenters. It uses
   `auto_subscribe=0` and subscribes on demand. The signaling protocol does not change.

**Relay (edge pulls from a media server).**

1. For each media server holding a presenter its viewers want, the edge opens **one relay
   session**: a hidden participant of kind `RELAY` on the media server, joined with an internal
   key over the private network. This reuses the existing signaling and subscriber path, with
   no new protocol.
2. Subscribing a viewer to a presenter makes the edge subscribe its relay session to that
   track, if it is not already relayed. The edge then attaches the viewer's DownTrack to the
   relayed track's receiver.
3. **Layers.** The relay subscription asks the media server for the highest layer any viewer on
   this edge needs, using `track_setting`. The media server's forwarder already switches
   layers with continuous sequence numbers and timestamps, so the edge sees one clean stream
   per presenter.
4. **Recovery.** NACKs are answered hop by hop from each server's buffer. PLIs travel viewer →
   edge → media server → presenter.
5. **No congestion control on relay sessions.** It is a private network, so a media server
   never pauses or downgrades a relay subscription because of bandwidth.
6. **Unsubscribing.** A relayed track no viewer watches is unsubscribed after a 10 s grace
   period, so switching back is instant.

**Switching among 10.**

- Track already relayed to the edge: the switch is a subscription change on the viewer's
  existing connection, waiting only for a keyframe (about 0.3-1 s).
- Track not yet relayed: add one round trip on the private network.
- Normal functional testing is enough; no performance target.

**Recording.** Unchanged, on the presenter's media server.

### Failure and drain

| Event | Behaviour |
|---|---|
| Edge dies | Its viewers rejoin through `/rtc/route` on another edge (a few seconds). Its presenters lose their TURN relay and WebSocket proxy: the SDK resumes through another edge within the resume window (15 s) with an ICE restart, so the session and its recording continue, with a short gap. |
| Media server dies | Its presenters rejoin and land on another media server (a new session, so a new recording). A surviving node closes their recordings from `journal:` in Redis. Edges drop the dead relay sessions; viewers see those presenters leave and rejoin. |
| Redis unavailable | Running sessions and existing relays continue. New joins, new relay subscriptions and placement fail until it is back (managed Redis with a replica). |
| Drain a media server | No new presenters; wait for sessions to end (bounded by the session length), then restart. After a maximum wait, `leave{RECONNECT}`. |
| Drain an edge | No new viewers or presenters. After a short wait, `leave{RECONNECT}`; clients move to another edge. Presenters keep their media server and recording. |

### Security

Public:
- edges only: 443/TCP (WebSocket, TURN over TLS), the TURN UDP/TCP port and the SFU UDP port.

Private:
- media servers and Redis.
- Edge TURN relays only to the media-server subnet. The relay sessions use an internal key
  that is never handed to clients.

## Consequences

- One connection per viewer regardless of how presenters are spread. Switching is a
  subscription change; overlapping groups, supervisors and groups larger than a server work.
- The two tiers scale separately: media servers by recorded presenters (about 300-450 per 4 vCPU
  measured), edges by viewers (to be measured).
- One extra hop for viewers (about 1 ms inside a zone) and relay traffic on the private network.
- New moving parts: the relay session and the virtual group room on edges, Redis, the
  WebSocket proxy. The relay is the riskiest piece and gets the most testing.
- Presenters always use TURN, so they cost the edge relay bandwidth.
- The single-node mode stays: with no Redis configured, a node is both edge and media server,
  as today (`single-node` branch behaviour).

## Plan

Each phase is tested before the next.

| Phase | Content | Test gate | Effort |
|---|---|---|---|
| 1 | Redis registry, `/rtc/route`, presenter placement, edge WebSocket proxy and TURN to private media servers (viewers still on the media server) | presenters record through an edge; kill/drain drills | 3-4 days |
| 2 | Relay session (edge subscribes from media servers), virtual group room on edges, viewers on edges | viewer watches presenters on 3 media servers through one edge; e2e tests | 5-7 days |
| 3 | Layer selection on the relay, group affinity, grace-period unsubscribe, switching | switch among 10 of 100 presenters | 2-3 days |
| 4 | Failure and drain handling, crash journal in Redis | drills from the table above | 2-3 days |
| 5 | GCP: 2 edges, 3 media servers, Redis; soak, network scenarios, failure drills | same bars as the single-node soak | 2 days plus GCP cost (estimate before starting) |

## Alternatives considered

- **Room per group on one server, everyone connects directly.** Simplest, but needs public
  media servers (compliance), and groups larger than a server or overlapping assignments do
  not fit.
- **Viewer connects to every server holding one of its presenters.** No relay to build, but
  up to 9 connections per viewer, split congestion control and slow switches (see Context).
- **Full mesh cascading (any node relays to any node, rooms span nodes).** More general than
  needed: presenters never watch, so relaying one way, media server to edge, is enough.
- **Separate TURN machines plus public media servers.** Satisfies the TURN rule but leaves
  media servers public, and still leaves the many-connections problem for viewers.
