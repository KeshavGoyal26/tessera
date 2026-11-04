# Recording (HLS)

Implements ADR-0007. `tessera-recorder` joins a room as a hidden subscriber, remuxes the
RTP it receives into fMP4/CMAF HLS and writes it to local disk or an S3-compatible bucket.

## Flow

1. `RecordingService.StartRecording` (admin API) creates a job in the in-server **Coordinator**
   (`pkg/recorder`, status `STARTING`).
2. A worker long-polls `RecorderWorkerService.ClaimJob`, gets the job plus a hidden recorder
   access token (`pkg/recorder.JoinGrant`) and joins with `pkg/client`.
3. For every subscribed track that matches the target (`TrackRecordingTarget` or
   `ParticipantRecordingTarget`) a per-track pipeline runs:
   `RTP → reorder buffer (200 ms) → depacketizer → muxer (fMP4 + playlist) → ordered uploader`.
4. The worker reports `ACTIVE` when the first sample is muxed, heartbeats every 5 s (the
   Coordinator fails jobs after `recording.heartbeat_timeout`, 15 s, or re-queues them if they
   never started) and receives stop requests in the heartbeat response.
5. On stop the muxers flush, uploads drain, a `master.m3u8` is written (participant recordings
   and whenever more than one track was recorded) and the job ends `COMPLETE` (`FAILED` on
   upload/track errors, `ABORTED` when stopped before any media).
6. The Coordinator emits `recording_started` (first `ACTIVE`) and `recording_ended` webhooks
   whose payload is the full `RecordingInfo` including `outputs` (playlist location/URL,
   segment count, duration, size).

## Output layout

```
<prefix>/                      prefix = filename_prefix, default "{room}/{recording_id}"
  master.m3u8                  EXT-X-MEDIA TYPE=AUDIO (demuxed audio) + EXT-X-STREAM-INF
  <track_sid>/index.m3u8       media playlist (EVENT while live, VOD + ENDLIST at the end)
  <track_sid>/init.mp4         init segment (init_N.mp4 after a codec change)
  <track_sid>/seg_00000.m4s    CMAF fragment (moof+mdat), cut on keyframes
```

Templates: `{room}`, `{recording_id}`, `{time}` (UTC `20060102T150405Z`). Segment target
defaults to 4 s and is clamped to 2 to 6 s; video segments are cut on the first keyframe at or
after the target. Segments are always uploaded before the playlist that lists them.

## Timing and robustness

- Timestamps come from RTP, unwrapped to 64 bits (`hls.Unwrapper`), so 32-bit wrap is handled.
- A/V alignment: all tracks of a recording share an origin; each track's first sample is
  placed at `(first wall time − origin)` on the output timeline and `EXT-X-PROGRAM-DATE-TIME`
  carries the wall time. Wall time of a sample is derived from RTCP SR when the track source
  implements `recorder.SRSource`, otherwise from packet arrival time of the first packet
  (accurate to network jitter, not to lip-sync precision). The current `pkg/client` track does
  not expose SRs yet; wiring it is a small follow-up.
- Reordering: `hls.ReorderBuffer` (200 ms / 128 packets). Packets given up on are counted
  (`tessera_recorder_rtp_packets_lost`).
- Loss (H.264/VP8): the damaged frame and everything until the next IDR is dropped and a
  keyframe is requested (at most once per second). The timeline stays continuous: the last good
  frame is held for the gap, so A/V sync is preserved.
- Opus loss/DTX: gaps simply lengthen the previous sample duration (no fake silence frames).
- SSRC change: per-stream state is reset and the output timeline continues from the last sample
  plus the elapsed wall time. A forward DTS jump larger than 2x the target duration or a clock
  that moves backwards starts a new segment marked `EXT-X-DISCONTINUITY`.
- SPS/PPS change (resolution switch): the muxer cuts, writes `init_N.mp4` and marks a
  discontinuity.
- Assumes no B-frames (WebRTC H.264), so DTS = PTS and `ctts` offsets are 0.

## Codecs and fallbacks

| Source | Output | Process |
|--------|--------|---------|
| H.264 | H.264 | pure Go remux, **no FFmpeg** (asserted by `TestH264PathNeverSpawnsFFmpeg`) |
| Opus | Opus (`Opus` + `dOps`) | pure Go remux, default |
| Opus | AAC-LC 48 kHz stereo | `hls.audio_codec: aac` or `HLSOptions.audio_codec = AAC`; FFmpeg subprocess per track (Ogg Opus on stdin, ADTS on stdout) |
| VP8 | H.264 baseline | FFmpeg subprocess (IVF in, Annex B out, libx264 ultrafast/zerolatency, GOP 50) |

Transcoding tracks reserve extra capacity units on the worker (VP8: 3, AAC: 1, remux: 0 beyond
the job's own cost) so a worker does not over-admit CPU-heavy jobs. FFmpeg must be on `PATH` or
set `ffmpeg_path`. VP9/AV1 are not supported yet (the track is skipped and `track_errors`
increases).

## Browser and player compatibility (verified facts and open items)

- hls.js (MSE): fMP4 HLS with H.264 + Opus plays in Chrome, Edge, Firefox and Safari 17+
  (ADR-0007). **Not yet verified in this repository by a headless hls.js test**; ffprobe/ffmpeg
  decode validation passes for the generated playlists (see `pkg/hls` and `pkg/recorder` tests).
- Native Apple HLS: Opus is not in Apple's HLS authoring spec; treat as best effort and use AAC
  mode when recordings must play natively on iOS/macOS or third-party HLS players.
- Playlists are HLS version 7 (fMP4 requires 6+), `EXT-X-INDEPENDENT-SEGMENTS`, target duration
  is the ceiling of the longest segment, demuxed audio uses `EXT-X-MEDIA`.

## Statistics

`GET :6790/stats` on the recorder returns `recorder.Stats` JSON (active recordings with
per-track packets/bytes/loss/segments/backlog, segment delay, upload backlog, errors, the last
50 finished recordings with playlist URLs). `GET :6790/metrics` adds Prometheus series:

| Metric | Type |
|--------|------|
| `tessera_recorder_active_recordings`, `_tracks_recording`, `_longest_recording_seconds` | gauge |
| `tessera_recorder_segments_total`, `_uploaded_bytes_total` | counter |
| `tessera_recorder_upload_backlog`, `_capacity_used_ratio`, `_rtp_packets_lost` | gauge |
| `tessera_recorder_errors_total{type=upload,track,claim}` | counter |
| `tessera_recorder_finished_recordings_total{status}` | counter |
| `tessera_recorder_segment_delay_seconds` | histogram |

## Configuration (`recorder.yaml`)

`server_url`, `api_key`, `secret`, `worker_id`, `capacity`, `heartbeat`, `ffmpeg_path`,
`storage.kind: local|s3` (+ `local_root` or `s3.*`), `hls.segment_duration`, `hls.audio_codec`,
`metrics.addr`. Per-request `StorageOptions` override the default; local directories are
resolved below `local_root`.

## Known gaps

- `Room.active_recording` is not set (needs a `room.Admin` extension).
- Composite (grid) recording is not implemented; `StartRecording` returns `unimplemented`.
- No headless hls.js/Playwright test, no Apple device verification, no real-server end-to-end
  test (blocked on `pkg/client` landing); the pipeline is tested with in-memory RTP.
- Recorder CPU benchmark (100 tracks/vCPU) not yet measured.
