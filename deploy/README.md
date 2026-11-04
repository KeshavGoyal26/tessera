# deploy

| Path | Purpose |
|------|---------|
| `docker/Dockerfile` | Multi-stage build; targets `tessera` (distroless), `recorder` (Debian slim + FFmpeg), `loadtest` |
| `docker-compose.yml` | Local stack: server, recorder, MinIO (+ bucket bootstrap) |
| `config/tessera.yaml`, `config/recorder.yaml` | Example configs with `${ENV}` expansion |
| `.env.example` | Development environment values |

```bash
cp deploy/.env.example deploy/.env
docker compose -f deploy/docker-compose.yml --env-file deploy/.env up --build
```

Networking: WebRTC media must reach the server on `7882/udp` (or `7881/tcp`) at the address in
`rtc.node_ip`. With Docker Desktop, publish the ports and set `TESSERA_NODE_IP=127.0.0.1` for local
tests; on Linux servers prefer `network_mode: host`.

Status (Phase 0): images build the stub binaries; the stack becomes useful as WP1 and WP4 land.
MinIO image tags are pinned and should be re-checked by WP5. Owned by WP5 (Helm chart, Grafana
dashboard and production hardening come later).
