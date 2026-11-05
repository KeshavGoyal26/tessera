#!/bin/bash
# run_compare.sh <tessera|tessera-nocc|livekit> <meeting-a|rooms-b|audio-c> [outdir]
# Starts the server, runs the matching load generator, samples the server process via
# /proc/<pid>/{stat,status} and the loopback counters in /proc/net/dev once per second,
# then reduces the last 25 s before the generator exits (steady state) to one summary line.
# Expects: bin/tessera, bin/tessera-loadtest (go build ./cmd/...), /tmp/lk/bin/{livekit-server,lk-loadtest},
# /tmp/lk/tess.yaml, /tmp/lk/tess-nocc.yaml, /tmp/lk/lk.yaml.
set -u
srv=$1; sc=$2; out=${3:-/tmp/lk/out}; mkdir -p "$out"
here=$(cd "$(dirname "$0")" && pwd); root=$(cd "$here/../../.." && pwd)
SECRET=devsecret-devsecret-devsecret-0000
DUR=${DUR:-40}   # seconds of steady state after ramp-up
tag=$srv-$sc
pkill -f 'bin/tessera --config' 2>/dev/null; pkill livekit-server 2>/dev/null; sleep 1
if [ "$srv" = tessera ] || [ "$srv" = tessera-nocc ]; then
  cfg=/tmp/lk/tess.yaml; [ "$srv" = tessera-nocc ] && cfg=/tmp/lk/tess-nocc.yaml
  "$root/bin/tessera" --config $cfg >"$out/$tag.server.log" 2>&1 & spid=$!
else
  /tmp/lk/bin/livekit-server --config /tmp/lk/lk.yaml >"$out/$tag.server.log" 2>&1 & spid=$!
fi
sleep 3
case $sc in
  meeting-a) # 1 room: 2 publishers (audio+video) and 50 listen-only subscribers
    T="-rooms 1 -publishers 2 -subscribers 50 -publish-only -video-kbps 150,600,2000 -ramp-up 15s"
    L="--room r1 --video-publishers 2 --audio-publishers 2 --subscribers 50" ; ramp=15 ;;
  rooms-b)   # 10 rooms, each 2 publishers + 2 subscribers
    T="-rooms 10 -publishers 2 -subscribers 2 -publish-only -video-kbps 150,600,2000 -ramp-up 15s"
    L="" ; ramp=15 ;;
  audio-c)   # 1 room, audio only: 10 publishers + 40 subscribers
    T="-rooms 1 -publishers 10 -subscribers 40 -audio-only -publish-only -ramp-up 15s"
    L="--room r1 --audio-publishers 10 --subscribers 40 --layout 5x5" ; ramp=15 ;;
esac
total=$((ramp+DUR+10))
"$here/sample_proc.sh" $spid $total "$out/$tag.proc" & sp=$!
t0=$(date +%s)
if [ "$srv" != livekit ]; then
  "$root/bin/tessera-loadtest" $T -duration ${DUR}s -loopback -api-key devkey -secret $SECRET -json "$out/$tag.gen.json" >"$out/$tag.gen.log" 2>&1
else
  if [ "$sc" = rooms-b ]; then
    pids=""
    for r in 1 2 3 4 5 6 7 8 9 10; do
      /tmp/lk/bin/lk-loadtest --url ws://127.0.0.1:7880 --api-key devkey --api-secret $SECRET --room r$r --video-publishers 2 --audio-publishers 2 --subscribers 2 --duration ${DUR}s --num-per-second 10 >"$out/$tag.gen.$r.log" 2>&1 &
      pids="$pids $!"
    done
    wait $pids
  else
    /tmp/lk/bin/lk-loadtest --url ws://127.0.0.1:7880 --api-key devkey --api-secret $SECRET $L --duration ${DUR}s --num-per-second 10 >"$out/$tag.gen.log" 2>&1
  fi
fi
t1=$(date +%s); echo "$tag generator took $((t1-t0))s"
# summary window: [t1-t0-30, t1-t0-5]
a=$((t1-t0-30)); b=$((t1-t0-5))
awk -v a=$a -v b=$b -v tag=$tag '$1>=a && $1<=b { if(!n){f=$0; split($0,F," ")} n++; split($0,L," ");
  if($3>rss)rss=$3 } END { dt=L[1]-F[1]; if(dt<1){print tag,"no samples"; exit}
  printf "%s cpu%%=%.1f rss_mb=%.0f lo_pps=%.0f lo_mbit=%.1f\n", tag, (L[2]-F[2])/dt, rss/1024, (L[7]-F[7])/dt, (L[5]-F[5])*8/dt/1e6 }' "$out/$tag.proc" | tee "$out/$tag.summary"
kill $sp 2>/dev/null; kill $spid 2>/dev/null; sleep 1; pkill livekit-server 2>/dev/null; true
