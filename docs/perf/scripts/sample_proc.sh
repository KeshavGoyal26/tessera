#!/bin/bash
# sample_proc.sh PID SECONDS OUTFILE: samples /proc every second.
# Output lines: t_sec cpu_ticks rss_kb rx_bytes tx_bytes rx_pkts tx_pkts (lo interface)
pid=$1; secs=$2; out=$3
: > "$out"
for ((i=0;i<secs;i++)); do
  [ -d /proc/$pid ] || break
  st=($(cut -d' ' -f14,15 /proc/$pid/stat))
  rss=$(awk '/VmRSS/{print $2}' /proc/$pid/status)
  lo=($(awk '/lo:/{sub("lo:"," ");print $1,$2,$9,$10}' /proc/net/dev))  # rxB rxP txB txP
  echo "$i $((st[0]+st[1])) $rss ${lo[0]} ${lo[2]} ${lo[1]} ${lo[3]}" >> "$out"
  sleep 1
done
