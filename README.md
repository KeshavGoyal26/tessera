# Tessera

Tessera is a WebRTC SFU (Selective Forwarding Unit) media server written from scratch in Go on
[Pion](https://github.com/pion/webrtc). It records to HLS inside the server, scales out as a
cluster of public edges in front of private media nodes, and comes with a TypeScript browser SDK,
a Java server SDK, a web demo and an admin dashboard.

Status: the SFU core, HLS recording, cluster mode, admin API, SDKs and dashboard are implemented
and tested (unit, end-to-end, load and browser tests). See [docs/PROGRESS.md](docs/PROGRESS.md)
for detail and [docs/TODO.md](docs/TODO.md) for known open items.

## Contents

- [Features](#features)
- [Quick start](#quick-start)
- [Single server and cluster](#single-server-and-cluster)
- [Ports and URLs](#ports-and-urls)
- [Configuration](#configuration)
- [Docker](#docker)
- [TURN and restrictive networks](#turn-and-restrictive-networks)
- [Recording](#recording)
- [Dashboard and metrics](#dashboard-and-metrics)
- [Load testing](#load-testing)
- [SDKs](#sdks)
- [Repository map](#repository-map)
- [Development](#development)
- [Documentation](#documentation)

## Features

- **SFU for many feeds per room**: many publishers and subscribers per room, two
  PeerConnections per participant (one each way), no glare.
- **Browsers**: Chrome, Edge, Firefox and Safari (latest two versions).
- **Codecs**: Opus (DTX, in-band FEC, optional RED) and H.264. One stream per track: no
  simulcast and no VP8 since the cluster design (ADR-0012; the `single-node` branch keeps both).
- **Forwarding**: RTP sequence and timestamp rewriting across pauses, NACK from per-track ring
  buffers, PLI/FIR throttling, RTCP SR/RR, TWCC send-side bandwidth estimation that pauses video
  a subscriber cannot take (with probing to recover), active speaker detection.
- **Networking**: one UDP port for all media, ICE-TCP fallback, NAT 1:1 (`node_ip`), embedded
  TURN over UDP, TCP and TLS, forced relay for restrictive networks.
- **Cluster mode**: public edges serve viewers and relay presenters to private media nodes;
  Redis holds the node registry and presenter placement (ADR-0012).
- **Recording**: built into the server. Tracks are read from the receive buffer (no second
  WebRTC session) and written as fMP4/CMAF HLS. H.264 and Opus are remuxed, video is never
  transcoded; an optional AAC audio mode uses FFmpeg. Output to local disk or S3-compatible
  storage, with live playback (rewind) while recording and crash recovery.
- **Integration**: HS256 JWT access tokens, a Twirp-style admin API (JSON or protobuf),
  HMAC-signed webhooks, a Java 17 SDK (only runtime dependency: protobuf-java) and a small
  framework-free ESM browser SDK.
- **Operations**: an admin dashboard, Prometheus metrics, pprof, health and readiness endpoints,
  graceful drain, structured logs (`log/slog`), Docker images and compose files.
- **Performance targets**: at least 100k forwarded packets/s per core, zero allocations per
  forwarded packet, under 2 ms added p99 latency. Measured on GCP n2-standard-4: about 500-600
  recorded presenters per media node and about 300 viewers × 10 presenters per edge
  ([docs/perf](docs/perf)).

## Quick start

All options start the server(s) and the web demo in the background and print the URLs to open.
Development credentials: API key `devkey`, secret `devsecret-devsecret-devsecret-0000`.

**Native** (needs Go 1.27, which is downloaded automatically if needed, and Node 22; the cluster
also needs `redis-server`, e.g. `brew install redis`):

```bash
scripts/start.sh            # single server          stop: scripts/stop.sh
scripts/cluster-start.sh    # cluster: 2 media, 2 edges, Redis   stop: scripts/cluster-stop.sh
```

**Docker** (needs only Docker Desktop):

```bash
scripts/docker.sh start            # cluster          stop: scripts/docker.sh stop
scripts/docker.sh start single     # single server    stop: scripts/docker.sh stop single
```

Then open the web demo, for example `http://localhost:5173/?url=ws://<your LAN IP>:7930` for the
cluster (`:7880` for a single server). In one tab go live as a presenter, in another watch as a
viewer. `call.html` is a video call between equals.

**By hand** (single server, development mode):

```bash
go run ./cmd/tessera --dev                              # HTTP :7880, media :7882/udp, metrics :6789
go run ./cmd/tessera create-token --room demo --identity alice
cd sdk/js && npm ci && npm run build && cd ../../examples/web-demo && npm ci && npm run dev
```

`--dev` turns on development mode: the dev API key when none is configured, readable logs,
`/dev/token` (tokens for the web demo; anyone who can reach the port can join any room) and
recordings served under `/recordings/`. Never use it in production.

## Single server and cluster

The same binary runs in both modes; the config decides.

| | Single server | Cluster (ADR-0012) |
|---|---|---|
| Processes | one `tessera` | Redis, media nodes (`cluster.role: media`), edges (`cluster.role: edge`) |
| Clients connect to | the server | an edge; presenters' media goes to a media node |
| Presenters | on the server | on a media node, placed at random (a reconnect returns to the same node) |
| Viewers | on the server | on any edge, which pulls each presenter once from its media node |
| Recording | in the server | on the presenter's media node |
| Scale | one machine | add media nodes for presenters, edges for viewers |
| Config | no `cluster:` section | `cluster: { role, private_url, redis: { addr } }` |

Media nodes are meant to stay on a private network: presenters reach them through the edges'
WebSocket proxy and, with forced relay, through the edges' TURN. A room's presenters can span
several media nodes; a viewer on any edge still sees all of them over one connection.

## Ports and URLs

Single server (defaults):

| Port | What |
|---|---|
| 7880/tcp | signalling (WebSocket `/rtc`), admin API, dashboard `/admin/`, `/dev/token` and `/recordings/` in `--dev` |
| 7882/udp, 7881/tcp | media (ICE over UDP, ICE-TCP fallback) |
| 3478 (udp, tcp), 5349/tcp | embedded TURN when `turn.enabled` |
| 6789/tcp | metrics (`/metrics`, `/stats`, `/recorder/stats`, health, pprof) |
| 5173/tcp | web demo (start scripts and Docker) |

Local cluster (`scripts/cluster-start.sh` and `scripts/docker.sh start`):

| Node | Signalling and dashboard | Media (tcp, udp) | Metrics and recordings |
|---|---|---|---|
| media m1 | 7910 | 7911, 7912 | 6791 |
| media m2 | 7920 | 7921, 7922 | 6792 |
| edge e1 | 7930 | 7931, 7932 | (6793 native) |
| edge e2 | 7940 | 7941, 7942 | (6794 native) |
| Redis | 6390 native, internal in Docker | | |
| web demo | 5173 | | |

Clients connect to the edges: `ws://<host>:7930` or `:7940`. Dashboards are at
`http://<node>/admin/` on every node; the Cluster page shows all nodes. Run one setup at a time:
the native and Docker setups use the same ports.

## Configuration

A YAML file (`--config tessera.yaml`) with `${ENV}` expansion; unknown keys are errors. All
options and defaults are in [pkg/config/config.go](pkg/config/config.go). Examples:
[deploy/config/tessera.yaml](deploy/config/tessera.yaml) (single server) and
[deploy/config/cluster-node.yaml](deploy/config/cluster-node.yaml) (any cluster node).

Environment variables that override the file:

| Variable | Effect |
|---|---|
| `TESSERA_KEYS` | API keys: `"key1: secret1, key2: secret2"` |
| `TESSERA_NODE_IP` | address advertised to browsers for media (`rtc.node_ip`, NAT 1:1) |
| `TESSERA_TURN_URLS` | TURN servers for clients, comma-separated |
| `TESSERA_TURN_SECRET` | shared secret of those TURN servers: per-session credentials (`rtc.turn_relay`) |
| `TESSERA_TURN_USERNAME`, `TESSERA_TURN_CREDENTIAL` | fixed credentials instead (`rtc.ice_servers`) |
| `TESSERA_FORCE_RELAY` | `true`: clients use only TURN |

Production: real secrets of at least 32 bytes, TLS in front of the signalling port (`wss://`),
`node_ip` set to the public IP, S3 storage for recordings. See
[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md).

## Docker

`scripts/docker.sh {start|build|stop|status|logs} [cluster|single]` wraps two compose files:

- [deploy/docker-compose.yml](deploy/docker-compose.yml): one server and the web demo.
- [deploy/docker-compose.cluster.yml](deploy/docker-compose.cluster.yml): Redis, media nodes
  m1 m2, edges e1 e2 and the web demo, same ports as the native cluster.

On first run the script creates `deploy/.env` from [deploy/.env.example](deploy/.env.example)
with this machine's LAN IP, and follows the IP when it changes. It waits until the servers answer
and prints the URLs. `start` rebuilds what changed; to start without building:
`docker compose -f deploy/docker-compose.cluster.yml --env-file deploy/.env up -d`.

Settings, on the command line or in `deploy/.env`:

| Setting | Effect |
|---|---|
| `TESSERA_NODE_IP` | media address for browsers (set automatically) |
| `EDGE_TURN=1` | cluster: presenters only through the edges' TURN over TCP |
| `TURN_URLS`, `TURN_SECRET` or `TURN_USERNAME`/`TURN_CREDENTIAL`, `FORCE_RELAY` | an external TURN server for clients |
| `NPMRC=~/.npmrc` | npm registry and credentials for the web demo build |
| `NPM_REGISTRY`, `GOPROXY` | download mirrors for the build |

Behind a proxy that re-signs HTTPS (company network, VPN, antivirus), put its root certificate in
`deploy/docker/local/*.crt`; see [deploy/docker/local/README.md](deploy/docker/local/README.md).
The build context is an allowlist ([.dockerignore](.dockerignore)): a build sends about 0.5 MB.
The server image is about 32 MB (distroless, non-root). In Docker, recordings live in the
containers and are lost when they are removed.

## TURN and restrictive networks

- **Embedded TURN** (`turn:` section): UDP, TCP and TLS (`turns:` on 443 with a certificate),
  per-session credentials from a shared secret. `turn.relay_ip` is the address the relay uses
  towards the media servers (an edge's private address); clients reach TURN at `rtc.node_ip`.
- **Forced relay** (`rtc.turn_relay.force` or `TESSERA_FORCE_RELAY=true`): browsers use only
  TURN. Handing out only `?transport=tcp` or `turns:` URLs makes that TURN over TCP or TLS.
- **Cluster**: edges run TURN, media nodes hand presenters the edges' TURN and force relay, so
  presenters never reach a media node directly. In Docker: `EDGE_TURN=1 scripts/docker.sh start`.

Check in `chrome://webrtc-internals`: the selected candidate pair should be `relay` with
protocol `tcp`; the dashboard's Connectivity page lists the ICE type per participant.

## Recording

Recordings run inside the server (in cluster mode on the presenter's media node). Start them
through the admin API or dashboard, per track or per participant; or automatically: a token grant
with `auto_record`, or a room created with `recording_enabled`, records every participant from
the moment they publish. Output per recording:

```
<room>/<recording_id>/master.m3u8          live while recording, VOD at the end
<room>/<recording_id>/<track>/index.m3u8   fMP4 segments, init.mp4
<room>/<recording_id>/quality.json         connection-quality timeline
```

`recording.filename_prefix` changes the layout. For example,
`recordings/{shard}/{pid}/{start_ms}` puts every recording of a presenter under
`recordings/<pid mod 1000>/<pid>/` with an `index.json` listing them all.

Viewers can rewind a presenter while the recording is live (the web demo's rewind button; the
URL is in the participant's `hls_url` attribute). `recording_started` and `recording_ended`
webhooks carry the playlist URLs. Details: [docs/RECORDING.md](docs/RECORDING.md).

## Dashboard and metrics

- **Dashboard** at `/admin/` on every node (log in with an API key and secret): overview,
  rooms and participants (kick, mute, record), track quality, connectivity, recordings with
  playback, alerts, and a cluster view across all nodes. It keeps the last hour of history in
  memory. See [docs/DASHBOARD.md](docs/DASHBOARD.md).
- **Prometheus** at `/metrics` on the metrics port: node health, traffic, rooms, participants,
  tracks, connection quality, NACK/PLI, recording, webhooks and admin API (all names
  `tessera_*`, no per-room labels). [deploy/grafana](deploy/grafana) has a scrape config and a
  Grafana dashboard. See [docs/STATS.md](docs/STATS.md).
- **JSON**: `/stats` (node, rooms, tracks) and `/recorder/stats` on the metrics port.

## Load testing

```bash
go run ./cmd/tessera-loadtest --url ws://localhost:7880 --secret devsecret-devsecret-devsecret-0000 \
  --rooms 2 --publishers 4 --subscribers 4 --duration 30s
```

`--url` takes several servers (`ws://a:7930,ws://b:7940` for both edges); `--json <file>` writes
the report (connect failures, disconnects, loss, latency, join time); the exit code is non-zero
on failures. Results and the cluster test plan: [docs/perf](docs/perf).

## SDKs

**Browser** ([sdk/js](sdk/js), `@tessera/client`):

```ts
const room = await Room.connect("wss://media.example.com", token);
room.on(RoomEvent.TrackSubscribed, (track) => document.body.append(track.attach()));
await room.localParticipant.setCameraEnabled(true);
await room.localParticipant.setMicrophoneEnabled(true);
```

Reconnects on its own (resume within the resume window, full rejoin after), honours forced
relay, and serializes device toggles. The [web demo](examples/web-demo) shows a live
presenter/viewer app and a video call.

**Java** ([sdk/java](sdk/java)): tokens, admin API and webhook verification.

```java
String token = AccessToken.builder()
    .apiKey("devkey").secret(secret)
    .identity("alice")
    .grants(RoomGrant.builder().room("demo").roomJoin(true).build())
    .build()
    .toJwt();
```

See [sdk/java/README.md](sdk/java/README.md) and the [Spring example](examples/java-spring).
**Go**: [pkg/client](pkg/client) is the Go client the load tester and cluster edges use.

## Repository map

| Path | Contents |
|---|---|
| `cmd/tessera` | server binary (`--dev`, `create-token`) |
| `cmd/tessera-loadtest` | load generator |
| `pkg/rtc/...` | SFU engine: transports, receivers, downtracks, buffer, forwarding, BWE, speakers, TURN |
| `pkg/room`, `pkg/signal`, `pkg/auth` | rooms and participants, WebSocket signalling, tokens |
| `pkg/cluster` | cluster mode: Redis registry, placement, edge proxy and relays, admin fan-out |
| `pkg/admin`, `pkg/webhook` | Twirp admin API, webhooks |
| `pkg/hls`, `pkg/recorder`, `pkg/storage` | fMP4/HLS muxer, recording coordinator and worker, local and S3 storage |
| `pkg/dashboard`, `web/admin` | dashboard backend and its single-page app (built into the binary) |
| `pkg/config`, `pkg/client`, `pkg/stats`, `pkg/metrics` | config, Go client, statistics, metrics and health |
| `proto/tessera/v1`, `gen/go` | protobuf schemas and generated Go code |
| `sdk/js`, `sdk/java` | browser SDK, Java SDK |
| `examples/web-demo`, `examples/java-spring` | demo apps |
| `deploy/` | Dockerfile, compose files, example configs, Grafana |
| `scripts/` | start/stop scripts (native, cluster, Docker), local CI |
| `test/e2e` | end-to-end tests: real servers, Pion clients, cluster, recording |
| `docs/` | architecture, ADRs, protocol, deployment, performance, status |

## Development

```bash
make test-race      # Go tests with the race detector (also: make build lint bench proto)
make js             # browser SDK: lint, typecheck, test, build
make java           # Java SDK
scripts/ci-local.sh # every CI job locally
```

The GitHub workflows (`.github/workflows`) run only when started by hand (`gh workflow run ci`),
to save Actions minutes. After changing `proto/`, run `make proto` (buf, protoc-gen-go,
protoc-gen-es). After changing `web/admin`, run `npm run build` there: the dashboard is embedded
from `pkg/dashboard/dist`.

## Documentation

- [Architecture](docs/ARCHITECTURE.md): components, hot path, memory, scaling, targets
- [Protocol](docs/PROTOCOL.md): signalling, admin API, webhooks
- [Architecture decision records](docs/adr): ADR-0012 covers the cluster (edges and media nodes)
- [Deployment](docs/DEPLOYMENT.md), [Recording](docs/RECORDING.md),
  [Dashboard](docs/DASHBOARD.md), [Statistics](docs/STATS.md), [Demo](docs/DEMO.md)
- [Performance](docs/perf), [Progress](docs/PROGRESS.md), [Open items](docs/TODO.md)

Tessera takes design inspiration from Kurento, Janus, OpenVidu, LiveKit, Pion, mediasoup and
ion-sfu. It contains no code from them.

## License

Apache-2.0 (to be confirmed by the owner; a LICENSE file will be added).
