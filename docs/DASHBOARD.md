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
| Recordings | recordings in progress (▶ Watch live from the live master playlist, Stop) and ended (▶ Play), recorder workers (capacity, backlog, segment delay, errors), active tracks (problems first), recordings finished on this worker with per-track playback through hls.js (needs a playlist URL: S3/CDN, or local storage served with `local_base_url`, the `--dev` default) |

Roles follow ADR-0012 and are derived from what a participant does: a **presenter** publishes, a
**viewer** only watches; edge relay sessions (on a media node) and recorders are counted apart.
Rows keep a stable order (rooms by name, presenters first, then by identity) and list views have a
filter; tables render at most 200 rows (the 100 worst on Connectivity) and say how many were left
out. The once-a-second refresh keeps keyboard focus and pauses while text is selected; a hidden
browser tab skips rendering.

Light and dark theme (follows the OS, toggle in the header), responsive down to phone width.

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

- History is in memory only; multi-node aggregation is not implemented (one node per dashboard:
  an edge shows its viewers and relayed presenters, a media node its presenters and relay sessions).
- Roles are inferred (a presenter who has not published yet is listed as a viewer).
- The frontend has unit tests and was checked with a headless browser against `tessera-loadtest`
  rooms, but there is no automated end-to-end suite yet.
