# deploy

| Path | Purpose |
|------|---------|
| `docker/Dockerfile` | Multi-stage build; targets `tessera` (distroless, recording built in), `loadtest`, `web-demo` |
| `docker-compose.yml` | Single server + web demo |
| `docker-compose.cluster.yml` | Cluster (ADR-0012): Redis, media nodes m1 m2, edges e1 e2, web demo |
| `config/tessera.yaml` | Single-server config with `${ENV}` expansion (loaded by a test, so it cannot drift) |
| `config/cluster-node.yaml` | One config for every cluster node, set per service through the environment |
| `.env.example` | Development environment values |
| `grafana/` | Prometheus scrape config and a Grafana dashboard, for use with your own Prometheus/Grafana |

```bash
scripts/docker.sh start            # cluster: build if needed, start, wait, print URLs
scripts/docker.sh start single     # single server
scripts/docker.sh stop [single]    # also: build, status, logs [service]
```

The script creates `deploy/.env` from `.env.example` on first run (with this machine's LAN IP) and
removes the images a rebuild replaces. Build settings (command line or `deploy/.env`):
`NPMRC=~/.npmrc` (npm registry and credentials), `NPM_REGISTRY=<url>`, `GOPROXY=<url>`; behind a
proxy that re-signs HTTPS, see `docker/local/README.md`. Plain compose works too:
`docker compose -f deploy/docker-compose.cluster.yml --env-file deploy/.env up -d --build`.

| | Single | Cluster |
|---|---|---|
| Web demo | http://localhost:5173/?url=ws://localhost:7880 | http://localhost:5173/?url=ws://localhost:7930 (or 7940) |
| Dashboard | http://localhost:7880/admin/ | http://localhost:7930/admin/ |
| Recordings | served by the server on 7880 | each media node serves its own (6791, 6792) |

Login: `devkey` / `TESSERA_DEV_SECRET`. Recordings stay inside the container (`/tmp/recordings`)
and are lost with it; use `recording.storage.kind: s3` in production.

Networking: WebRTC media must reach each server's UDP (or TCP) port at `TESSERA_NODE_IP`. With
Docker Desktop, publish the ports (as the compose files do); on Linux servers prefer
`network_mode: host`. The cluster files publish the media nodes too because they run without
TURN; in production only edges are public (ADR-0012).
