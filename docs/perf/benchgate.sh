#!/usr/bin/env bash
# Benchmark regression gate. Runs the forwarding-path benchmarks and compares them with the committed
# baseline using benchstat. Fails when a benchmark's sec/op is significantly (p<0.05) slower than the
# baseline by more than BENCH_MAX_REGRESSION percent.
#
#   docs/perf/benchgate.sh            # run + compare
#   docs/perf/benchgate.sh --update   # run and overwrite the baseline
#
# The baseline is hardware dependent. Refresh it from the CI artifact ("bench-results") of a green run on
# main so it matches the GitHub runner class, not a developer machine (see docs/perf/README.md).
set -euo pipefail
cd "$(dirname "$0")/../.."

BASELINE=docs/perf/baseline/bench.txt
OUT=${BENCH_OUT:-bench-new.txt}
COUNT=${BENCH_COUNT:-6}
BENCHTIME=${BENCH_TIME:-300ms}
MAX=${BENCH_MAX_REGRESSION:-40}
PKGS=${BENCH_PKGS:-./pkg/rtc/ ./pkg/rtc/buffer/ ./pkg/rtc/forward/}
PATTERN=${BENCH_PATTERN:-.}

# shellcheck disable=SC2086
if [[ -z ${BENCH_SKIP_RUN:-} ]]; then # BENCH_SKIP_RUN=1: only compare an existing $OUT (used to test the gate)
  go test -run '^$' -bench "$PATTERN" -benchmem -benchtime "$BENCHTIME" -count "$COUNT" $PKGS | tee "$OUT"
fi

if [[ ${1:-} == --update ]]; then
  cp "$OUT" "$BASELINE"
  echo "baseline updated: $BASELINE"
  exit 0
fi

BENCHSTAT=$(command -v benchstat || echo "$(go env GOPATH)/bin/benchstat")
if [[ ! -x $BENCHSTAT ]]; then
  go install golang.org/x/perf/cmd/benchstat@be2c69fb417e
fi

CSV=$(mktemp)
"$BENCHSTAT" -format csv "$BASELINE" "$OUT" > "$CSV" || true
"$BENCHSTAT" "$BASELINE" "$OUT" || true

# CSV sections: "name,old,CI,new,CI,delta,p" per unit table. Only the sec/op table is gated.
awk -F, -v max="$MAX" '
  /^,sec\/op/ {sec=1; next}
  /^,/ && !/^,sec\/op/ {sec=0}
  sec && NF>=7 && $1!="geomean" && $1!="" {
    d=$6; gsub(/[+%]/,"",d)
    if ($7 != "" && $6 ~ /%/ && d+0 > max) { printf "REGRESSION %s: %s (p=%s)\n", $1, $6, $7; bad=1 }
  }
  END { exit bad }' "$CSV" || { echo "benchmark regression above ${MAX}% vs $BASELINE"; exit 1; }
echo "benchmarks within ${MAX}% of baseline"
