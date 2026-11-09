#!/usr/bin/env bash
# Stop the demo started with scripts/start.sh.
set -euo pipefail
cd "$(dirname "$0")/.."
PID=.demo/demo.pid
if [ ! -f "$PID" ] || ! kill -0 "$(cat "$PID")" 2>/dev/null; then
  echo "not running"; rm -f "$PID"; exit 0
fi
kill "$(cat "$PID")"   # demo.sh stops the server and the web demo on exit
for _ in $(seq 1 20); do kill -0 "$(cat "$PID")" 2>/dev/null || break; sleep 0.5; done
rm -f "$PID"
echo "stopped"
