#!/usr/bin/env bash
# Build, start and stop Tessera in Docker (deploy/docker-compose*.yml).
#
#   scripts/docker.sh start  [cluster|single]   build if needed, start, wait, print URLs
#   scripts/docker.sh build  [cluster|single]   rebuild the images (drops the old ones)
#   scripts/docker.sh stop   [cluster|single]
#   scripts/docker.sh status [cluster|single]
#   scripts/docker.sh logs   [cluster|single] [service]
#
# Build settings, on the command line or in deploy/.env:
#   NPMRC=~/.npmrc        npm config for the web demo build (registry, auth)
#   NPM_REGISTRY=<url>    npm registry (overrides the one in NPMRC)
#   GOPROXY=<url>         Go module proxy
#
# Default: cluster (Redis, media m1 m2, edges e1 e2, web demo). The ports are those of
# scripts/cluster-start.sh and scripts/start.sh, so run one setup at a time.
set -euo pipefail
cd "$(dirname "$0")/.."

cmd=${1:-}
mode=${2:-cluster}
case $mode in
  cluster) file=deploy/docker-compose.cluster.yml; edge=7930 ;;
  single) file=deploy/docker-compose.yml; edge=7880 ;;
  *) echo "unknown setup '$mode' (cluster or single)"; exit 2 ;;
esac

# deploy/.env: created from the example on first run; TESSERA_NODE_IP (the address browsers
# send media to) follows this machine's LAN IP when the saved one is no longer its own.
lan=$(ipconfig getifaddr en0 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}' || true)
if [ ! -f deploy/.env ]; then
  sed "s/^TESSERA_NODE_IP=.*/TESSERA_NODE_IP=${lan:-127.0.0.1}/" deploy/.env.example > deploy/.env
  echo "created deploy/.env (TESSERA_NODE_IP=${lan:-127.0.0.1})"
fi
ip=$(sed -n 's/^TESSERA_NODE_IP=//p' deploy/.env)
if [ -n "$lan" ] && [ "$ip" != "$lan" ] && [ "$ip" != 127.0.0.1 ] && ! ifconfig 2>/dev/null | grep -q "inet $ip "; then
  sed -i.bak "s/^TESSERA_NODE_IP=.*/TESSERA_NODE_IP=$lan/" deploy/.env && rm -f deploy/.env.bak
  echo "this machine's IP changed: TESSERA_NODE_IP $ip -> $lan (deploy/.env)"
  ip=$lan
fi
secret=$(sed -n 's/^TESSERA_DEV_SECRET=//p' deploy/.env)
compose() { docker compose -f "$file" --env-file deploy/.env "$@"; }

# The build only sees files inside the project: copy the given .npmrc into the git-ignored
# deploy/docker/local/ (and drop a copy from an earlier run when none is given).
use_npmrc() {
  local f=${NPMRC:-$(sed -n 's/^NPMRC=//p' deploy/.env)}
  f=${f/#\~/$HOME}
  if [ -n "$f" ]; then
    [ -f "$f" ] || { echo "NPMRC: no file at $f"; exit 1; }
    cp "$f" deploy/docker/local/.npmrc
  else
    rm -f deploy/docker/local/.npmrc
  fi
}

urls() {
  cat <<TXT

  Tessera ($mode) is running in Docker
  ------------------------------------------------------------
  Web demo     http://localhost:5173/?url=ws://$ip:$edge
TXT
  [ "$mode" = cluster ] && echo "               http://localhost:5173/?url=ws://$ip:7940   (edge e2)"
  cat <<TXT
  Video call   http://localhost:5173/call.html?url=ws://$ip:$edge
  Dashboard    http://localhost:$edge/admin/   login: devkey / $secret
  Logs         scripts/docker.sh logs $mode
  Stop         scripts/docker.sh stop $mode
TXT
}

case $cmd in
  build)
    use_npmrc
    compose build
    docker image prune -f >/dev/null # the images this build replaced
    ;;
  start)
    use_npmrc
    compose up -d --build
    docker image prune -f >/dev/null
    printf "waiting for the servers"
    for _ in $(seq 60); do
      curl -sf -o /dev/null "http://127.0.0.1:$edge/dev/token?room=check&identity=check" && break
      printf .; sleep 1
    done
    echo
    curl -sf -o /dev/null "http://127.0.0.1:$edge/dev/token?room=check&identity=check" \
      || { echo "server on :$edge did not come up; see: scripts/docker.sh logs $mode"; exit 1; }
    urls
    ;;
  stop) compose down ;;
  status) compose ps ;;
  logs) compose logs -f --tail 100 "${@:3}" ;;
  *) sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
