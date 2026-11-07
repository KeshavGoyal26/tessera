# Recording capacity: plan and homework

Goal: a measured answer to "how many recorded presenters fit on one c8i.xlarge (4 vCPU, 8 GiB)", for
Tessera and for the alternatives, on the same workload. Today's production baseline is Kurento with
**300 candidates** per c8i.xlarge (VP8 + Opus in, WebM recording).

## What we know

### How each server records

| Server | Recording path | Video re-encoded? | Output |
|---|---|---|---|
| Tessera (H.264 publishers) | recorder joins as a participant, repackages to HLS | no | HLS (fMP4), playable live |
| Tessera (VP8 publishers) | same, FFmpeg VP8 to H.264 per track | yes | HLS |
| Kurento (current prod) | RecorderEndpoint inside the media server | no, when VP8 goes to WebM | WebM |
| LiveKit track egress | egress service joins as a participant | no | WebM/OGG (VP8), MP4 (H.264) |
| LiveKit track composite | egress, GStreamer pipeline | yes | MP4 / HLS |
| OpenVidu 3 | LiveKit egress | as LiveKit | as LiveKit |
| OpenVidu 2 INDIVIDUAL | Kurento | no | WebM |
| SRS | WebRTC to internal RTMP (`rtc_to_rtmp`, experimental), then HLS/DVR | no, **H.264 only**; Opus to AAC | HLS, FLV, MP4 |
| Janus | raw RTP to `.mjr`, converted later | no (later, offline) | `.mjr` |

### Published numbers: weak, so they need checking

- **LiveKit:**
  - The docs recommend at least 4 CPU and 4 GB per egress instance.
  - Track egress is cheap; the docs say "hundreds" fit on one instance.
  - The scheduler reserves 1 CPU per track egress, 2 per track composite and 3 per room composite. These are weights, not measurements.
  - SFU benchmark: 150 publishers and 150 subscribers on 16 cores at 85% CPU.
- **OpenVidu 2:** forum report of about 60 sessions × 2 participants with INDIVIDUAL recording on 4 CPU. Composed recording needs at least 1 CPU per session.
- **SRS:**
  - No WebRTC benchmark published; the PERFORMANCE.md WebRTC table is empty.
  - Mostly one core per process, as far as we know.
  - No simulcast that we know of.
- **Kurento:** no official numbers. Ours is 300 per c8i.xlarge in production.

### Tessera so far (2026-11-06)

Linux VM, 4 vCPU on Apple M1. Each presenter sends 720p30 H.264 at 1.5 Mbit/s plus Opus, and is recorded to HLS.

- **10 presenters:**
  - 4.5% of a vCPU per presenter (server 2.1%, recorder 2.4%);
  - 37 MB of recorder memory each;
  - every recording correct.
- **These figures are inflated.** A bug, fixed in 42a38e3, made every recording subscribe to every presenter (N² traffic), so these runs carried far more traffic than real use.
- **After the fix:** not yet measured cleanly.

### Tessera, measured after the fixes (2026-11-06, commits 42a38e3 and f431665)

Linux VM, 4 vCPU on Apple M1 (arm64). The server, recorder and load generator all run in the VM. Every presenter publishes real H.264 plus Opus and is recorded to HLS, with no FFmpeg.

| Presenters | Stream | Server + recorder CPU | Per presenter | Memory (server + recorder) | Recordings valid |
|---|---|---|---|---|---|
| 30 | 720p30, 1.5 Mbit/s | 46% of a vCPU | 1.53% | 282 MB | 30/30 |
| 60 | 720p30, 1.5 Mbit/s | 83% | 1.39% | 524 MB | 60/60 |
| 100 | 720p30, 1.5 Mbit/s | 144% | 1.44% | 903 MB | 100/100 |
| 150 | 360p30, 0.4 Mbit/s | 181% | 1.21% | 1.1 GB | 150/150 (VM 97% busy) |
| 300 | 360p30, 0.4 Mbit/s | not valid | | | VM saturated by the load generator, 258/300 started |

- **Cost is linear and mostly fixed per presenter.** 0.4 Mbit/s costs nearly as much as 1.5 Mbit/s.
- **Kernel networking is not in these figures.** It comes to about 1% per presenter in the VM, partly from the load generator.
- **Where the time goes** (CPU profile at 100 presenters):
  - system calls for packet I/O: about 20% in each process;
  - memory copies and GC;
  - Pion's per-packet interceptors in the recorder. Dropping the congestion-feedback and stats interceptors would save only about 2%.
- **The structural cost is the recorder being a separate WebRTC participant.** Every recorded packet is encrypted again, sent over loopback, decrypted and parsed. The recorder process is about half of the total.

**Rough c8i.xlarge estimate:** about 2-3% of a vCPU per recorded presenter including the kernel. At 75% CPU that is roughly **100-150 recorded presenters** with the separate recorder. To pass 300, record inside the SFU process: it writes HLS straight from the receive buffer, removing the second hop and about half the cost. ADR-0007 chose a separate process for isolation; revisit that trade-off.

### In-process recording, measured (2026-11-07)

Linux VM on an M1 with 8 vCPUs. The server is pinned to 4 cores (`taskset -c 0-3`) and the load generator to the other 4. Every presenter sends 360p at 400 kbit/s and is recorded.

| Presenters | Server process | Cores 0-3 busy (incl. kernel) | Recordings |
|---|---|---|---|
| 150 | 139% | 180% | all full length (82-100 s) |
| 200 | 207-288% | 259-343% | all present, slightly short (median 83 s) |
| 250 | 322-327% | 381-388% (saturated) | truncated (median 50-62 s) |
| 300 | 325% | 387% (saturated) | truncated (median 33 s) |

- **Clean capacity on this test bed: about 150-200 recorded presenters per 4 vCPUs.**
- **Runs are noisy.** At 200 the server used anywhere from 207% to 288% across runs. The M1 mixes performance and efficiency cores, so the server's 4 vCPUs are not uniform.
- **What it means for c8i.xlarge:** 4 identical, hyper-threaded vCPUs. Only an EC2 run gives the real number.
- **Where the time goes at 200:** copying (`buffer.Write`, fMP4 fragments), per-packet allocations on the recording path (now reduced) and system calls.
- **To reach 300 on 4 vCPUs,** the next levers are, in order:
  1. single-layer publishing for recorded-only presenters (no simulcast), which is about a third fewer packets;
  2. fewer per-packet copies (the receive ring, pion packetio);
  3. measuring on EC2 before optimising further.

### GCP, separate load generator (2026-11-07, after 9e08135)

Server GCP n2-standard-4 (4 vCPU, asia-south1); load generator n2-standard-8 on the same VPC.
Every presenter publishes real H.264 360p at 400 kbit/s plus Opus and is recorded; each step
runs 240 s and every recording is checked (finalized, length).

| Recorded presenters | Server CPU (% of one vCPU) | Share of the box | RSS | Recordings |
|---|---|---|---|---|
| 250 | 159% | 40% | 1.1 GB | 500/500 full length |
| **300** | **189%** | **47%** | 1.35 GB | 600/600 full length |
| 350 | 215% | 54% | 1.6 GB | 700/700 full length |
| 400 | 241% | 60% | 1.9 GB | 800/800 full length |
| 450 | 265% | 66% | 2.1 GB | 900/900 full length |
| 500 (+50 live) | 302% | 76% | 2.5 GB | full length; 50 more queued (capacity 1000 units) |
| live only 500 / 700 | 219% / 278% | 55% / 70% | 1.8 / 2.6 GB | n/a |

About 0.6% of a vCPU per recorded presenter and 0.4% per live-only one, linear. Kurento's 300 per
c8i.xlarge uses under half of this box; ~500 recorded presenters fit with 25% headroom.

A 2 h soak at 200 recorded presenters with churn every 15 min (3,200 playlists) was clean: no lost
or short recordings, goroutines/FDs back to baseline, heap 9.9 MB after the load.

Real Chrome presenter and viewer on a shaped network (with the 300 above in the background)
found the recorder stuck on a paused top simulcast layer (37% of video recorded); fixed in
9e08135 (93-96%, ~2 s per drop of the top layer).

The default `recording.capacity` was 100 units (50 presenters): recordings beyond it wait in the
queue and miss their start. It is now 150 units per CPU.

### Long soak and network scenarios (2026-11-07, build with 9e08135 and 6f6c637)

**5 h 5 min soak** (stopped early because every curve was flat): 300 recorded presenters (6 groups of 50,
each rejoining every 15 min) plus a real Chrome presenter and viewer looping the network scenarios
below.

| Metric | Result |
|---|---|
| Recordings | 12,344 playlists, all finalized, 0 missing segments, 0 under 90% coverage, 99.99% covered |
| Server CPU | 197-208% of one vCPU (about half the box), flat |
| RSS | 1.46-1.50 GB for the whole run, no growth |
| Goroutines / FDs | 18,408 / 316 under load; 21 / 12 after it (baseline) |
| RSS / heap after the load | 115 MB / 16 MB |
| Panics, ERROR log lines | 0 / 0 |

**Network scenarios**, real Chrome presenter and viewer in Linux network namespaces shaped with
`tc netem`, 10 rounds under the load above (medians; freezes are seconds frozen per 45 s phase):

| Condition | Viewer sees |
|---|---|
| Presenter uplink 1 Mbit/s, 500 kbit/s | 360p, 18-20 fps, 1-2 s freezes |
| Presenter uplink 250 / 150 kbit/s | 180p, 14 / 13 fps, 3 / 7.5 s freezes |
| Viewer downlink 1 Mbit/s / 500 kbit/s | 720p / 360p, smooth |
| Viewer downlink 250 / 150 kbit/s | 8 / 2 fps, frequent stalls (accepted: below the supported range) |
| Viewer 5% loss, or 150 ms added delay either way | 720p, smooth |
| **Presenter 5% loss / 2% loss + 75 ms** | **360p, 11 / 13 s freezes (open)** |
| Blackout 3-30 s | video back in ~2 s (worst 8.6 s) |
| Blackout 60 s | video back in 6-7 s (worst 11 s); the SDK rejoins as a new session |

Supported range: medium and fast networks. Slow-link tuning (viewers under 250 kbit/s, presenter
uplinks under 250 kbit/s) is out of scope.

Failure drills: SIGTERM finalizes all recordings in 0.2 s; a full disk fails the recordings cleanly
and live media is unaffected; `kill -9` leaves recordings open, closed at the next start since
a87af1b.

## Questions for us (answer before testing)

1. **Kurento candidate stream:** what resolution, frame rate and bitrate? Is simulcast on? Does anyone watch live?
2. **Kurento CPU and memory at 300 candidates:** how much headroom is left?
3. **Output format:** is WebM acceptable, or must recordings be HLS or MP4?
4. **Deployment:** must the SFU and recorder share one box, or can recording run on separate instances?

## Test plan

### Workloads (same for every server)

| ID | Publisher | Recording | Mirrors |
|---|---|---|---|
| W1 | 640x360 at 15 fps, ~400 kbit/s, Opus, no simulcast | per participant | Kurento prod (adjust to answer 1) |
| W2 | 720p30 at 1.5 Mbit/s, Opus, no simulcast | per participant | a heavier camera |

Codec variants: **H.264** (Tessera HLS, SRS, LiveKit MP4) and **VP8** (Tessera HLS by re-encoding, LiveKit WebM, Kurento WebM).

### Method

1. **Server box:** a c8i.xlarge running the server and its recorder or egress. Run it on a separate box as well if answer 4 allows.
2. **Load generator:** a separate box, for example a c8i.2xlarge, so it doesn't compete for CPU. Use real encoded clips looped, as `h264load` already does, not synthetic packets.
3. **Ramp:** 25, 50, 100, 150, 200, 300 presenters, holding each step for 3 minutes.
4. **Record at every step:**
   - CPU per process and for the whole box (`/proc`), memory, and network in and out;
   - recorder health: active recordings, segment upload delay, dropped samples;
   - every recording at the end: playlist finalized, duration within 2% of the publish time, `ffprobe` decodes with no errors.
5. **Capacity:** the largest step where box CPU stays at or below 75%, memory at or below 80%, and every recording is valid.

### Per server

- **Tessera:**
  - Promote `h264load` (now in the scratchpad) to `tessera-loadtest --codec h264|vp8 --record`.
  - Run W1 and W2 with H.264 (HLS repackaging) and with VP8 (HLS re-encoding).
- **LiveKit / OpenVidu 3:** server plus egress, track egress (WebM, or MP4 for H.264) and track composite (HLS). Use `lk load-test`, or the same Pion generator with LiveKit tokens.
- **SRS:** WHIP publish with H.264 and Opus, and `rtc_to_rtmp` plus HLS on. The generator needs a small WHIP mode.
- **Kurento:** reuse the production numbers if answers 1 and 2 are known; otherwise one W1 run.

### Likely improvements to try if Tessera is short of 300 (in order)

1. **VP8 passthrough to WebM:** no FFmpeg; the same path as Kurento and LiveKit track egress.
2. **Single stream for recorded-only publishers:** no simulcast, about 30-50% fewer packets.
3. **FFmpeg `-threads 1`** for the VP8-to-HLS path: about 1.4x per measurements.
4. **Recording inside the SFU process:** removes the second WebRTC hop. A bigger change; only if the measurements call for it.

## Next steps

1. **After the usage reset:** a clean Tessera run on the local Linux VM (30, 60, 100) to confirm the fix scales linearly. A few calls.
2. **Answer the four questions above.**
3. **Add `--codec h264 --record` to `tessera-loadtest`.**
4. **EC2 runs:** c8i.xlarge server plus a load-generator box. Run Tessera first, then LiveKit and SRS. Needs AWS access from you.
5. **Write up a results table here, with the same columns for every server.**

Sources:
- [LiveKit self-hosting: Egress](https://docs.livekit.io/transport/self-hosting/egress/)
- [LiveKit benchmarks](https://docs.livekit.io/transport/self-hosting/benchmark/)
- [LiveKit Egress CPU weights](https://pkg.go.dev/github.com/livekit/egress@v1.8.4)
- [OpenVidu 3 recording](https://openvidu.io/3.8/docs/troubleshooting/recording/)
- [OpenVidu 2 recording](https://docs.openvidu.io/en/stable/advanced-features/recording)
- [OpenVidu forum: 4-CPU capacity](https://openvidu.discourse.group/t/simultaneous-conferences/329)
- [SRS performance](https://github.com/ossrs/srs/blob/develop/trunk/doc/PERFORMANCE.md)
- [SRS WebRTC](https://ossrs.io/lts/en-us/docs/v6/doc/webrtc)
- [Kurento RecorderEndpoint](https://doc-kurento.readthedocs.io/en/stable/_static/client-jsdoc/module-elements.RecorderEndpoint.html)
