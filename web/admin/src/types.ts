// Mirrors the JSON of pkg/stats (docs/STATS.md) and pkg/dashboard.
export interface Published {
  sid: string; kind: string; source: string; codec: string; muted: boolean; ssrc: number; width?: number; height?: number; active: boolean
  bitrate_bps: number; packets_recv: number; packets_lost: number; loss_percent: number; jitter_ms: number
  nacks: number; plis: number; firs: number; subscribers: number; audio_level?: number
}
export interface Subscribed {
  track_sid: string; publisher_sid: string; kind: string; codec: string
  bandwidth_estimate_bps: number; bitrate_bps: number; packets_sent: number; packets_lost: number; loss_percent: number
  jitter_ms: number; rtt_ms: number; restarts: number; nacks: number; plis: number; retransmits: number
  paused: boolean; muted: boolean
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
    outputs: null | { track_sid: string; participant: string; playlist_url?: string; playlist_location: string; segments: number; duration_ms: number }[] }[]
}
export interface RecorderStatus { url: string; ok: boolean; error?: string; stats?: RecorderStats }
export interface Update { snapshot: Snapshot; recorders: RecorderStatus[]; alerts: Alert[]; point: Point }
export interface CoordRecording { recording_id: string; room: string; status: string; started_at_ms?: string; ended_at_ms?: string; error?: string; worker_id?: string
  request?: { participant?: { identity: string }; track?: { track_sid: string } }
  master_playlist_location?: string
  /** Set once the tracks have their first segments, so also while recording (a live EVENT playlist). */
  master_playlist_url?: string
  outputs?: { track_sid: string; participant_identity?: string; playlist_url?: string; duration_ms?: string }[] }

// Cluster view (pkg/dashboard/cluster.go).
export interface NodeSummary {
  version: string; started_at: number; cpu: number; cores: number; rss: number; bps_in: number; bps_out: number; rooms: number
  /** On an edge these are the relayed copies of presenters. */
  presenters: number; viewers: number; relays: number; published: number; subscribed: number
  recordings: number; rec_used: number; rec_cap: number; loss_max: number; rtt_avg: number; critical: number; warnings: number
}
export interface NodeView { id: string; role: string; self?: boolean; draining?: boolean; down?: boolean; reachable: boolean; seen: number; summary?: NodeSummary }
export interface ClusterPoint {
  t: number; media: number; edges: number; down: number; presenters: number; viewers: number; bps_in: number; bps_out: number
  cpu_max: number; cpu_avg: number; recordings: number; rec_used: number; rec_cap: number; loss_max: number
}
export interface ClusterState { self: string; time: number; error?: string; nodes: NodeView[] | null; alerts: Alert[] | null; point?: ClusterPoint }
export interface ClusterRoom { name: string; presenters: number; viewers: number; recording?: boolean; media: string[]; edges: string[] }
export interface NodeRoom { node: string; role: string; room?: Room; error?: string }
