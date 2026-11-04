# ADR-0010: Observability and deployment

- Status: accepted
- Date: 2026-11-04
- Deciders: Tessera maintainers

## Context

Operators need to know whether a node is healthy, how loaded it is, and why a call was bad.
Performance work needs profiles from production-like load.

## Decision

1. **Logging**: `log/slog`, JSON handler in production, text handler in development; level
   configurable at runtime (`PUT /debug/loglevel`). Logs carry `room`, `participant`,
   `track` attributes. Nothing on the per-packet path logs (rate-limited counters instead).
2. **Metrics**: Prometheus (`github.com/prometheus/client_golang`), served on a separate
   listener (default `:6789`) at `/metrics`. Each package registers its own collectors
   through a `prometheus.Registerer` passed at construction (no global registry use in
   libraries). Naming: `tessera_<subsystem>_<name>_<unit>`. Per-packet counters are kept in
   per-track atomics and exported by a collector at scrape time (no per-packet metric calls).
   Required series include rooms, participants, published/subscribed tracks, packets and
   bytes in/out, NACKs, PLIs, retransmissions, packet loss, jitter, forwarding latency
   histogram (sampled), BWE estimates, allocator layer changes, signal messages, admin API
   requests, webhook deliveries, recorder jobs and segment upload latency.
3. **Profiling**: `net/http/pprof` on the metrics listener only (never on the public port),
   plus `/debug/trace` for execution traces.
4. **Health**: `GET /healthz` (process alive) and `GET /readyz` (UDP mux bound, config valid,
   not draining; recorder: storage reachable and below capacity). On `SIGTERM` the server
   enters **drain**: readiness fails, new joins are refused, connected clients are asked to
   move (`LeaveRequest{action=RECONNECT}`) after `drain_timeout`.
5. **Deployment**: multi-stage Dockerfiles (`deploy/docker/`), distroless static images for
   `tessera`, a Debian-slim image with FFmpeg for `tessera-recorder`; `deploy/docker-compose.yml`
   runs server, recorder and MinIO (with bucket bootstrap). Containers use host networking on
   Linux for media (or explicit port publishing with `rtc.node_ip`). Helm charts later (WP5).
6. Optional OpenTelemetry tracing of signaling/admin calls is a later milestone; not on the
   media path.

## Consequences

- Standard tooling (Prometheus, Grafana, pprof) works out of the box; a Grafana dashboard is
  a WP5 deliverable.
- Discipline needed to keep metrics off the hot path; enforced by benchmarks (zero allocs).
