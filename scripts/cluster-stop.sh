#!/usr/bin/env bash
# Stop the local cluster started with scripts/cluster-start.sh.
set -uo pipefail
cd "$(dirname "$0")/.."
D="$PWD/.cluster"
[ -f "$D/pids" ] || { echo "not running"; exit 0; }
while read -r pid name; do
  pkill -P "$pid" 2>/dev/null; kill "$pid" 2>/dev/null
done < "$D/pids"
redis-cli -p 6390 shutdown nosave >/dev/null 2>&1
rm -f "$D/pids"
echo "stopped"
