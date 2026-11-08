# Cluster test plan on GCP (ADR-0012 phase 4)

Status: **not run yet**. Every command below creates or deletes paid resources; run them
only after review. Same rules as the single-node soak: project
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
