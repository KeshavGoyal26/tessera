#!/usr/bin/env bash
# Run the whole Tessera demo natively (no Docker):
#   tessera --dev, tessera-recorder (local-disk storage) and the web demo dev server.
#
#   scripts/demo.sh [--loadtest] [--host IP]
#
#   --loadtest   also start tessera-loadtest for background traffic (room "loadtest")
#   --host IP    address to print in URLs and advertise for media (default: first LAN IP)
#
# Ctrl-C stops everything. State lives under .demo/ (git-ignored).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
DEMO="$ROOT/.demo"
KEY=devkey
SECRET=devsecret-devsecret-devsecret-0000
PORT=7880
WEB_PORT=5173
LOADTEST=0
HOST=""

while [ $# -gt 0 ]; do
  case "$1" in
    --loadtest) LOADTEST=1 ;;
    --host) HOST="${2:?--host needs an address}"; shift ;;
    -h|--help) sed -n 2,11p "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required tool: $1" >&2; exit 1; }; }
need go; need node; need npm

# Portable LAN IP detection: macOS ipconfig, Linux hostname -I, else loopback. Never fails.
detect_ip() {
  local ip=""
  if command -v ipconfig >/dev/null 2>&1; then
    for ifc in en0 en1 en2; do
      ip="$(ipconfig getifaddr "$ifc" 2>/dev/null || true)"
      [ -n "$ip" ] && break
    done
  fi
  if [ -z "$ip" ] && command -v hostname >/dev/null 2>&1; then
    ip="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
  fi
  echo "${ip:-127.0.0.1}"
}
[ -n "$HOST" ] || HOST="$(detect_ip)"

for p in $PORT $WEB_PORT; do
  if (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
    echo "port $p is already in use; stop whatever is listening there first" >&2
    exit 1
  fi
done

mkdir -p "$DEMO/bin" "$DEMO/recordings" "$DEMO/tmp" "$DEMO/logs"

echo "==> building Go binaries"
go build -o "$DEMO/bin/" ./cmd/tessera ./cmd/tessera-recorder ./cmd/tessera-loadtest

echo "==> preparing web demo"
if [ ! -d sdk/js/dist ]; then
  (cd sdk/js && npm ci --silent && npm run build --silent)
fi
[ -d examples/web-demo/node_modules ] || (cd examples/web-demo && npm ci --silent)

RTC_IPS=""
if [[ "$HOST" =~ ^[0-9.]+$ ]]; then
  if [ "$HOST" = 127.0.0.1 ]; then ips='["127.0.0.1"]'; else ips="[\"$HOST\", \"127.0.0.1\"]"; fi
  RTC_IPS="rtc:
  ips: $ips"
fi
cat > "$DEMO/tessera.yaml" <<EOF
development: true
port: $PORT
keys:
  $KEY: $SECRET
$RTC_IPS
EOF

cat > "$DEMO/recorder.yaml" <<EOF
development: true
server_url: http://127.0.0.1:$PORT
api_key: $KEY
secret: $SECRET
worker_id: demo-recorder
capacity: 20
temp_dir: $DEMO/tmp
storage:
  kind: local
  local_root: $DEMO/recordings
  local_base_url: http://127.0.0.1:6790/recordings
hls:
  segment_duration: 4s
  audio_codec: opus
metrics:
  addr: "127.0.0.1:6790"
EOF

PIDS=()
kill_tree() { # pid: kill descendants first, then the process
  local c
  for c in $(pgrep -P "$1" 2>/dev/null || true); do kill_tree "$c"; done
  kill "$1" 2>/dev/null || true
}
cleanup() {
  trap - INT TERM EXIT
  echo; echo "==> stopping"
  for pid in "${PIDS[@]:-}"; do [ -n "$pid" ] && kill_tree "$pid"; done
  wait 2>/dev/null || true
}
trap cleanup INT TERM EXIT

start() { # name cmd...
  local name="$1"; shift
  "$@" >"$DEMO/logs/$name.log" 2>&1 &
  PIDS+=("$!")
}

# The dashboard shows the recorder's live stats and finished recordings.
TESSERA_DASHBOARD_RECORDERS="http://127.0.0.1:6790" start tessera "$DEMO/bin/tessera" --config "$DEMO/tessera.yaml" --dev

for _ in $(seq 1 50); do
  curl -fs "http://127.0.0.1:$PORT/" >/dev/null 2>&1 && break
  if ! kill -0 "${PIDS[0]}" 2>/dev/null; then echo "tessera failed to start:" >&2; tail -20 "$DEMO/logs/tessera.log" >&2; exit 1; fi
  sleep 0.2
done

start recorder "$DEMO/bin/tessera-recorder" --config "$DEMO/recorder.yaml"
(cd examples/web-demo && exec npx vite --host 0.0.0.0 --port "$WEB_PORT" --strictPort) >"$DEMO/logs/web.log" 2>&1 &
PIDS+=("$!")

if [ "$LOADTEST" = 1 ]; then
  start loadtest "$DEMO/bin/tessera-loadtest" --url "ws://127.0.0.1:$PORT" --api-key "$KEY" --secret "$SECRET" \
    --rooms 2 --publishers 4 --subscribers 2 --duration 8h --ramp-up 10s
fi

sleep 1
cat <<EOF

  Tessera demo is running
  ------------------------------------------------------------
  Web demo      http://localhost:$WEB_PORT/          (open in two tabs; LAN: http://$HOST:$WEB_PORT/)
  Dashboard     http://localhost:$PORT/admin/        login: $KEY / $SECRET
  Live stats    http://localhost:6789/stats
  Token API     http://localhost:$PORT/dev/token?room=demo&identity=me
  Recordings    $DEMO/recordings
  Logs          $DEMO/logs/*.log
EOF
[ "$LOADTEST" = 1 ] && echo "  Load test     running in rooms from the loadtest tool (see $DEMO/logs/loadtest.log)"
echo
echo "  Camera/mic need https or localhost: on another device use a tunnel or Chrome's"
echo "  insecure-origin flag (see docs/DEMO.md). Press Ctrl-C to stop."

# Exit if the server dies; otherwise wait for Ctrl-C.
while kill -0 "${PIDS[0]}" 2>/dev/null; do sleep 1; done
echo "tessera exited; see $DEMO/logs/tessera.log" >&2
