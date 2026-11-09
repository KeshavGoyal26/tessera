# Recording (HLS)

Implements ADR-0007 (amended 2026-11-07: recording runs inside the server). The recorder reads
each recorded track straight from the server's receive buffer, remuxes it into fMP4/CMAF HLS
and writes it to local disk or an S3-compatible bucket. There is no separate recorder process
and no second WebRTC session: about half the per-presenter CPU of the old design.

## Flow

1. `RecordingService.StartRecording` (admin API) creates a job in the in-server **Coordinator**
   (`pkg/recorder`, status `STARTING`).
2. The in-server worker claims the job from the Coordinator. `LocalConnector` attaches a
   DownTrack to each wanted track's receiver, bound to an in-memory queue instead of a
   PeerConnection (keyframe requests and RED stripping as for any subscriber). The forwarding path never blocks on it: a full queue drops packets and the
   recorder waits for the next keyframe.
3. For every subscribed track that matches the target (`TrackRecordingTarget` or
   `ParticipantRecordingTarget`) a per-track pipeline runs:
   `RTP → reorder buffer (200 ms) → depacketizer → muxer (fMP4 + playlist) → ordered uploader`.
4. The worker reports `ACTIVE` when the first sample is muxed, heartbeats every 5 s (the
   Coordinator fails jobs after `recording.heartbeat_timeout`, 15 s, or re-queues them if they
   never started) and receives stop requests in the heartbeat response (the in-server
   worker is told at once, without waiting for a heartbeat).
5. On stop the muxers flush, uploads drain, a `master.m3u8` is written (participant recordings
   and whenever more than one track was recorded) and the job ends `COMPLETE` (`FAILED` on
   upload/track errors, `ABORTED` when stopped before any media).
6. The Coordinator emits `recording_started` (first `ACTIVE`) and `recording_ended` webhooks
   whose payload is the full `RecordingInfo` including `outputs` (playlist location/URL,
   segment count, duration, size).

## Output layout

```
<prefix>/                      prefix = filename_prefix, default "{room}/{recording_id}"
  master.m3u8                  EXT-X-MEDIA TYPE=AUDIO (demuxed audio) + EXT-X-STREAM-INF; written live
                               once the tracks have segments (hls_url attribute, RECORDING_UPDATED)
  quality.json                 connection timeline, one sample per 4 s (format: PROTOCOL.md section 6)
  <track_sid>/index.m3u8       media playlist (EVENT while live, VOD + ENDLIST at the end)
  <track_sid>/init.mp4         init segment (init_N.mp4 after a codec change: a new EXT-X-MAP,
                               no discontinuity; keyframes also carry their SPS/PPS in-band)

A playlist's timeline is always its media timeline, so no rendition ever needs an
EXT-X-DISCONTINUITY (one in a single rendition stalls hls.js): a pause of up to 8 s holds the
last sample (a frozen frame while the audio goes on), a codec change switches at the next
sample, a longer stretch without media is listed as EXT-X-GAP entries (players step over it),
and a clock that goes backwards (new SSRC) continues the timeline.
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
  plus the elapsed wall time. A forward DTS jump larger than 2x the target duration starts a new
  segment after EXT-X-GAP entries for the stretch without media; a clock that moves backwards
  continues the timeline. No EXT-X-DISCONTINUITY is ever written.
- SPS/PPS change (resolution switch): from the next sample the muxer cuts and writes
  `init_N.mp4` (a new EXT-X-MAP, same timeline).
- One stream per track (no simulcast, ADR-0012): the recording is the stream viewers see.
  When a short uplink makes the publisher's encoder lower its resolution, that is a resolution
  change (above).
- Assumes no B-frames (WebRTC H.264), so DTS = PTS and `ctts` offsets are 0.
- Crash (kill -9, OOM, power loss): media up to the last uploaded segment is in storage, but the
  playlists are still live and there is no master. A crash journal in `temp_dir/journal` (last
  uploaded playlist and track details, removed when a recording ends normally) lets the next
  start close the playlists, write the master and send `recording_ended` with status `FAILED`
  and the outputs. Works for local and S3 storage; recordings with per-request S3 credentials
  are not journaled. Keep `temp_dir` on persistent disk for this to survive a reboot.

## Codecs and fallbacks

| Source | Output | Process |
|--------|--------|---------|
| H.264 | H.264 | pure Go remux, **no FFmpeg** (asserted by `TestH264PathNeverSpawnsFFmpeg`) |
| Opus | Opus (`Opus` + `dOps`) | pure Go remux, default |
| Opus | AAC-LC 48 kHz stereo | `hls.audio_codec: aac` or `HLSOptions.audio_codec = AAC`; FFmpeg subprocess per track (Ogg Opus on stdin, ADTS on stdout) |
| other video | refused | not transcoded: the track is skipped and the recording fails with "recording needs H.264 video". The server negotiates only H.264 for video, so this happens only with a custom client. |

AAC tracks reserve an extra capacity unit on the worker (remux: 0 beyond
the job's own cost) so a worker does not over-admit CPU-heavy jobs. FFmpeg is needed only for
AAC mode (on `PATH` or `ffmpeg_path`). VP8/VP9/AV1 are refused (`track_errors` increases and
the recording fails with the reason).

## Browser and player compatibility (verified facts and open items)

- hls.js (MSE): fMP4 HLS with H.264 + Opus plays in Chrome, Edge, Firefox and Safari 17+
  (ADR-0007). Verified manually for this repository: the master playlist of a recorded
  participant (H.264 + demuxed Opus, produced by `TestWorkerParticipantRecordingEndToEnd` with
  `TESSERA_TEST_OUTDIR` set) loads with hls.js 1.7.4 in headless Chromium (Playwright) without
  errors, reports 12 s / 3 fragments, and plays (currentTime advances, video 320x240). Not an
  automated CI test yet. ffprobe/ffmpeg decode validation of every generated playlist runs in
  `pkg/hls` and `pkg/recorder` tests.
- Native Apple HLS: Opus is not in Apple's HLS authoring spec; treat as best effort and use AAC
  mode when recordings must play natively on iOS/macOS or third-party HLS players.
- Playlists are HLS version 7 (fMP4 requires 6+), `EXT-X-INDEPENDENT-SEGMENTS`, target duration
  is the ceiling of the longest segment, demuxed audio uses `EXT-X-MEDIA`.

## Statistics

`GET /recorder/stats` on the metrics listener (`metrics.addr`, default `127.0.0.1:6789`) returns
`recorder.Stats` JSON (active recordings with
per-track packets/bytes/loss/segments/backlog, segment delay, upload backlog, errors, the last
50 finished recordings with playlist URLs). `/metrics` includes Prometheus series:

| Metric | Type |
|--------|------|
| `tessera_recorder_active_recordings`, `_tracks_recording`, `_longest_recording_seconds` | gauge |
| `tessera_recorder_segments_total`, `_uploaded_bytes_total` | counter |
| `tessera_recorder_upload_backlog`, `_capacity_used_ratio`, `_rtp_packets_lost` | gauge |
| `tessera_recorder_errors_total{type=upload,track,claim}` | counter |
| `tessera_recorder_finished_recordings_total{status}` | counter |
| `tessera_recorder_segment_delay_seconds` | histogram |

## Recording on join (`auto_record`)

A participant whose token grant has `auto_record: true` (Java: `RoomGrant.builder().autoRecord(true)`)
is recorded without any API call: when they join, the server starts a participant recording; when
they leave, it stops every in-progress participant recording of that identity in that room. A
room created with `recording_enabled` (token `room_config` or `CreateRoom`) records every
participant who joins it this way. A participant who joins with camera and microphone off is
recorded once they publish; until then the job waits instead of failing after 30 s. Rejoining
starts a new recording. Short network outages resume the same session and recording (the
recording has a gap); when the outage is long enough that the SDK rejoins as a new session
(about 30 s or more in tests), the old recording ends at once and the new session is recorded
separately. The old recording never takes the new session's tracks. In `--dev`, `/dev/token?...&record=1` issues such a token and also sets
the participant attribute `recorded=true` so clients can show a REC marker.

## Configuration (`recording:` in the server config)

`enabled` (default true; disable on media-only nodes), `capacity` (units, one per remuxed track;
default 150 per CPU, i.e. 75 camera+mic presenters per CPU; a recording beyond it waits in the
queue and misses its start, watch `tessera_recording_jobs_queued`), `temp_dir`, `ffmpeg_path`
(only for the AAC audio mode), `storage.kind: local|s3` (+ `local_root` or `s3.*`),
`hls.segment_duration`, `hls.audio_codec`, `storage.local_base_url` (serve `local_root` read-only
under `/recordings/` on the metrics listener and report playlist URLs below this prefix, so the
dashboard can play local recordings). Per-request `StorageOptions` override the default; local directories are
resolved below `local_root`.

## Known gaps

- `Room.active_recording` is not set (needs a `room.Admin` extension).
- Composite (grid) recording is not implemented; `StartRecording` returns `unimplemented`.
- No automated hls.js/Playwright test and no Apple device verification. The pipeline is tested
  with in-memory RTP and, in `TestLiveRecordingThroughRealServer`, end to end: a real SFU on
  loopback, a publisher with H.264 + Opus, Coordinator + Worker with `LocalConnector`.
- Benchmark: `go test -bench RemuxH264 ./pkg/hls` (RTP unmarshal + depacketize + mux + playlist,
  storage excluded) measured about 0.04 ms CPU per media-second for a 320x240 100 kbps track on a
  2.1 GHz Xeon, i.e. orders of magnitude under the 10 ms/media-second budget of the 100
  tracks/vCPU target. Higher bitrates cost mostly memory copies; a 720p measurement with a real
  uploader (S3 TLS) is still open.
