# Cluster test plan on GCP (ADR-0012 phase 4)

Status: **run on 2026-11-08** (results below). Every command below creates or deletes paid
resources; run them only after review. Same rules as the single-node soak: project
`project-bade21b6-8437-4697-93e`, zone `asia-south1-a`, `--project`/`--zone` on every
command (never `gcloud config set`), names `soak-*`, network tag `soak`, label
`purpose=soak`. The Watch Party VM, its IP and its firewall rules are not touched.

## Setup

| VM | Type | Role | Count |
|---|---|---|---|
| `soak-redis` | e2-small | Redis 7 (stand-in for ElastiCache) | 1 |
| `soak-media-1..3` | n2-standard-4 | media nodes (recording to local disk, journal on disk) | 3 |
| `soak-edge-1..2` | n2-standard-4 | edges (TURN, viewers, relays) | 2 |
| `soak-loadgen` | n2-standard-8 | load tester and Chrome network scenarios | 1 |

All traffic stays on the default VPC's internal addresses (the existing
`default-allow-internal` rule), so **no firewall rule and no static IP are created**.
Clients use `ws://<edge internal IP>:7880`; the load tester spreads them over both edges.

**Cost** at about ₹21/h per n2-standard-4, ₹41/h for the n2-standard-8 and ₹2/h for the
e2-small: about **₹150/hour**, 12-16 hours in total, **about ₹1,800-2,400**.

```bash
P=project-bade21b6-8437-4697-93e; Z=asia-south1-a
for n in soak-redis:e2-small soak-media-1:n2-standard-4 soak-media-2:n2-standard-4 soak-media-3:n2-standard-4 \
         soak-edge-1:n2-standard-4 soak-edge-2:n2-standard-4 soak-loadgen:n2-standard-8; do
  gcloud compute instances create ${n%%:*} --project=$P --zone=$Z --machine-type=${n##*:} \
    --image-family=debian-12 --image-project=debian-cloud --boot-disk-size=50GB \
    --tags=soak --labels=purpose=soak
done
```

Nodes run the binary built from `main` with configs like `scripts/cluster-smoke.sh` writes
(`cluster.role`, `cluster.redis.addr: <soak-redis IP>:6379`, `private_url` on media nodes,
`rtc.ips: [<own internal IP>]`, `room.lock_published_tracks: true`, recording on media nodes).

## Tests (in order; each must pass before the next)

1. **Smoke** (10 min): `scripts/cluster-smoke.sh` logic against the VMs: 2 groups x 3
   presenters, 2 viewers each; viewers receive every presenter, 0 connection failures.
2. **Media node capacity** (1.5 h): presenters only (publish-only, auto-recorded, one 360p
   H.264 stream at 450 kbit/s) on one media node, 300 to 700 in steps of 100 for 5 minutes;
   CPU, memory, every recording full length. Expected: more than the 450 per box measured
   with simulcast.
3. **Edge capacity** (1.5 h): 200 presenters on the media nodes; viewers on one edge, each
   watching 10 presenters of its group (`--subscribers`, groups of 10), 500 to 2,000 viewers.
   CPU and egress of the edge; relay traffic from the media nodes.
4. **Soak** (4 h): 3 x 200 recorded presenters (groups of 50, rejoining every 15 min) and
   2 x 500 viewers; memory and goroutines flat, every recording finalized, no panics.
5. **Drills** (1 h), during load:
   - `kill -9` a media node: its presenters rejoin on the others, edge viewers get them back,
     its recordings close at its restart (disk journal).
   - stop an edge: its viewers rejoin through the other edge; presenters proxied through it
     resume (same media node) within the 15 s resume window.
   - restart Redis: sessions in progress continue; new joins fail until it is back.
6. **Network** (1 h): the Chrome scenarios of the single-node soak, presenter and viewer
   through an edge.

## Clean-up

```bash
gcloud compute instances delete soak-redis soak-media-1 soak-media-2 soak-media-3 soak-edge-1 soak-edge-2 soak-loadgen \
  --project=project-bade21b6-8437-4697-93e --zone=asia-south1-a --quiet
gcloud compute instances list --project=project-bade21b6-8437-4697-93e --filter="labels.purpose=soak"   # must be empty
```

## Results (2026-11-08)

Run on 6 VMs, not the 7 above: the free-trial quota is 32 vCPUs, so Redis ran on
`soak-loadgen`. Media nodes n2-standard-4 (recording to local pd-balanced), edges
n2-standard-4, load generator n2-standard-8. Presenters publish real H.264 + Opus
(auto-recorded) through an edge; viewers are `tessera-loadtest` subscribers watching every
presenter of their group. Total GCP cost about Rs 1,000.

**Bugs found and fixed**

- Presenters of one group joining together were split across media nodes (the group's
  hosting set and the node's session count only changed at the next heartbeat). Fixed in
  2e6c393: a placement is written to Redis at once.
- The periodic crash-journal recovery (every 30 s) took this process's own recordings for
  orphans (the pid check treats "same pid" as a previous run): every recording was sealed,
  reported FAILED and lost its crash protection about 30 s after it started. Fixed in
  348b4f0; with the fix no live recording was touched and a killed node's recordings were
  all closed at its restart.

**Smoke**: 4 groups x 3 presenters, 8 viewers over both edges: 0 connect failures, 0.03%
loss, every recording finalized at full length, each group on one media node.

**Media node capacity** (presenters only, all recorded, one media node, steps of 4 min):

| Presenters | tessera CPU (of 400%) | RSS | Recordings |
|---|---|---|---|
| 300 | 166% | 1.3 GB | all finalized, full length |
| 400 | 211% | 1.8 GB | all finalized, full length |
| 500 | 258% | 2.2 GB | all finalized, full length |
| 600 | 288% | 2.6 GB | all finalized\* |
| 700 | 312% | 2.9 GB | all finalized\* |

\* `recording.capacity` was 1000 units (500 presenters with camera + mic); presenters over
it waited for a free slot, so their recordings are short. CPU grows linearly: about 800
recorded presenters per n2-standard-4 at the limit; plan for about 500-600 with headroom
(the default capacity, 150 units per vCPU, allows 300).

**Edge capacity** (200 presenters on the media nodes, viewers on one edge, each watching
the 10 presenters of its group):

| Viewers | Video streams out | edge CPU (of 400%) | Viewer loss |
|---|---|---|---|
| 100 | 1,000 | 170% | 0.01% |
| 200 | 2,000 | 262% | 0.04% |
| 400 | 4,000 | 375% (saturated) | 11.7% |

The load generator was near its own limit from 200 viewers on, so loss above that is not
the edge's alone. About 300 viewers x 10 presenters per n2-standard-4 edge. A CPU profile
at 240 viewers: 31% in `sendto`/`sendmmsg`, 9% SRTP encryption, 2% in Tessera's
forwarding; relay intake (one Pion PeerConnection per group and media node, with its
interceptors) is the next largest cost when every presenter is watched. After an overload
to 800 viewers the edge recovered fully (goroutines back to 18, live heap 27 MB).

**Soak** (3 h): 600 recorded presenters (60 groups of 10, each group rejoining every
15 min, staggered, alternating edges) and 120 viewers (2 per group, both edges, new
viewers every 15 min).

- Media nodes: about 190-200% CPU each with 200 presenters; goroutines flat (about
  12,430); no errors. 6,585 recordings (13,179 tracks) checked and removed as they
  finished: all finalized, 5 shorter than 850 s.
- Edges: about 280% CPU each (each relays nearly all 600 presenters); goroutines flat
  (9,950 -> 9,820).
- Viewers: 0 connect failures, 0.1-0.55% loss per 15-min cycle (the load generator was at
  about 98% CPU).
- RSS rose about 14% from the second to the last quarter on media nodes and edges; see the
  memory check below.

**Drills** (200 presenters in 20 groups, 40 viewers):

- `kill -9` of a media node (70 presenters): every presenter rejoined on the other media
  nodes, most within about 1 s of the session loss; viewers had those presenters back
  within 15-20 s. At its restart the node closed all 70 recordings from its journal (140
  tracks finalized).
- `kill -9` of an edge: its 100 presenters rejoined through the other edge within 60 s;
  viewers on the other edge saw no dip.
- Redis restart: sessions in progress unaffected (no dip for viewers), every node back in
  the registry within 30 s, new joins work.

**Memory check** (90 min of the soak load, forced-GC heap profiles of media-1 and edge-1):

| | start | 20 min | 50 min | 80 min | 3 min after the load |
|---|---|---|---|---|---|
| media-1 heap | 4 MB | 484 MB | 514 MB | 544 MB | 15 MB |
| edge-1 heap | 43 MB | 768 MB | 803 MB | 840 MB | 23 MB |

Goroutines stayed flat (12,444 and 10,180) and went back to idle (24 and 101), so nothing is
left behind once sessions close. Diffing the 20 and 80 min profiles shows the growth comes from
Pion's `packetio` buffers of SRTCP read streams. Pion (v4.2.26) never reads the RTCP stream of a
receiver's RTX SSRC, so the sender reports for it collect in that stream's buffer (100 KB cap,
about 70 B/s) until the receiver closes. That is at most about 125 KB per video receiver,
reached after about 25 min. The 3 h soak shows the same plateau: media-node RSS grew by 71, 34,
28, 26 and 23 MB per 15 min. Not changed: the only way around it is a custom
`SettingEngine.BufferFactory`, which also changes when Pion starts the RTX reader, a riskier
change than a bounded few tens of MB.

**Not run**: the Chrome network scenarios (the media path is the single-node one, already
tested) and the S3 + Redis journal (recordings went to local disk).
