# Production deployment runbook

Single-node deployment (Phase 1). Multi-node placement is Phase 2 and not implemented, so scale vertically
and by sharding rooms across independent nodes in your own routing layer. Measured numbers are in
`docs/perf/LOADTEST.md`; every capacity figure below is a lower bound from a shared 4 vCPU machine unless noted.

## 1. Ports

| Port | Proto | Purpose | Expose |
|------|-------|---------|--------|
| 7880 | TCP | HTTP + WebSocket signalling, admin API (JWT protected, see PROTOCOL.md) | via TLS reverse proxy only |
| 7882 | UDP | WebRTC media (single muxed port, `rtc.udp_port`) | public, open in firewall / security group |
| 7881 | TCP | ICE-TCP fallback (`rtc.tcp_port`); the server starts UDP-only if it cannot bind it | public, optional |
| 3478 UDP/TCP, 5349 TCP | | Embedded TURN, TURNS (only if `turn.enabled`) | public |
| `turn.relay_range_start..end` | UDP | TURN relay allocations | public when TURN is enabled |
| 6789 | TCP | `/metrics`, `/healthz`, `/readyz`, `/stats`, `/debug/pprof` | **private network only** |
| 6790 | TCP | Recorder metrics | private |

One UDP port serves all participants, so the firewall needs a single rule (`rtc.port_range_start/end` exists
for environments that insist on a range). Use `/readyz` for load balancer and orchestrator probes.

## 2. Addressing and NAT 1:1 (cloud VMs)

The server must advertise an address the browser can reach.

- **VM with a public IP on the NIC**: set `rtc.use_external_ip: true` (STUN discovery) or `rtc.node_ip`.
- **Cloud 1:1 NAT (AWS, GCP, Azure: the VM only sees its private IP)**: set `rtc.node_ip` to the public
  (elastic) IP. Candidates carry that address while the socket stays bound to the private one. This is the
  recommended setting; do not rely on STUN discovery behind NAT.
- **Docker**: publish `7882/udp` and `7881/tcp` and set `TESSERA_NODE_IP` to the host's reachable IP. On Linux
  production hosts prefer `network_mode: host` (avoids docker-proxy and conntrack cost on UDP).
- **Kubernetes**: use `hostNetwork: true` or a `hostPort` DaemonSet-style deployment per node; a Service of
  type LoadBalancer with UDP works but adds a hop and cannot pin a client to one node. No Helm chart yet.

Check with the browser's `chrome://webrtc-internals` that the selected candidate pair is the public IP, and
`curl :6789/stats` for `ice_candidate_type` per participant.

## 3. TLS

The signalling listener speaks plain HTTP/WebSocket. Terminate TLS in front (nginx, Caddy, an ALB/ingress)
and proxy `wss://` to `:7880` with WebSocket upgrade, `proxy_read_timeout` above `signal.ping_timeout` (default 15 s)
and `proxy_http_version 1.1`. Browsers require HTTPS/WSS for `getUserMedia`
except on `localhost`. Media is always DTLS-SRTP and needs no certificate setup. TURNS (`turn.tls_port`)
requires `turn.cert_file` and `turn.key_file` for the TURN domain.

Minimal Caddy example:

```
rtc.example.com {
    reverse_proxy 127.0.0.1:7880
}
```

## 4. TURN

Enable the embedded TURN when clients sit behind symmetric NATs or UDP-blocking firewalls (typically 10 to 15%
of enterprise users): `turn.enabled: true`, `turn.domain`, a stable `turn.secret` (otherwise credentials do not
survive a restart), and TLS on 5349 (or 443 via a TCP load balancer) for networks that only allow TLS.
Relay traffic is not cheaper than SFU traffic: budget bandwidth for it. External coturn also works through
`rtc.ice_servers`.

## 5. Configuration checklist

- `development: false`; secrets in `keys:` of at least 32 bytes, loaded from the environment or `key_file`,
  never from git. Rotate by adding a second key, moving clients, removing the old one.
- `limits.max_rooms`, `limits.max_participants` and `limits.joins_per_second_per_ip` set to values for the
  host (see section 6); `room.empty_timeout` to bound idle rooms.
- `logging.level: info`, JSON logs on (default outside development).
- `metrics.addr` bound to a private interface, `metrics.pprof` only if that interface is trusted.
- Raise the file descriptor limit (`LimitNOFILE=1048576` in systemd) and, for sustained traffic,
  `net.core.rmem_max` / `wmem_max` to at least `rtc.socket_buffer_size` (default 25 MiB; the kernel silently
  clamps lower values and bursts are then dropped as UDP receive errors).
- Webhooks: set `webhook.endpoints` and a `webhook.api_key`; delivery is queued and retried, not durable
  across restarts.
- Recording: run `tessera-recorder` separately (needs FFmpeg) pointing at `server_url`, plus S3-compatible
  storage; recorders scale out horizontally and are pull-based.

## 6. Scaling limits (measured and estimated)

Measured on 4 vCPUs with the load generator on the same host (`docs/perf/LOADTEST.md`):

| Load | CPU | Memory |
|------|-----|--------|
| 1 publisher (3 layers) + 100 subscribers, 22k egress pkt/s | 36% of one core | 140 MB |
| 50 rooms x 4 publishers full mesh (200 clients, 64k egress pkt/s, 111 Mbit/s) | 1.3 cores | 1.35 GB |

Rules of thumb for sizing (leave 30% headroom):

- CPU: about 25k egress packets per second per core including all protocol overhead; a 720p subscriber
  receives roughly 300 to 500 pkt/s per video stream, an audio stream 50 pkt/s.
- Memory: about 0.75 MB per received stream (three video layers plus audio = 3 MB per publisher) plus
  about 1 MB per connection. 1 GB of RAM per ~300 published streams. This is the first limit you will hit in
  large meetings; see the recommendations in `docs/perf/LOADTEST.md`.
- Network: egress bandwidth is the sum of subscriber bitrates; 1 Gbit/s NIC saturates near 3,000 to 4,000
  720p subscribers. Ingress is small.
- Webinar fan-out per publisher is bounded by one `readLoop` goroutine per stream; very large rooms
  (thousands) need cascading relay (Phase 3).
- Sustained tests from a separate machine against the reference hardware are still open (targets P5/P6 in
  `docs/ARCHITECTURE.md` section 9 are not yet confirmed).

Scale out by running independent nodes and routing each room to one node (hash room name to node). A room
cannot span nodes yet.

## 7. Monitoring and alerting

- Scrape `:6789/metrics` (Prometheus) and `recorder:6790/metrics`. `deploy/grafana/` has a provisioned
  Grafana dashboard (`tessera-dashboard.json`) and the Prometheus example config.
- `GET :6789/stats` returns node CPU, RSS, GC pause, UDP pps and per-room/per-participant quality (also used by
  the admin dashboard).
- Alert on: `readyz` failing; process CPU above 70% of cores for 5 min; RSS above 80% of RAM; GC pause p99
  above 20 ms; UDP receive errors or socket buffer drops (`nstat -az UdpRcvbufErrors`); participants with
  `connection_quality` poor for over 2 min; recorder job failures.
- Capture a profile of a hot node with
  `curl 'http://127.0.0.1:6789/debug/pprof/profile?seconds=30' -o cpu.pprof` and `go tool pprof`.
- Upgrades: drain by setting the node out of rotation, wait for rooms to empty (`/stats`), restart.
  The signalling resume window (`signal.resume_window`) covers short WebSocket drops, not process restarts.

## 8. Docker Compose

`deploy/docker-compose.yml` runs the full demo stack (server, recorder, MinIO, web demo, Prometheus,
Grafana). It is for evaluation, not production; see `deploy/README.md` for what has been verified.
