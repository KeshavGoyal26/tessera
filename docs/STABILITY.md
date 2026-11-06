# Stability under bad networks

How Tessera is tested on lossy, slow and capped links, what that found, and what is still open.

## Harness

`test/netem` is a TURN relay that impairs the link between a browser and the media server:
loss (random or bursty), delay, jitter (order kept) and a bandwidth cap with a queue. Browsers are
forced through it (`iceTransportPolicy: "relay"`, only this server in `iceServers`), so the same
network applies to any media server, and to a direct browser-to-browser call as a baseline.

```bash
go run ./test/netem -listen 127.0.0.1:3478 -ctl 127.0.0.1:3479            # presenter link
go run ./test/netem -listen 127.0.0.1:3488 -ctl 127.0.0.1:3489            # viewer link
curl '127.0.0.1:3489/set?loss=5&delay=150ms&jitter=30ms&kbps=800'        # change a link live
curl '127.0.0.1:3489/stats'   # passed / lost / queue drops, and how late the relay itself delivered
```

TURN credentials are `netem` / `netem`. Measure on the viewer with `getStats()`: frames decoded per
second, `freezeCount`, `totalFreezesDuration`, audio `concealedSamples`.

## Results

Chrome presenter -> Tessera -> Chrome viewer, 30 s runs, both links impaired unless noted.

| Link | Before | After |
|---|---|---|
| Clean | random freezes of up to 3.5 s | none |
| 5% loss | 162 kbit/s, 6 fps | ~750 kbit/s, 17 fps, 720p |
| 10% loss, viewer only | 59 kbit/s, 2 fps | ~745 kbit/s, 16 fps, 720p |
| 800 kbit/s cap | 12 layer switches | 4 |
| 5% loss, audio concealed | ~5.5% | 1.7% (RED) |
| 300 ms RTT + 30 ms jitter + 2% loss | 110-540 kbit/s | 420-740 kbit/s |

Soak (15 min, one node): 2 Chrome presenters (one recorded) and 2 Chrome viewers watching both,
plus `tessera-loadtest` with 3 rooms x (3 publishers + 3 subscribers), 23 participants, 26 Mbit/s
through the server. Server goroutines (956-958) and open files (43) stayed flat, heap cycled between
48 and 91 MB with no upward trend, CPU ~25% of a core; after everyone left, goroutines and files went
back to their starting values and the heap to 6 MB. Viewers decoded 20 fps for the whole run with one
0.3 s freeze each; the load test saw 0% loss and p99 latency 1.4 ms throughout. The 15-min
recording is 720p for all 18,264 frames, audio and video both 913 s, with steady 50 ms frame
spacing. The recorder stayed at 74 goroutines and under 10 MB of heap.

Also checked: 30 rounds of 12 participants joining and leaving leave no goroutine, file or heap
growth; after a server restart, clients rejoin and video plays again ~7 s after the kill; fuzzing of
VP8 rewriting, the forwarder, RED parsing and TWCC feedback finds nothing.

## What was fixed

- Forwarder: per-range sequence-number offsets (late packets kept their slot); temporal layer raised
  only at a layer-sync frame; no layer switch on a keyframe whose first packet arrived late.
- Congestion control: loss judged against the link's usual level, so random loss is not congestion;
  GCC-style overuse persistence; probes count only when they prove the upgrade.
- RTT: measured from ICE and used for NACK pacing (retry once per RTT, give up after 4 RTT);
  sender reports carry the server clock, so receiver-report RTT is right.
- Recording: video is never transcoded. Recorded participants are asked for H.264 first; a VP8
  track is refused with an error. Each recording subscribes only to its own participant's tracks
  (before, every recording received every participant: N^2 traffic, 1 GB of buffers at 30).
- Shutdown: dashboard event streams are closed first (an open dashboard tab held shutdown for 30 s),
  a second Ctrl-C exits at once, and shutdown is capped at 60 s.
- Audio: Opus RED. Browsers that support it publish RED; RED subscribers get it unchanged, others
  (Firefox, the recorder, Go/Java clients) get the primary Opus frame. Receiver reports to the
  publisher carry the worst subscriber loss, so Opus FEC follows the listeners.

## Deployment notes

- Advertise **one address** per server (`rtc.node_ip`, or one entry in `rtc.ips`). With two
  equivalent host candidates (for example LAN and loopback) a browser may switch paths mid-call, and
  media sent on the old path is lost for about a second.

## Open

- 300 ms RTT: video runs at 8-12 fps with several seconds of freezes per 30 s. A direct
  browser-to-browser call reaches ~20 fps there, but it also uses video FEC, which LiveKit does not;
  an A/B against LiveKit on the same relays would tell whether this is a gap or the cost of NACK-only
  recovery at that delay.
- Browsers can still switch between their own candidate pairs once per call on relayed paths,
  which costs about a second of media that is then retransmitted.
