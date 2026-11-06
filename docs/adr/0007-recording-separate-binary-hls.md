# ADR-0007: Recording as a separate binary producing CMAF HLS

> **Amended 2026-11-07:** the separate `tessera-recorder` process is removed. Recording runs
> inside the media server: the worker reads tracks straight from the receive buffers (a
> DownTrack bound to an in-memory queue), so there is no second WebRTC session, encryption or
> loopback hop. Measured: about 2.25x less CPU per recorded presenter. Isolation is kept with a
> per-track panic guard, a non-blocking bounded queue between forwarding and recording, and a
> recording capacity cap. The HLS and job model below are unchanged.

- Status: accepted
- Date: 2026-11-04
- Deciders: Tessera maintainers

## Context

Recording must not slow down the SFU hot path, must scale independently of media nodes, and
should be cheap: remuxing compressed media costs a tiny fraction of transcoding. Kurento
records inside the media pipeline (GStreamer); LiveKit Egress runs Chrome/GStreamer in a
separate service; Janus records raw `.mjr` dumps and post-processes. We want playable HLS
output during and after the session.

## Decision

1. **Separate binary `tessera-recorder`** (one process handles many jobs; deploy N replicas).
   The SFU never writes media to disk.
2. **Control plane (pull model)**: the admin API's `RecordingService.StartRecording` creates a
   job in the server-side **Coordinator** (`pkg/recorder`, running inside `tessera`). Recorder
   workers call `RecorderWorkerService.ClaimJob` (long-poll, Twirp-style HTTP, authenticated
   with a `recorder_worker` grant), receive the job plus a short-lived **hidden recorder
   access token**, and report progress with `UpdateJob`/`Heartbeat`. A job whose worker misses
   heartbeats for 15 s is marked `FAILED` (or re-queued if it never started). Pull works
   behind NAT and needs no service discovery; with Redis (multi-node) the queue moves to Redis
   without changing the worker API.
3. **Media plane**: the worker joins the room as a **hidden subscriber** (`hidden=true`,
   `recorder=true`, `can_publish=false`) using the Go client (`pkg/client`), subscribes to the
   target tracks at the highest layer (recorders are exempt from BWE downgrades unless
   congested) and receives RTP over a normal subscriber PeerConnection.
4. **Output: HLS with fMP4/CMAF segments**, one media playlist per track plus a master
   playlist (`EXT-X-MEDIA TYPE=AUDIO` for demuxed audio). Defaults: 4 s target segment
   duration (cut on keyframes), `EXT-X-PLAYLIST-TYPE:EVENT` while live and `VOD` +
   `EXT-X-ENDLIST` when finished, `EXT-X-INDEPENDENT-SEGMENTS`, `EXT-X-PROGRAM-DATE-TIME`
   for wall-clock alignment across tracks.
5. **No transcoding for H.264**: RTP is depacketized (single NAL, STAP-A, FU-A), access units
   are assembled, converted from Annex B to AVCC (length-prefixed), SPS/PPS go into `avcC` in
   the init segment (re-emitted as a new init segment and `EXT-X-DISCONTINUITY` when they
   change), and fragments (`moof`/`mdat`) are written by a pure-Go ISO-BMFF writer
   (`pkg/hls`). Timestamps come from RTP (90 kHz) unwrapped to 64 bits; gaps/jumps are
   handled with discontinuities. Missing packets after the NACK window cause the GOP up to
   the next keyframe to be dropped (and a PLI to be requested).
6. **Audio**:
   - **Default: Opus in fMP4** (`Opus` sample entry + `dOps` box, 48 kHz timescale, pure Go,
     no transcoding).
   - **Optional AAC mode** (`hls.audio_codec: aac`): Opus is decoded and AAC-LC is encoded by
     an **FFmpeg subprocess** per audio track (Ogg/Opus in on stdin, ADTS AAC out on stdout,
     then muxed by our fMP4 writer). This exists for maximum native-player compatibility.
   - **Browser support facts** (verified November 2026; WP4 must re-check with its test matrix):
     - Playback through **hls.js** (MSE) of fMP4 HLS with H.264 + Opus works in Chrome,
       Edge and Firefox, and in Safari 17+ (macOS MSE; iOS 17.1+ via ManagedMediaSource,
       which hls.js 1.5+ supports).
     - **Native** Safari/AVPlayer HLS: Opus is **not listed** in Apple's HLS Authoring
       Specification codec table. An Apple media engineer stated on the Apple Developer
       Forums that Opus is supported from iOS 17 only in MP4 (fMP4) containers, and Apple
       recommends AAC for compatibility. Treat native Opus playback on Apple devices as
       best-effort; choose AAC mode when recordings must play natively on older iOS/macOS
       or in third-party HLS players.
     - Chrome on desktop added native HLS playback in 2025 (reported for Chrome 142); hls.js
       remains the reference player for our tests.
7. **Video is never transcoded.** VP8 in fMP4 HLS is not broadly playable, and transcoding it
   costs a full encoder per presenter, which caps a node at a few dozen recordings. A VP8 (or
   other non-H.264) video track is refused and the recording fails with an error that asks for
   H.264. Participants who will be recorded get H.264 listed first, so SDKs publish it by
   default (ADR-0006). (A VP8-to-H.264 FFmpeg fallback existed until 2026-11-06.)
8. **Phasing**: per-track recording first (`TrackRecordingTarget`, then
   `ParticipantRecordingTarget` = one participant's audio + video with a shared master
   playlist). A **composite grid** recording (`RoomCompositeTarget`) comes later and will
   necessarily decode/encode (FFmpeg or GStreamer subprocess); it does not affect the
   per-track design.
9. **Storage**: segments are written locally first, then uploaded by a `Storage`
   implementation: local disk (default) or S3-compatible (AWS S3, MinIO, R2, GCS
   interoperability) with multipart upload for large objects. The playlist is uploaded after
   each segment so that live HLS works from the bucket.

## Consequences

- SFU CPU and latency are unaffected by recording; recorder capacity scales separately.
- H.264 recording is cheap (target: 100+ concurrent single-track H.264 recordings per vCPU).
- FFmpeg becomes a runtime dependency only for AAC mode, VP8 sources and (later) composite;
  the recorder image includes it, the core path does not need it.
- Recording quality is limited to what the publisher sends (top simulcast layer).

## Alternatives considered

- **Recording inside the SFU**: violates hot-path isolation.
- **Headless-Chrome composite (LiveKit Egress style) for everything**: very expensive per
  recording; kept only as a possible future composite strategy.
- **MPEG-TS segments**: simpler muxing but cannot carry Opus in HLS for Apple players and is
  less efficient; fMP4/CMAF is the modern choice.
- **WebM recording**: not HLS-playable.
