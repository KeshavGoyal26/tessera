#!/usr/bin/env bash
# Start a local cluster (ADR-0012): Redis, 2 media nodes, 2 edges and the web demo, in the
# background. Stop it with scripts/cluster-stop.sh. State, logs and recordings: .cluster/
#
#   media m1 :7910  m2 :7920 (private)      edges e1 :7930  e2 :7940 (clients connect here)
#   Redis :6390                              web demo :5174 (?url=ws://<host>:7930 or :7940)
set -euo pipefail
cd "$(dirname "$0")/.."
D="$PWD/.cluster"
command -v redis-server >/dev/null || { echo "redis-server not found (brew install redis)"; exit 1; }
[ -f "$D/pids" ] && { echo "already running; stop it with scripts/cluster-stop.sh"; exit 1; }
mkdir -p "$D/bin" "$D/logs"
HOST=$(ipconfig getifaddr en0 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}' || true)
HOST=${HOST:-127.0.0.1}

echo "==> building"
go build -o "$D/bin/" ./cmd/tessera ./cmd/tessera-loadtest
[ -d sdk/js/dist ] || (cd sdk/js && npm ci --silent && npm run build --silent)
[ -d examples/web-demo/node_modules ] || (cd examples/web-demo && npm ci --silent)

redis-server --port 6390 --save "" --appendonly no --daemonize yes --logfile "$D/logs/redis.log"

node_cfg() { # id role port metrics
  local rec="recording: { enabled: false }"
  [ "$2" = media ] && rec="recording: { temp_dir: $D/$1/tmp, storage: { kind: local, local_root: $D/$1/rec } }"
  mkdir -p "$D/$1"
  cat > "$D/$1.yaml" <<YAML
node_id: $1
port: $3
keys: { devkey: devsecret-devsecret-devsecret-0000 }
rtc: { ips: [$HOST], udp_port: $(($3+2)), tcp_port: $(($3+1)) }
metrics: { addr: "127.0.0.1:$4" }
logging: { level: warn }
$rec
cluster:
  role: $2
  private_url: http://127.0.0.1:$3
  redis: { addr: "127.0.0.1:6390" }
YAML
}
: > "$D/pids"
for spec in "m1 media 7910 6791" "m2 media 7920 6792" "e1 edge 7930 6793" "e2 edge 7940 6794"; do
  set -- $spec
  node_cfg "$@"
  nohup "$D/bin/tessera" --dev --config "$D/$1.yaml" > "$D/logs/$1.log" 2>&1 < /dev/null &
  echo "$! $1" >> "$D/pids"
done
(cd examples/web-demo && nohup npx vite --host 0.0.0.0 --port 5174 --strictPort > "$D/logs/web.log" 2>&1 < /dev/null) > /dev/null 2>&1 &
echo "$! web" >> "$D/pids"
sleep 4
while read -r pid name; do kill -0 "$pid" 2>/dev/null || { echo "$name failed:"; tail -5 "$D/logs/$name.log"; }; done < "$D/pids"
cat <<TXT

  Tessera cluster is running (2 media nodes, 2 edges, Redis)
  ------------------------------------------------------------
  Live demo via e1  http://localhost:5174/?url=ws://$HOST:7930
  Live demo via e2  http://localhost:5174/?url=ws://$HOST:7940
  Video call        http://localhost:5174/call.html?url=ws://$HOST:7930
  Dashboard (e1)    http://localhost:7930/admin/   login: devkey / devsecret-devsecret-devsecret-0000
  Logs              $D/logs/   recordings: $D/m1/rec, $D/m2/rec
  Stop with:        scripts/cluster-stop.sh
TXT
