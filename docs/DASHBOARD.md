# Admin dashboard

Served by the `tessera` server at `http://<host>:7880/admin/` (single binary: the TypeScript
app in `web/admin` is built into `pkg/dashboard/dist` and embedded with `go:embed`).

## Access

Sign in with an API key and secret from `keys:` in `tessera.yaml` (the same pair used for the
admin API). Login sets an `HttpOnly`, `SameSite=Strict` cookie (`tessera_admin`, path `/admin`,
`Secure` over HTTPS or `X-Forwarded-Proto: https`), valid for 12 h. State-changing requests
need the `X-CSRF-Token` header returned by `login`/`session` and a same-origin `Origin`.
Five failed logins from one IP lock further attempts for a minute. Put the dashboard behind
TLS (reverse proxy) in production; with plain HTTP the cookie is not `Secure`.

## Views

| View | Content |
|------|---------|
| Overview | rooms, participants (presenters / viewers), tracks, recording (active, capacity units used), CPU, memory, goroutines, GC, UDP packets and bandwidth, one hour of charts (legends show the latest values), alerts |
| Rooms | rooms (presenters, viewers, recording, speakers) → participants (role, quality, what they publish or watch, paused tiles) → a presenter's tracks or what a viewer watches; end room, remove participant, mute/unmute track, start recording (presenters and their tracks) |
| Track quality | per published track: bitrate, loss, jitter, resolution, NACK/PLI/FIR, receiving / no media; per subscriber: bandwidth estimate, restarts, paused/muted state. Time series for the selected track are collected in the browser (last 15 min) |
| Connectivity | ICE candidate types, TURN (relay) usage, ICE/DTLS setup avg/p95/max, connection quality, RTT history, the 100 worst participants |
| Recordings | recordings in progress (▶ Watch live from the live master playlist, Stop) and ended (▶ Play); the player draws the recording's connection timeline under the video (green / yellow / red per 4 s, click to seek) with a dot for the playhead and a summary ("Connection: 90% good · 6% unstable · 4% no media (1 drop)", see `quality.json` in PROTOCOL.md); recorder workers (capacity, backlog, segment delay, errors), active tracks (problems first), recordings finished on this worker with per-track playback through hls.js (needs a playlist URL: S3/CDN, or local storage served with `local_base_url`, the `--dev` default) |

Roles follow ADR-0012 and are derived from what a participant does: a **presenter** publishes, a
**viewer** only watches; edge relay sessions (on a media node) and recorders are counted apart.
Rows keep a stable order (rooms by name, presenters first, then by identity) and list views have a
filter; tables render at most 200 rows (the 100 worst on Connectivity) and say how many were left
out. The once-a-second refresh keeps keyboard focus and pauses while text is selected; a hidden
browser tab skips rendering.

Light and dark theme (follows the OS, toggle in the header), responsive down to phone width.

## Cluster view

On a cluster node (ADR-0012) the dashboard opens on **Cluster**, and any node's dashboard can show
the whole cluster (use an edge's: it is reachable, and its actions go through the admin forwarder).

| | Content |
|-|---------|
| Cluster | nodes up/down by role, presenters (media nodes), viewers (all nodes), egress, busiest CPU, recording capacity; cluster alerts; the node table (status, CPU, memory, connections, recording, bandwidth, version, uptime, heartbeat age); one hour of cluster charts; the room index across nodes (presenters, viewers, media nodes, edges) |
| Cluster › room | one room on every node: presenters on their media node and viewers on each edge (relayed copies and relay sessions left out), with Record / Remove |
| Node picker | the header's **Node** menu points Overview, Rooms, Track quality, Connectivity and Recordings at another node, shown through this dashboard (a banner says so; **Back** returns) |

How it stays cheap:

- Each node adds its **summary** (about 500 bytes: CPU, memory, connections, recording capacity,
  bandwidth, alert counts) to the registry heartbeat it already sends every 2 s. It is derived from
  the sample the dashboard takes anyway.
- The serving node reads the registry every 5 s (always on edges, otherwise for an hour after a
  visit), keeps one hour of cluster history and derives the cluster alerts from it.
- Room index, one room and one node are fetched from the nodes' dashboards **only while that page
  is open** (the browser polls every 2–5 s while visible) and cached for 1–4 s on the serving node,
  so several open dashboards cost the same as one. A node answers from its latest sample (the
  full update is marshalled at most once per second whoever asks).
- Measured on a local cluster (2 media nodes, 1 edge, 20 clients): 23 Redis commands/s with or
  without the cluster page open; every cluster endpoint answers in under 1 ms; cluster state
  1.5 KB, room index 0.4 KB, one room 12.5 KB.

Node table connections, by kind:

| Column | Media node | Edge |
|---|---|---|
| Media in | presenters sending media here, and how many through TURN | presenters pulled from media nodes, and the pull sessions bringing them |
| Media out | edges' pull sessions served | viewers, and how many through TURN |
| Proxied signaling | | presenter WebSockets passed through to their media node |
| TURN | | allocations on this edge's TURN server and their traffic (both legs: clients and media nodes) |

"Through TURN" is the participant's selected ICE path (`relay`). A browser told about several
TURN servers may allocate on each, so allocations can exceed the connections using them. The
**Out** column is media sent by the SFU; TURN traffic is shown in its own column.

Cluster alerts: a node that stops heartbeating (listed as down for an hour), a late heartbeat
(over 4 s), a draining node, the registry unreachable, no media node up, and each node's own
alerts rolled up into one line per node.

Nodes fetch from each other with a token signed by the cluster API key (`room_admin` grant) over
the private network, at `GET /admin/api/node`; a node needs `cluster.private_url` to be reachable
(media nodes have it; set it on edges too to see their viewers from another node's dashboard).

## Alerts

Evaluated every second on the server:

- downlink loss above 5 % (one alert per participant, naming the worst track's presenter) and a
  presenter's uplink loss above 5 %;
- participant RTT above 300 ms; CPU above 80 % of all cores;
- a presenter connected to this node whose video receives no packets for a window (after 10 s in
  the room; admin-muted tracks and presenters relayed from another node are skipped);
- recorder unreachable, recorder upload/track errors, recorder capacity at 90 % (critical when
  full: a recording beyond capacity waits and misses its start), failed recordings (last 15 min).

At most 100 alerts are sent (critical first); the rest are counted in one summary alert.
Thresholds are `dashboard.Params` fields (`LossPercent`, `RTTMs`, `CPUPercent`).

## Endpoints (all under `/admin/api/`, JSON, cookie auth)

| Endpoint | |
|----------|--|
| `POST login` `{api_key, secret}` | sets cookie, returns `{csrf}` |
| `GET session`, `POST logout` | |
| `GET snapshot` | latest `{snapshot, recorders, alerts, point}` |
| `GET events` | Server-Sent Events, event `update`, same payload every second |
| `GET history` | last hour of `Point`s (ring buffer, 3600 × 1 s, in memory, lost on restart) |
| `GET recordings` | coordinator's recordings: all in progress and the newest 200 ended, with `total` |
| `POST actions/{end-room,remove-participant,mute-track,start-recording,stop-recording}` | CSRF required |
| `GET cluster` | cluster sample: nodes with summaries, cluster alerts, latest cluster point (cluster nodes only) |
| `GET cluster/history` | last hour of cluster points (720 × 5 s) |
| `GET cluster/rooms`, `GET cluster/room?name=` | room index across nodes; one room on every node |
| `GET cluster/node?id=[&history]` | another node's update (or its last hour), through this dashboard |
| `GET node[?room=\|?rooms\|?history]` | for other nodes' dashboards only: bearer token signed by a cluster API key with `room_admin` |

The sampler calls `stats.Provider.Snapshot()` once per second regardless of the number of
viewers (see `docs/STATS.md`), so dashboard load does not grow with viewers.

## Configuration

The recorder section shows the server's built-in recorder (its `/recorder/stats` on the metrics
listener). `TESSERA_DASHBOARD_RECORDERS` (comma-separated base URLs, `/stats` appended) can add
other nodes' recorders.

## Development

```bash
cd web/admin && npm ci && npm run dev    # vite dev server proxies /admin/api to localhost:7880 (tessera --dev)
npm run build                            # writes ../../pkg/dashboard/dist (commit the result)
```

`pkg/dashboard/dist` is committed so `go build` needs no Node toolchain. Rebuild it after
changing `web/admin/src`.

## Grafana

`deploy/grafana/tessera-dashboard.json` (40 panels: node, participants/tracks, quality and
adaptation, connectivity, recorder) uses the metrics of `docs/STATS.md` and
`docs/RECORDING.md`. Import it in Grafana (datasource variable `DS_PROMETHEUS`) or mount
`deploy/grafana/provisioning` and the JSON at `/var/lib/grafana/dashboards`.
`deploy/grafana/prometheus.yml` is an example scrape config.

## Known gaps

- History is in memory only (one hour per node, one hour of cluster samples on the serving node).
- With local recording storage, playlist URLs point at the media node that recorded them, which
  an edge's dashboard (or a browser) may not reach; use S3/CDN storage in a cluster.
- Roles are inferred (a presenter who has not published yet is listed as a viewer).
- The frontend has unit tests and was checked with a headless browser against `tessera-loadtest`
  rooms, but there is no automated end-to-end suite yet.

## Scale (10k-100k users)

Nothing the dashboard sends grows with the number of participants:

- The live stream (`api/events`) sends a ~1 KB frame per sample: history point, counts by role,
  quality and ICE counts, at most 50 alerts (`alerts_total` counts all) and recorder totals.
- Lists are filtered, sorted and paged on the server: `api/rooms`, `api/participants`
  (`room`, `role`, `connected=1`, plus quality/ICE/setup aggregates over the filtered set) and
  `api/tracks`, with `q`, `sort`, `desc=1`, `offset`, `limit` (100 by default, 500 at most).
  Details: `api/participant?room=&sid=` (subscriptions capped at 500) and `api/track?sid=`
  (worst 100 subscribers). Each answer is built once per sample and cached.
- Every list and detail endpoint takes `node=<id>` in a cluster: the query runs on that node.
- Alerts of one kind (RTT, loss, stall) from more than 5 participants become one alert with the
  count and examples.
- With no dashboard open and no API use for 30 s, the node samples every 5 s instead of 1 s.
- History: the last hour at 1 s (`api/history`) and the last day at 10 s, each point the
  maximum of its window (`api/history?range=24h`, about 1.3 MB per node).
- JSON responses over 2 KB are gzipped when the browser accepts it.

## Sessions, troubleshooting and cluster search

- **Sessions** (`#/sessions`, `api/sessions`, `api/session-detail?sid=`): one entry per
  participant connection on the node, active ones and the last 5,000 ended (in memory): a
  timeline (joined, quality and ICE changes, tracks published and unpublished, left), the share
  of time per connection quality, worst RTT and loss. Searchable by identity, name, room and sid,
  filtered by state, role and "had poor or lost quality", sorted and paged on the server. The
  journal is derived from the samples (no change on the media path); `Handler.Journal().Record`
  lets other sources add events (any kind is shown).
- **Why was this call bad**: an active session's page shows its three hops side by side: its
  own uplink, the uplinks of the presenters it watches, and its downlink (worst 50 tracks).
- **Cluster** (`#/cluster`): participant search across every node and the worst 50
  (`api/cluster/participants`: each node returns its first offset+limit rows in the requested
  order, the server merges them; at most 16 nodes asked at once), a balance panel (presenters
  per media node, viewers per edge, CPU) and the room index searched and paged on the server.
- **Recordings** (`api/recordings`): searched (id, room, identity), filtered by status and paged.
- **Webhooks**: delivery health (sent, failed, retried, dropped, queued, last error) on Overview.
- **Export**: every list exports its current page as CSV or JSON.

## Cluster at scale (phase 3)

- **Session store**: when a session ends, its summary and timeline are written once to Redis
  (`tessera:sess:<sid>`, 24 h) and indexed per identity (`tessera:user:<identity>`, last 20).
  Cluster page › "Past sessions of a user" (`api/cluster/user?identity=`) answers "what happened
  to user X" even after the node that saw it is gone. Writes are asynchronous and dropped when
  Redis falls behind (the node's own journal keeps them).
- **Capacity** (Cluster page, `capacity` in `api/cluster`): per role the average and busiest
  node load (the higher of CPU and recording capacity) and whether the others could take the
  busiest node's load and stay under 80 % (N+1).
- **Room list at 100k**: each node returns only the slice of rooms the page needs (searched and
  sorted there) and the rooms are merged by name; cost O(nodes × page), cached a few seconds.
  Exact for name order; for presenter/viewer order a room's counts from a node that did not
  return it in its slice are missing.

## Also

- **Shared snapshots**: the dashboard, Prometheus and `/stats` read one shared snapshot per
  ~1 s (`stats.Shared`) instead of each building their own; `Snapshot()` itself stays fresh.
- **Leave reasons**: the room manager reports why each participant left (`room.Params.OnLeave`),
  shown as a `leave_reason` event in the session timeline.
- **Alert history**: alerts raised and cleared over the last 24 h (at most 1,000), paged and
  searchable on Overview (`api/alerts`), and drawn as dashed markers on the Overview charts.
