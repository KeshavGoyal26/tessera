# Performance

| File | Content |
|------|---------|
| `LOADTEST.md` | End-to-end load test results (`cmd/tessera-loadtest`), pprof findings and recommendations |
| `WP1.md` | Micro-benchmarks and profiles of the forwarding path |
| `benchgate.sh` | Benchmark regression gate used by `.github/workflows/bench.yml` |
| `baseline/bench.txt` | Committed baseline (`go test -bench` output, 6 samples per benchmark) |

## Benchmark regression gate

`.github/workflows/bench.yml` runs `docs/perf/benchgate.sh` when started by hand (`gh workflow run bench`)
(about 1 minute including the Go build). It runs the benchmarks of `pkg/rtc`, `pkg/rtc/buffer` and
`pkg/rtc/forward` six times and compares them with `baseline/bench.txt` using `benchstat`. The job fails
when a benchmark is slower in sec/op by more than `BENCH_MAX_REGRESSION` percent (default 40) **and**
benchstat reports the difference as significant (p < 0.05). 40% leaves room for shared-runner noise
(single benchmarks vary by 15 to 30% run to run); real regressions on the packet path are usually larger.

The baseline was recorded on a 4 vCPU Xeon container, not on a GitHub runner. After the first green run on
`main`, replace it with the `bench-results` artifact of that run so the numbers match the runner class:

```bash
cp bench-new.txt docs/perf/baseline/bench.txt   # artifact from CI, or: docs/perf/benchgate.sh --update
```

Run locally with `docs/perf/benchgate.sh` (needs `benchstat`:
`go install golang.org/x/perf/cmd/benchstat@be2c69fb417e`). The gate itself was checked by feeding it a
baseline with one benchmark made 2.5x slower (fails, exit 1) and the baseline itself (passes).
