# deploy

| Path | Purpose |
|------|---------|
| `docker/Dockerfile` | Multi-stage build; targets `tessera` (distroless, recording built in), `loadtest`, `web-demo` |
| `docker-compose.yml` | Local stack: server (records to MinIO), MinIO (+ bucket bootstrap), web demo, Prometheus, Grafana |
| `config/tessera.yaml` | Example config with `${ENV}` expansion (loaded by a test, so it cannot drift) |
| `.env.example` | Development environment values |

```bash
cp deploy/.env.example deploy/.env
docker compose -f deploy/docker-compose.yml --env-file deploy/.env up --build
```

Networking: WebRTC media must reach the server on `7882/udp` (or `7881/tcp`) at the address in
`rtc.node_ip`. With Docker Desktop, publish the ports and set `TESSERA_NODE_IP=127.0.0.1` for local
tests; on Linux servers prefer `network_mode: host`.

## Verification status (2026-11-05, WP5 session E)

Verified in a sandbox with a Docker daemon (Docker 29.9, no direct internet; builds used `--network host`
and an extra CA, which are sandbox workarounds that are not part of the committed files):

| Item | Result |
|------|--------|
| `docker compose config` | valid |
| `tessera` image (distroless, target `tessera`) | builds, 31 MB; container starts, `/healthz` 200 on :7880 and :6789 |
| `web-demo` image (npm build + nginx) | builds, serves 200 on :8080 |
| Media through the container | `tessera-loadtest` against the published ports (`TESSERA_NODE_IP=127.0.0.1`): 3 clients, 0 failures, 0% loss, p99 1.2 ms |
| Prometheus v2.53.0 | up; target `tessera:6789` UP |
| Grafana 11.1.0 | healthy; Prometheus datasource and the "Tessera" dashboard provisioned |

**Not verified**

- `recorder` image: `apt-get install ffmpeg` could not reach the Debian mirror from the sandbox, so the image was
  never built and the recorder container never ran (the Prometheus target `recorder:6790` is therefore DOWN
  in that run). The recorder code itself is covered by `go test ./pkg/recorder/... ./test/e2e` with FFmpeg.
- MinIO and `mc`: the previously pinned `minio/minio:RELEASE.2025-09-07T16-13-09Z` and
  `minio/mc:RELEASE.2025-08-13T08-35-41Z` tags **do not exist on Docker Hub** (pull is denied; MinIO stopped
  publishing there). The compose file now points at `quay.io/minio/minio` and `quay.io/minio/mc` with the same
  tags, but quay.io was blocked in the sandbox, so those tags are **unconfirmed**. If they fail to pull, pick
  an available S3-compatible image (any S3 endpoint works: set `storage.s3.endpoint` in `config/recorder.yaml`).
- The full recorder -> MinIO -> HLS playback path in containers, browser access to the stack, TURN.

Networking: WebRTC media must reach the server on `7882/udp` (or `7881/tcp`) at the address in
`rtc.node_ip`. With Docker Desktop, publish the ports and set `TESSERA_NODE_IP=127.0.0.1` for local
tests; on Linux servers prefer `network_mode: host`. For production see `docs/DEPLOYMENT.md`.
