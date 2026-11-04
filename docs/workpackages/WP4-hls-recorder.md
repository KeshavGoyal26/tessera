# WP4: HLS recorder

**Goal:** `tessera-recorder` producing fMP4/CMAF HLS from WebRTC tracks without transcoding for
H.264 + Opus, with optional AAC and VP8 fallbacks via FFmpeg, uploaded to disk or S3, controlled
by the in-server Coordinator (ADR-0007).

## Owns (exclusive)

`pkg/hls`, `pkg/recorder`, `pkg/storage`, `cmd/tessera-recorder`, `testdata/rtp/**` (new; RTP
fixtures), a new `recorder` job in CI (installs FFmpeg).

## Interfaces

- Provides: `recorder.Service` and `recorder.WorkerAPI` (implemented by `*Coordinator`, consumed
  by WP3's admin routes), `recorder.NewCoordinator(CoordinatorParams)` (frozen; wired in
  `cmd/tessera`), `recorder.JoinGrant`, `hls.Muxer`/`hls.Sink`, `storage.Storage`.
- Consumes: `client.Client` / `client.RemoteTrack` (WP1 M2) through `recorder.RTPSource`,
  `room.Admin` (WP1) to validate rooms/tracks, `webhook.Notifier` (WP3; `webhook.Nop` in
  tests), `auth.AccessToken` for recorder tokens, admin Twirp client (WP3 M1; until then a
  minimal local client in `pkg/recorder`).

## Scope

1. **Coordinator** (in `tessera`): recording state machine (STARTING → ACTIVE → ENDING →
   COMPLETE/FAILED/ABORTED), job queue with capacity matching, long-poll `ClaimJob`, heartbeats
   and failure detection, recorder tokens with `JoinGrant`, `RecordingInfo` store (in-memory now,
   interface ready for Redis), recording webhooks, `Room.active_recording` flag via `room.Admin`
   (request a `room.Admin` extension from WP1 if needed).
2. **Worker** (in `tessera-recorder`): claim loop, per-job pipeline: join hidden via
   `pkg/client`, subscribe targets, per-track RTP → jitter/reorder buffer (small, 200 ms) →
   depacketizer → muxer → sink → storage, keyframe requests on loss, graceful stop, capacity
   accounting, metrics.
3. **H.264**: depacketizer (single NAL, STAP-A, FU-A; drop until IDR after loss), Annex B →
   AVCC, SPS parsing for width/height/profile, `avcC`, codec string `avc1.PPCCLL`.
4. **Opus in fMP4**: `Opus` sample entry + `dOps`, 48 kHz timescale, DTX gaps handled (sample
   durations, no fake silence frames required by players; verify with hls.js).
5. **fMP4 writer**: `ftyp`, `moov` (`mvhd`, `trak`, `mvex/trex`), `moof` (`mfhd`, `traf`,
   `tfhd`, `tfdt`, `trun`), `mdat`; 64-bit `tfdt`.
6. **Segmenter and playlists**: keyframe-aligned segments near the target duration, EVENT →
   VOD playlists, `EXT-X-PROGRAM-DATE-TIME`, discontinuities, master playlist with demuxed
   audio (`EXT-X-MEDIA TYPE=AUDIO`), `CODECS`/`BANDWIDTH`/`RESOLUTION`.
7. **AAC mode**: Opus → AAC-LC via FFmpeg subprocess (Ogg Opus in, ADTS out), then muxed with
   `esds`; **VP8 fallback**: FFmpeg transcode to H.264 (IVF in, Annex B out) then the normal
   H.264 path. Subprocess supervision, back-pressure, CPU capacity accounting.
8. **Storage**: local (atomic write via temp file + rename), S3-compatible via
   `github.com/minio/minio-go/v7` (multipart for large objects, retries), upload ordering
   (segment before playlist), content types.
9. Participant recordings (audio + video, one master playlist); composite later (design
   note only in this WP).

## Milestones

| ID | Deliverable |
|----|-------------|
| M1 | H.264 depacketizer + fMP4 writer + segmenter + playlists from fixture RTP; ffprobe-validated output |
| M2 | Opus in fMP4, master playlist, local and S3 storage (MinIO in tests via docker or skipped) |
| M3 | Coordinator + Worker end to end with fakes, then with WP1 M2 client against a real server |
| M4 | AAC mode and VP8 fallback via FFmpeg, failure handling, capacity, metrics |
| M5 | Participant recordings, hardening (long recordings, SPS changes, clock jumps), docs |

## Acceptance criteria

- Output of a 60 s H.264 + Opus fixture recording: `ffprobe -v error` reports no errors for
  every segment and for the playlist; durations sum to 60 s ± 1 segment; A/V start offset
  < 100 ms via program-date-time.
- Playlists validate against hls.js (headless test that loads the master playlist with hls.js
  in Chromium via Playwright, or `mediastreamvalidator` notes when unavailable) and follow
  RFC 8216 (target duration ≥ every EXTINF rounded).
- No transcoding on the H.264 + Opus path (no FFmpeg process spawned; asserted in tests).
- Recorder CPU: ≥ 100 concurrent track recordings per vCPU in a benchmark with synthetic RTP.

## Required tests

- Depacketizer tables (STAP-A, FU-A start/middle/end, lost fragment, reordering), SPS parser
  vectors, box writer golden bytes, segmenter cut decisions, playlist text goldens.
- Fixture tests: RTP captures in `testdata/rtp/` (generated with a small tool from known H.264
  and Opus files; document how) → HLS → `ffprobe -show_streams -show_frames` assertions
  (skipped with `t.Skip` when ffprobe is missing; CI installs it).
- Coordinator: job lifecycle, heartbeat timeout, stop, capacity, webhooks (fake notifier).
- Storage: local implementation tests; S3 against MinIO when `TESSERA_TEST_S3_ENDPOINT` is set.
