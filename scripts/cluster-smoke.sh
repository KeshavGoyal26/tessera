#!/usr/bin/env bash
# Cluster smoke test (ADR-0012): Redis, two media nodes and one edge as real processes on
# this machine; tessera-loadtest sends presenters (publish only) and viewers (listen only)
# through the edge. Needs redis-server on PATH. Usage: scripts/cluster-smoke.sh [seconds]
set -euo pipefail
DUR=${1:-30}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
W=$(mktemp -d)
trap 'pkill -f "$W/tessera" 2>/dev/null || true; redis-cli -p 6390 shutdown nosave >/dev/null 2>&1 || true' EXIT
(cd "$ROOT" && go build -o "$W/tessera" ./cmd/tessera && go build -o "$W/loadtest" ./cmd/tessera-loadtest)
for n in e1:7880:7900:7910:6790:edge m1:7890:7901:7911:6791:media m2:7891:7902:7912:6792:media; do
  IFS=: read -r id http udp tcp met role <<< "$n"
  {
    echo "node_id: $id"; echo "port: $http"
    echo "rtc: { udp_port: $udp, tcp_port: $tcp, ips: [127.0.0.1] }"
    echo "metrics: { addr: 127.0.0.1:$met }"
    echo "recording: { enabled: false }"
    echo "cluster:"; echo "  role: $role"; echo "  redis: { addr: 127.0.0.1:6390 }"
    [ "$role" = media ] && echo "  private_url: http://127.0.0.1:$http"
  } > "$W/$id.yaml"
done
redis-server --port 6390 --save "" --appendonly no > "$W/redis.log" 2>&1 &
sleep 1
for id in e1 m1 m2; do "$W/tessera" --dev --config "$W/$id.yaml" > "$W/$id.log" 2>&1 & done
sleep 3
"$W/loadtest" --url ws://127.0.0.1:7880 --secret devsecret-devsecret-devsecret-0000 --rooms 2 --publishers 3 \
  --subscribers 2 --publish-only --loopback --duration "${DUR}s" --ramp-up 5s --report 10s --json "$W/load.json" > "$W/load.log" 2>&1 &
LT=$!
sleep $((DUR / 2 + 5))
for m in 6791:m1 6792:m2 6790:e1; do
  IFS=: read -r port id <<< "$m"
  curl -s "127.0.0.1:$port/stats" | python3 -c "
import json, sys
for r in json.load(sys.stdin).get('rooms') or []:
    kinds = {}
    for p in r.get('participants') or []:
        kinds[p['kind']] = kinds.get(p['kind'], 0) + 1
    print('$id', r['name'], kinds)"
done
wait $LT
python3 -c "
import json; r = json.load(open('$W/load.json'))
print('viewers: %.0f pkt/s, %.2f Mbit/s, loss %.2f%%, connect failures %d' % (r['rx_packets_per_sec'], r['rx_mbps'], r['loss_percent'], r['connect_failures']))
ok = r['connect_failures'] == 0 and r['rx_packets'] > 0 and r['loss_percent'] < 1
print('PASS' if ok else 'FAIL'); raise SystemExit(0 if ok else 1)"
