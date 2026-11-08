#!/usr/bin/env bash
# Runs the jobs of .github/workflows/ci.yml on this machine (the GitHub workflows run only on
# demand, to save Actions minutes). Needs Go, Node 22, Java 17 + Maven and FFmpeg; buf,
# protoc-gen-go and golangci-lint run at their pinned versions through `go run`.
#
#   scripts/ci-local.sh                 # every job
#   scripts/ci-local.sh go go-e2e       # only these jobs
#
# Jobs: proto go js java browser recorder go-e2e dashboard. The benchmark gate is left out: its
# baseline matches the GitHub runner, not this machine (run docs/perf/benchgate.sh by hand).
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
JOBS=("$@")
[ ${#JOBS[@]} -eq 0 ] && JOBS=(proto go js java browser recorder go-e2e dashboard)

BUF="go run github.com/bufbuild/buf/cmd/buf@v1.75.0"
LINT="go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0"
GOBIN_DIR=$(mktemp -d)
trap 'rm -rf "$GOBIN_DIR"' EXIT

proto() {
  $BUF lint
  GOBIN=$GOBIN_DIR go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
  PATH="$GOBIN_DIR:$PATH" $BUF generate
  git diff --exit-code -- gen/go
}

go_job() {
  go mod tidy && git diff --exit-code -- go.mod go.sum
  go build ./...
  go vet ./...
  $LINT run
  go test -race -count=1 ./...
  go test -run '^$' -bench . -benchtime 1x ./pkg/... > /dev/null
}

js() {
  (cd sdk/js && npm ci && npm run lint && npm run typecheck && npm run build && npm test && npm run size)
  (cd sdk/js && npm run generate && git diff --exit-code -- src/gen)
  (cd examples/web-demo && npm ci && npm run build)
}

java() { (cd sdk/java && mvn -B -ntp -q verify); }

browser() {
  # Playwright's macOS WebKit build does not connect WebRTC (ICE times out), so on a Mac only
  # Chromium and Firefox run; WebKit runs in the GitHub workflow (Linux).
  local browsers=(chromium firefox webkit)
  [ "$(uname)" = Darwin ] && browsers=(chromium firefox)
  (cd sdk/js && npm ci && npm run build)
  (cd examples/web-demo && npm ci)
  (cd sdk/js && npx playwright install "${browsers[@]}" &&
    npx playwright test "${browsers[@]/#/--project=}")
}

recorder() {
  go test -race -count=1 -v ./pkg/hls/... ./pkg/recorder/... ./pkg/storage/... > "$ROOT/recorder-test.log" 2>&1 ||
    { tail -40 "$ROOT/recorder-test.log"; return 1; }
  if grep -E -- '--- SKIP' "$ROOT/recorder-test.log"; then echo "FFmpeg tests were skipped"; return 1; fi
  rm -f "$ROOT/recorder-test.log"
}

go_e2e() { go test -race -count=1 -timeout 5m ./test/e2e/; }

dashboard() {
  (cd web/admin && npm ci && npm run typecheck && npm test && npm run build)
  git diff --exit-code -- pkg/dashboard/dist
}

failed=()
for job in "${JOBS[@]}"; do
  fn=${job//-/_}
  [ "$fn" = go ] && fn=go_job
  if ! declare -F "$fn" > /dev/null; then echo "unknown job: $job" >&2; exit 2; fi
  echo "=== $job"
  start=$SECONDS
  if "$fn"; then echo "=== $job ok ($((SECONDS - start))s)"; else echo "=== $job FAILED"; failed+=("$job"); fi
done
if [ ${#failed[@]} -gt 0 ]; then echo "failed: ${failed[*]}"; exit 1; fi
echo "all jobs passed: ${JOBS[*]}"
