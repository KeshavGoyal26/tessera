# WP5: Integration, load testing and performance (starts after WP1 M3)

**Goal:** prove and improve the performance targets in ARCHITECTURE.md section 9, make
deployment production-ready, and keep the documentation true.

## Owns (exclusive)

`cmd/tessera-loadtest`, `deploy/**`, `pkg/metrics`, `bench/**` (new: benchmark harness and
results), `docs/perf/**` (new), a `bench` CI job.

## Interfaces

- Consumes: `client.Client` (WP1), admin `Client` (WP3), all metrics exported by WP1/WP3/WP4.
- Provides: `metrics.NewRegistry`, `metrics.Handler`, `metrics.Serve` (keep signatures), load
  test CLI, Grafana dashboard JSON, Helm chart.

## Scope

1. **Load tester**: N rooms × M publishers × K subscribers, synthetic RTP publishers (VP8
   3-layer simulcast with keyframes every 2 s at realistic bitrates, Opus 20 ms frames with
   audio levels), subscribers that measure loss, jitter, end-to-end latency (timestamps
   embedded in payload), join time and time-to-first-frame; ramp-up; JSON + human reports.
2. **Scenarios** P5 (meeting), P6 (webinar), P7 (audio rooms), BWE-constrained subscribers,
   churn (joins/leaves per second), recording load (P11) with WP4.
3. **Profiling loop**: CPU and heap profiles, execution traces under load, GC tuning
   (`GOGC`/`GOMEMLIMIT` guidance), lock contention profiles; fixes go to the owning WP as
   requests or, after agreement, as small commits by WP5 reviewed by the owner.
4. **Networking optimisations** (with WP1): `SO_REUSEPORT` socket sharding,
   `recvmmsg`/`sendmmsg` batching, socket buffer tuning.
5. **Deployment**: hardened Docker images (non-root, read-only FS), compose with host
   networking profile, Helm chart, Grafana dashboard, alert rules, runbook.
6. **Multi-node design spike** (Redis relay, ARCHITECTURE.md section 8) as an ADR.

## Milestones

| ID | Deliverable |
|----|-------------|
| M1 | Load tester MVP (audio+video publish/subscribe, latency and loss reporting) |
| M2 | Benchmark harness and CI regression gate for micro-benchmarks (allocs, ns/op) |
| M3 | Scenario results P1–P12 on the reference machine documented in `docs/perf/` |
| M4 | Optimisation round (targets met or gaps explained with plans) |
| M5 | Helm chart, dashboard, runbook, multi-node ADR |

## Acceptance criteria

- Every target in ARCHITECTURE.md section 9 has a measured value, method and date in
  `docs/perf/results.md`.
- CI fails on new allocations in hot-path benchmarks or > 10% ns/op regressions (benchstat
  against the base commit).
- `docker compose up` gives a working stack (join from the web demo, record to MinIO).

## Required tests

Load tester unit tests (synthetic RTP generators, latency math), a short smoke load test in CI
(1 room, 3 participants, 20 s) asserting zero loss on loopback, compose smoke test (manual or
nightly).
