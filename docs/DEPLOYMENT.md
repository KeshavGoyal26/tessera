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
| 3478 UDP/TCP | | Embedded TURN (only if `turn.enabled`) | public |
| 443 (or 5349) TCP | | TURN over TLS, `turn.tls_port` (needs `turn.cert_file`/`turn.key_file`; off by default) | public |
| `turn.relay_range_start..end` | UDP | TURN relay allocations | public when TURN is enabled |
| 6789 | TCP | `/metrics`, `/healthz`, `/readyz`, `/stats`, `/debug/pprof` | **private network only** |

One UDP port serves all participants, so the firewall needs a single rule. Use `/readyz` for load balancer and orchestrator probes.

## 2. Addressing and NAT 1:1 (cloud VMs)

The server must advertise an address the browser can reach.

- **VM with a public IP on the NIC**: nothing to set, or set `rtc.node_ip` to that IP.
- **Cloud 1:1 NAT (AWS, GCP, Azure: the VM only sees its private IP)**: set `rtc.node_ip` to the public
  (elastic) IP. Candidates carry that address while the socket stays bound to the private one. This is the
  recommended setting; do not rely on STUN discovery behind NAT.
- **Docker**: publish `7882/udp` and `7881/tcp` and set `TESSERA_NODE_IP` to the host's reachable IP. On Linux
  production hosts prefer `network_mode: host` (avoids docker-proxy and conntrack cost on UDP).
- **TURN from the environment** (overrides the config file, e.g. in Docker): `TESSERA_TURN_URLS`
  (comma-separated) with `TESSERA_TURN_SECRET` (shared secret, per-session credentials) or
  `TESSERA_TURN_USERNAME`/`TESSERA_TURN_CREDENTIAL` (fixed); `TESSERA_FORCE_RELAY=true` makes clients
  use only TURN. With only `?transport=tcp`/`turns:` URLs that is TURN over TCP. The compose files
  pass `TURN_URLS`, `TURN_SECRET`, `TURN_USERNAME`, `TURN_CREDENTIAL` and `FORCE_RELAY` from `deploy/.env`.
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
survive a restart). Relay traffic is not cheaper than SFU traffic: budget bandwidth for it. External coturn also
works through `rtc.ice_servers`.

### TURN over TLS on 443

Many corporate networks only let TLS on port 443 through. Serve TURNS there:

```yaml
turn:
  enabled: true
  domain: turn.example.com        # must match the certificate
  secret: "<long random value>"   # stable across restarts
  udp_port: 3478
  tcp_port: 3478
  tls_port: 443                   # 0 (default) disables TURNS
  cert_file: /etc/tessera/turn.crt
  key_file: /etc/tessera/turn.key
```

The join response then also lists `turns:turn.example.com:443?transport=tcp`, so browsers fall back to it
automatically after UDP, ICE-TCP and `turn:` fail (the SDK passes the list to `RTCPeerConnection` unchanged; use
`forceRelay` in the client configuration to test it).

- The TURN listener binds port 443 on the node's media IP (`rtc.node_ip`). If the HTTPS reverse proxy for signalling
  also uses 443, give TURN a second IP (or a separate hostname/IP), or put a TCP SNI router such as HAProxy or nginx
  `stream` in front that sends `turn.example.com` to the TURN port and everything else to the proxy.
- Binding 443 needs root or `CAP_NET_BIND_SERVICE` (`setcap cap_net_bind_service=+ep tessera`, or
  `AmbientCapabilities=CAP_NET_BIND_SERVICE` in systemd). In containers use `--cap-add NET_BIND_SERVICE` or
  publish `443:5349` and set `tls_port: 5349`.
- The certificate must be valid for the TURN host name (Let's Encrypt works; reload by restarting). Browsers
  reject self-signed certificates for `turns:`.
- `go test ./test/e2e -run TestTURNOverTLSOnly` runs a client that can only reach the server through TURN/TLS: the
  server has no UDP or TCP TURN listener and the client keeps only the `turns:` URL with relay-only candidates.
  Without the CA in the client's trust store it fails, so the test also proves that TLS is verified.

## 5. Configuration checklist

- `development: false`; secrets in `keys:` of at least 32 bytes, loaded from the environment or `key_file`,
  never from git. Rotate by adding a second key, moving clients, removing the old one.
- `limits.max_rooms` and `limits.max_participants` set to values for the
  host (see section 6); `room.empty_timeout` to bound idle rooms.
- `logging.level: info`, JSON logs on (default outside development).
- `metrics.addr` bound to a private interface, `metrics.pprof` only if that interface is trusted.
- Raise the file descriptor limit (`LimitNOFILE=1048576` in systemd) and, for sustained traffic,
  `net.core.rmem_max` / `wmem_max` to at least `rtc.socket_buffer_size` (default 25 MiB; the kernel silently
  clamps lower values and bursts are then dropped as UDP receive errors).
- Webhooks: set `webhook.endpoints` and a `webhook.api_key`; delivery is queued and retried, not durable
  across restarts.
- Recording: built into the server (`recording:` config, S3-compatible or local storage). Recording
  shares the media node's CPU: size nodes for live plus recorded presenters (measured on a 4 vCPU GCP
  n2: 300 recorded 360p presenters use 47% of the box, 500 about 75%; `docs/perf/RECORDING-CAPACITY-PLAN.md`).
  `recording.capacity` defaults to 150 units (75 presenters) per CPU; alert on
  `tessera_recording_jobs_queued > 0`, since a queued recording misses its start. Set
  `recording.enabled: false` on media-only nodes. FFmpeg is needed only for the AAC audio mode.

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

## 6a. Two tiers: edges and private media servers (ADR-0012)

Edges are the only public machines; media servers (rooms, recording) stay private. Redis
(ElastiCache with a replica in production) holds the node registry and which media server
hosts each room. It is never on the media path: if it fails, running sessions continue and
new joins wait until it is back.

```yaml
# media server (private address 10.0.1.5)
node_id: media-1
cluster:
  role: media
  redis: { addr: redis.internal:6379, tls: true, password: "${REDIS_PASSWORD}" }
  private_url: http://10.0.1.5:7880     # how edges reach this server
rtc:
  turn_relay:                            # clients reach this server only through an edge's TURN
    urls: ["turn:edge.example.com:3478?transport=udp", "turns:edge.example.com:443?transport=tcp"]
    secret: "${TURN_SECRET}"             # the same secret as the edges' turn.secret
    force: true
room:
  lock_published_tracks: true            # presenters cannot mute or unpublish
```

```yaml
# edge (public)
node_id: edge-1
cluster:
  role: edge
  redis: { addr: redis.internal:6379, tls: true, password: "${REDIS_PASSWORD}" }
  private_url: http://10.0.0.7:7880      # optional: lets other nodes' dashboards show this edge
turn:
  enabled: true
  domain: edge.example.com
  secret: "${TURN_SECRET}"
  tls_port: 443
  relay_peers: ["10.0.1.0/24"]           # the media servers' subnet
recording:
  enabled: false
```

Clients use the edges' URL (`wss://edge.example.com/rtc`) as they would a single server.

- **Presenters** (tokens that may publish) are relayed to a media server: the one they are on
  (a reconnect resumes there), else a server already hosting their group with fewer than 250
  sessions, else the least-loaded live server that is not draining. A group can span servers.
  A presenter whose server disappears (no heartbeat for 10 s) is placed again when it rejoins.
- **Viewers** (tokens with `can_publish: false`) join the edge's own room. The edge relays the
  group's presenters there from every media server hosting the group: one hidden relay
  session per group and server over the private network. Only watched presenters flow: a
  track no viewer on the edge has subscribed to for 10 s is paused upstream (the media
  server stops sending it) and resumes, with a keyframe, when a viewer subscribes. Viewers
  subscribe on demand (`auto_subscribe: false`) to switch among presenters on one connection.
- The admin API can be called on any edge: presenter calls go to the presenter's media
  server, room calls to every server hosting the room (and the edge's viewers), lists are
  merged, and recordings are found through Redis. Webhooks come from the media servers.
- With S3 recording storage the crash journal lives in Redis: when a media server disappears
  (no heartbeat for 10 s), another media server closes its open recordings (playlists sealed,
  master written, `recording_ended` with status FAILED) within about 30 s, even if the machine
  never comes back. With local storage the journal stays on the machine's disk.

## 7. Monitoring and alerting

- Scrape `:6789/metrics` (Prometheus). `deploy/grafana/` has a provisioned
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

`deploy/docker-compose.yml` (single server + web demo) and `deploy/docker-compose.cluster.yml`
(Redis, 2 media nodes, 2 edges, web demo) are for local evaluation, not production; see
`deploy/README.md`.
