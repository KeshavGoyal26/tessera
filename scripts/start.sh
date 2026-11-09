#!/usr/bin/env bash
# Start the Tessera demo (server + web demo) in the background. Stop it with scripts/stop.sh.
#   scripts/start.sh [--loadtest] [--host IP]     (options are passed to scripts/demo.sh)
set -euo pipefail
cd "$(dirname "$0")/.."
PID=.demo/demo.pid
if [ -f "$PID" ] && kill -0 "$(cat "$PID")" 2>/dev/null; then
  echo "already running (pid $(cat "$PID")); stop it with scripts/stop.sh"; exit 1
fi
mkdir -p .demo/logs
nohup scripts/demo.sh "$@" > .demo/logs/demo.log 2>&1 < /dev/null &
echo $! > "$PID"
for _ in $(seq 1 90); do
  grep -q "demo is running" .demo/logs/demo.log 2>/dev/null && break
  kill -0 "$(cat "$PID")" 2>/dev/null || { echo "failed to start:"; tail -20 .demo/logs/demo.log; rm -f "$PID"; exit 1; }
  sleep 1
done
sed -n '/demo is running/,/Press Ctrl-C/p' .demo/logs/demo.log | sed 's/ Press Ctrl-C to stop.//'
echo "  Stop with: scripts/stop.sh"
