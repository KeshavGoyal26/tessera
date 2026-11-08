# ADR-0012: Edge tier (SFU + TURN) in front of private media servers

- Status: proposed
- Date: 2026-11-08 (revised the same day: fixed roles, always-on media, single stream)
- Deciders: Tessera maintainers

## Context

Use case and constraints for production:

- **Fixed roles.** A **presenter** only publishes: camera and microphone, always on (no mute or
  camera control), always recorded. A **viewer** only watches and never publishes. Both belong
  to a **group** (`groupId`).
- **Watching.** A viewer may watch any presenter of its group on demand, typically **10 at a
  time out of hundreds**, switching often. Who watches whom changes during the session and
  groups overlap (supervisors). Normal functional testing is enough for switching.
- **Scale.** About 10,000 concurrent users across many groups. A group can be larger than one
  server.
- **Compliance.** Internet-facing components run on machines separate from the media servers.
  The edge may decrypt media: the rule is about exposure, not content.
- **Priorities.** Stability on par with LiveKit, then simplicity. No transcoding, no deadline.
  The target is medium and fast networks; slow-link tuning is out of scope.

Today (ADR-0002, single node) every participant connects to the one public server hosting the
room. Measured on GCP n2-standard-4: about 450 recorded presenters (simulcast) at 66% CPU, with
recording in process (ADR-0007, amended).

**Rejected: each viewer connects to every server holding one of its presenters.** A viewer of 9
servers would need:
- 9 WebSockets, 9 PeerConnections, and 9 TURN allocations behind strict firewalls;
- its bandwidth split between 9 congestion controllers;
- a new connection (1-3 s) for every switch to a presenter on a new server.

## Decision

### Single stream, no simulcast

Presenters publish **one video stream**, not simulcast. The server sends `disable_simulcast` in
the presenter's client configuration. The quality is a server setting, defaulting to **360p at
15-20 fps**, about 0.4-0.5 Mbit/s. Every viewer receives that stream, and the recording is that
stream.

What this removes:
- layer switching and its keyframe handling;
- the recorder following layers, and its ~2 s gaps;
- any quality coordination between edge and media server;
- about two thirds of a presenter's upload, and of the media server's packets and buffer memory
  per presenter.

Simulcast code stays in the codebase (the `single-node` branch uses it). It is not used in this
deployment and may come back later as a separate step if field data calls for it.

What it costs, accepted:
- a viewer whose downlink cannot carry all its tiles gets some tiles **paused**, not lowered;
- watching 10 presenters needs about 4-5 Mbit/s;
- an enlarged view is the same 360p.

### Components

```
                   public                          |              private
 presenter --WS--> edge: WebSocket proxy ----------|--> media server: receive + record
 presenter ==UDP/TCP/TLS 443==> edge: TURN relay ==|==> media server (only this subnet)
 viewer ----WS---> edge: SFU (signaling)           |
 viewer <==media== edge: SFU <======= relay =======|=== media server (edge subscribes)
                                                   |
 nodes <---------------- Redis (registry, placement, presence) ----------------> nodes
```

1. **Media servers (private).** Today's `tessera`:
   - presenter sessions, receive buffers and NACK to the presenter;
   - in-process recording and webhooks.

   Presenters never subscribe, so a media server forwards only to edges' relay sessions. It has
   no public address.
2. **Edges (public).** The same binary with `role: edge`. Three jobs:
   - **Viewer SFU.** A viewer has one WebSocket and one receiving PeerConnection, to one edge,
     whatever servers its presenters are on. The edge forwards, does per-viewer congestion
     control (pausing tiles that do not fit) and answers NACK/PLI.
   - **TURN for presenters.** Embedded TURN (ADR-0005) whose relay permission is the
     media-server subnet only. Presenters are forced to relay (`force_relay`) through it.
   - **Signaling proxy for presenters.** The presenter's WebSocket terminates on the edge, which
     passes the bytes through to the media server's private URL.
3. **Redis.** Never on the media path. If Redis fails, new joins and new relay subscriptions
   stop; sessions in progress continue.
4. **HTTPS load balancer** in front of the edges, for `/rtc/route` and the admin API.

### Group state is a presence list

Presenters have exactly two tracks, camera and microphone, which are known at join and never
muted. Group state is therefore only "presenter X is on media server M with these two tracks"
or "presenter X left".

An edge works out locally from the relayed media:
- **Speaking:** from the RTP audio-level extension.
- **A stalled presenter:** the relayed video stops.

Presenter connection quality, if viewers should see it, is written to Redis every few seconds.

### Redis state

| Key | Value | Lifetime |
|---|---|---|
| `node:<id>` | role, public URL, private URL, CPU, sessions, recordings, draining | refreshed every 2 s, expires after 10 s |
| `pres:<group>:<identity>` | media node, participant sid, camera and mic track sids | refreshed by the media node every 10 s; deleted on leave |
| channel `group:<group>` | presenter joined / left (sid and version) | pub/sub |
| `rec:<id>` | RecordingInfo, so any node answers Get/List | about 7 days |
| `journal:<id>` | crash journal (moved from local disk), so a surviving node closes recordings of a dead one | until the recording ends |

### Flows

**Presenter join.**

1. The client calls `GET /rtc/route?access_token=...` on any edge.
2. The edge picks the least-loaded media server that is not draining and has recording
   capacity.
3. It returns its own WebSocket URL with a routing ticket naming that server.
4. The client connects. The edge proxies the WebSocket, and the JoinResponse carries the
   edge's TURN credentials, `force_relay` and `disable_simulcast`.
5. The presenter publishes camera and mic. The media server starts the recording, writes
   `pres:` and announces the join on `group:<group>`.

**Viewer join.**

1. `/rtc/route` picks an edge, preferring one that already serves the viewer's group (group
   affinity) so a presenter is relayed to as few edges as possible.
2. The edge lists the group's presenters from `pres:` and follows `group:<group>`.
3. The viewer gets a normal JoinResponse listing those presenters. It uses `auto_subscribe=0`
   and subscribes on demand. The signaling protocol does not change.

**Relay (edge pulls from a media server).**

1. For each media server holding a presenter its viewers watch, the edge keeps **one relay
   session**: a hidden participant of kind `RELAY`, joined with an internal key over the private
   network. It reuses the existing signaling and subscriber path.
2. A viewer subscribing to a presenter makes the edge subscribe its relay session to that
   presenter's two tracks, unless they are already relayed. The edge then attaches the viewer's
   DownTracks to the relayed tracks' receivers.
3. The receiver is source-agnostic, so a relayed track is a single-layer receiver on the edge.
4. **Recovery:**
   - NACKs are answered hop by hop from each server's buffer.
   - A PLI goes viewer → edge → media server → presenter.
5. **No congestion control on relay sessions.** On the private network a media server never
   pauses a relay subscription.
6. A relayed presenter no viewer on the edge watches is unsubscribed after a 10 s grace period,
   so switching back is instant.

**Switching among 10.**

- Presenter already relayed to the edge: a subscription change on the viewer's existing
  connection, waiting only for a keyframe (about 0.3-1 s).
- Not relayed yet: add one round trip on the private network.

**Recording.** Unchanged: one stream per presenter, recorded on its media server.

### Failure and drain

| Event | Behaviour |
|---|---|
| Edge dies | Its viewers rejoin through `/rtc/route` on another edge (a few seconds). Its presenters lose their TURN relay and WebSocket proxy: the SDK resumes through another edge within the resume window (15 s) with an ICE restart, keeping the session and the recording (short gap). |
| Media server dies | Its presenters rejoin on another media server (a new session and recording). A surviving node closes their recordings from `journal:`. Edges drop the dead relay session; viewers see those presenters leave and come back. |
| Redis unavailable | Running sessions and existing relays continue. New joins, new relay subscriptions and placement fail until it is back (managed Redis with a replica). |
| Drain a media server | No new presenters; wait for sessions to end (bounded by session length), then restart. After a maximum wait, `leave{RECONNECT}`. |
| Drain an edge | No new sessions. After a short wait, `leave{RECONNECT}`; clients move to another edge. Presenters keep their media server and recording. |

### Permissions (enforced by the server, not only the UI)

| Token | Can publish | Can subscribe | Other rules |
|---|---|---|---|
| Presenter | camera and microphone | no | mute and unpublish requests refused |
| Viewer | no | yes | may subscribe only to presenters of its own group |

### Security

- **Public:** edges only, with 443/TCP (WebSocket and TURN over TLS), the TURN UDP/TCP port and
  the SFU UDP port.
- **Private:** media servers and Redis.
- **TURN:** relays only to the media-server subnet.
- **Relay sessions:** use an internal key that is never handed to clients.

## Consequences

- Every viewer has one connection, however presenters are spread. Switching is a subscription
  change. Overlapping groups, supervisors and groups larger than a server all work.
- No simulcast, mute or room-state synchronisation across servers. The cross-server work is
  routing, a presence list and a one-way, single-stream relay.
- The tiers scale separately:
  - media servers by recorded presenters (measured about 450 per 4 vCPU with simulcast;
    single stream should allow more, to be measured);
  - edges by viewers (to be measured).
- One extra hop for viewers (about 1 ms inside a zone) and relay traffic on the private network.
- Presenters always use TURN, so every presenter costs edge relay bandwidth (about 0.5 Mbit/s).
- **New moving parts:** the relay session, the edge's group presence, Redis and the WebSocket
  proxy. The relay is the riskiest piece and gets the most testing.
- **Single-node mode stays:** with no Redis configured, a node is both edge and media server, as
  today.

## Plan

Each phase is tested before the next.

| Phase | Content | Test gate | Effort |
|---|---|---|---|
| 1 | Redis registry, `/rtc/route`, presenter placement, edge WebSocket proxy and TURN to private media servers, presenter permissions and single stream (viewers still on the media server) | presenters record through an edge; kill and drain drills | 3 days |
| 2 | Relay session (edge subscribes from media servers), group presence on edges, viewers on edges with on-demand subscription | a viewer watches presenters on 3 media servers through one edge; switching among 10 of 100; e2e tests | 4-5 days |
| 3 | Failure and drain handling, crash journal in Redis, group affinity | the drills in the table above | 2 days |
| 4 | GCP: 2 edges, 3 media servers, Redis; soak, network scenarios, failure drills | same bars as the single-node soak | 2 days plus GCP cost (estimated before starting) |

About **1.5-2 weeks** in total.

## Open questions

- Is 360p at 15-20 fps enough for recordings and for an enlarged view? The setting can change
  without code.

## Alternatives considered

- **Room per group on one public server.** Simplest, but media servers would be public
  (compliance), and groups larger than a server or overlapping assignments do not fit.
- **Viewer connects to every server holding one of its presenters.** Up to 9 connections per
  viewer, split congestion control and slow switches (see Context).
- **Keep simulcast and relay one chosen layer per edge.** Smaller tiles for weak viewers and HD
  enlarged views, but it brings back layer switching, recorder layer-following and quality
  coordination across two hops: the most bug-prone paths found so far.
- **Full mesh cascading (rooms span nodes, relays in both directions).** More general than
  needed: presenters never watch, so a one-way relay from media servers to edges is enough.
- **Separate TURN machines with public media servers.** Satisfies the TURN rule but leaves media
  servers public, and still leaves the many-connections problem for viewers.
