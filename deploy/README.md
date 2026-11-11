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
cp deploy/.env.example deploy/.env
docker compose -f deploy/docker-compose.yml --env-file deploy/.env up --build           # single server
docker compose -f deploy/docker-compose.cluster.yml --env-file deploy/.env up --build   # cluster
```

| | Single | Cluster |
|---|---|---|
| Web demo | http://localhost:8080/?url=ws://localhost:7880 | http://localhost:8080/?url=ws://localhost:7930 (or 7940) |
| Dashboard | http://localhost:7880/admin/ | http://localhost:7930/admin/ |
| Recordings | served by the server on 7880 | each media node serves its own (6791, 6792) |

Login: `devkey` / `TESSERA_DEV_SECRET`. Recordings stay inside the container (`/tmp/recordings`)
and are lost with it; use `recording.storage.kind: s3` in production.

Networking: WebRTC media must reach each server's UDP (or TCP) port at `TESSERA_NODE_IP`. With
Docker Desktop, publish the ports (as the compose files do); on Linux servers prefer
`network_mode: host`. The cluster files publish the media nodes too because they run without
TURN; in production only edges are public (ADR-0012).
