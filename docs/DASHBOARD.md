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
| Overview | CPU, memory, goroutines, GC, UDP packets and bandwidth, totals, one hour of charts, alerts |
| Rooms | rooms → participants → tracks; end room, remove participant, mute/unmute track, start recording (participant or track) |
| Track quality | per published track: bitrate, loss, jitter, resolution, NACK/PLI/FIR; per subscriber: bandwidth estimate, restarts, paused/muted state. Time series for the selected track are collected in the browser (last 15 min) |
| Connectivity | ICE candidate types, TURN (relay) usage, ICE/DTLS setup avg/p95/max, connection quality, RTT history |
| Recordings | recorder workers (capacity, backlog, segment delay, errors), active tracks, coordinator jobs with Stop, finished recordings with playback through hls.js (needs a playlist URL, e.g. S3/CDN) |

Light and dark theme (follows the OS, toggle in the header), responsive down to phone width.

## Alerts

Evaluated every second on the server: subscriber or publisher loss above 5 %, participant RTT
above 300 ms, CPU above 80 % of all cores, recorder unreachable, recorder upload/track errors,
failed recordings (last 15 min). Thresholds are `dashboard.Params` fields (`LossPercent`,
`RTTMs`, `CPUPercent`).

## Endpoints (all under `/admin/api/`, JSON, cookie auth)

| Endpoint | |
|----------|--|
| `POST login` `{api_key, secret}` | sets cookie, returns `{csrf}` |
| `GET session`, `POST logout` | |
| `GET snapshot` | latest `{snapshot, recorders, alerts, point}` |
| `GET events` | Server-Sent Events, event `update`, same payload every second |
| `GET history` | last hour of `Point`s (ring buffer, 3600 × 1 s, in memory, lost on restart) |
| `GET recordings` | coordinator's recording list |
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

- History is in memory only; multi-node aggregation is not implemented (one node per dashboard).
- Local-disk recordings cannot be played from the dashboard (only recordings with an HTTP
  playlist URL).
- The frontend is smoke-tested with a headless browser against synthetic stats, but there is no
  automated end-to-end suite yet; the live `pkg/room` stats were not available when this was
  written.
