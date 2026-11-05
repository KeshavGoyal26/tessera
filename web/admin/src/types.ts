// Mirrors the JSON of pkg/stats (docs/STATS.md) and pkg/dashboard.
export interface Layer { spatial: number; quality: string; rid?: string; ssrc: number; width: number; height: number; fps: number; bitrate_bps: number; active: boolean }
export interface Published {
  sid: string; kind: string; source: string; codec: string; muted: boolean; simulcast: boolean; layers: Layer[] | null
  bitrate_bps: number; packets_recv: number; packets_lost: number; loss_percent: number; jitter_ms: number
  nacks: number; plis: number; firs: number; subscribers: number; audio_level?: number
}
export interface Subscribed {
  track_sid: string; publisher_sid: string; kind: string; codec: string
  current_spatial: number; current_temporal: number; target_spatial: number; target_temporal: number; max_spatial: number
  bandwidth_estimate_bps: number; bitrate_bps: number; packets_sent: number; packets_lost: number; loss_percent: number
  jitter_ms: number; rtt_ms: number; layer_switches: number; nacks: number; plis: number; retransmits: number
  paused: boolean; dynacast_paused: boolean; muted: boolean
}
export interface Participant {
  sid: string; identity: string; name?: string; joined_at: string; state: string; kind: string; hidden?: boolean; client_sdk?: string
  ice_candidate_type: string; ice_setup_ms: number; dtls_setup_ms: number; rtt_ms: number
  connection_quality: string; quality_score: number; bandwidth_estimate_bps: number
  published: Published[] | null; subscribed: Subscribed[] | null
}
export interface Room { sid: string; name: string; created_at: string; active_recording: boolean; participants: Participant[] | null; active_speakers: { identity: string; level: number }[] | null }
export interface Node {
  node_id: string; version: string; started_at: string; cpu_percent: number; num_cpu: number; rss_bytes: number; heap_bytes: number
  goroutines: number; gc_pause_p99_ns: number; gc_cpu_percent: number; num_gc: number
  udp_packets_in_per_sec: number; udp_packets_out_per_sec: number; udp_bytes_in_per_sec: number; udp_bytes_out_per_sec: number
  peer_connections: number; rooms: number; participants: number; published_tracks: number; subscribed_tracks: number
}
export interface Snapshot { time: string; node: Node; rooms: Room[] | null }
export interface Point {
  t: number; cpu: number; rss: number; heap: number; goroutines: number; gc_pause_ns: number; gc_cpu: number
  pkts_in: number; pkts_out: number; bps_in: number; bps_out: number; rooms: number; participants: number
  published: number; subscribed: number; loss_avg: number; loss_max: number; rtt_avg: number; rtt_max: number; recordings: number
}
export interface Alert { id: string; severity: 'warning' | 'critical'; kind: string; message: string; since: string }
export interface RecTrack {
  track_sid: string; participant: string; kind: string; codec: string; width?: number; height?: number; packets: number; bytes: number
  packets_lost: number; dropped_frames: number; segments: number; duration_ms: number; upload_backlog: number; upload_errors: number
  last_packet_age_ms: number; error?: string
}
export interface RecorderStats {
  worker_id: string; capacity: number; capacity_used: number; active_recordings: number; tracks_recording: number
  segments_written: number; bytes_uploaded: number; upload_backlog: number; upload_errors: number; track_errors: number
  segment_delay_ms: number; segment_delay_max_ms: number; finished_total: number
  recordings: { recording_id: string; room: string; status: string; started_at: string; duration_ms: number; tracks: RecTrack[] }[]
  finished: { recording_id: string; room: string; status: string; error?: string; ended_at: string; duration_ms: number; master_playlist?: string; master_url?: string
    outputs: { track_sid: string; participant: string; playlist_url?: string; playlist_location: string; segments: number; duration_ms: number }[] }[]
}
export interface RecorderStatus { url: string; ok: boolean; error?: string; stats?: RecorderStats }
export interface Update { snapshot: Snapshot; recorders: RecorderStatus[]; alerts: Alert[]; point: Point }
export interface CoordRecording { recording_id: string; room: string; status: string; started_at_ms?: string; ended_at_ms?: string; error?: string; worker_id?: string
  request?: { participant?: { identity: string }; track?: { track_sid: string } }
  master_playlist_location?: string
  outputs?: { track_sid: string; participant_identity?: string; playlist_url?: string; duration_ms?: string }[] }
