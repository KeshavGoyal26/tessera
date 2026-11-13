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

// Scale-safe data flow: a small live frame over SSE, paged lists and detail pages (docs: dashboard contract).
export interface RecorderSummary {
  url: string; ok: boolean; error?: string; capacity: number; capacity_used: number; active_recordings: number; tracks_recording: number
  segments_written: number; bytes_uploaded: number; upload_backlog: number; upload_errors: number; track_errors: number
  segment_delay_ms: number; segment_delay_max_ms: number
}
export interface Live {
  version: number; point: Point; node: Node
  counts: { rooms: number; presenters: number; viewers: number; relays: number; recorders: number }
  quality: { excellent: number; good: number; poor: number; lost: number; unknown: number }
  ice: Record<string, number>
  alerts: Alert[]; alerts_total: number; recorders: RecorderSummary[]
  webhooks?: WebhookStat[]
}
export interface WebhookStat { url: string; sent: number; failed: number; retried: number; dropped: number; queued: number; last_error?: string }
export interface Page<T> { version: number; total: number; offset: number; items: T[] }
export interface RoomBrief {
  name: string; sid: string; created_at: string; participants: number; presenters: number; viewers: number; relays: number
  recording: boolean; speakers: string[]; worst_quality: string
}
export interface ParticipantBrief {
  room: string; sid: string; identity: string; name?: string; role: 'presenter' | 'viewer' | 'relay' | 'recorder'
  kind: string; hidden?: boolean; state: string; quality: string; score: number; rtt_ms: number
  loss_percent: number; bitrate_bps: number; bwe_bps: number; ice_type: string; ice_setup_ms: number; dtls_setup_ms: number
  published: number; subscribed: number; paused: number; no_video: boolean; client_sdk?: string; joined_at: string
}
export interface SetupStats { avg: number; p95: number; max: number }
export interface ParticipantPage extends Page<ParticipantBrief> {
  quality: Record<string, number>; ice: Record<string, number>; ice_setup_ms: SetupStats; dtls_setup_ms: SetupStats; relay: number
}
export interface TrackBrief {
  room: string; participant: string; participant_sid: string; sid: string; kind: string; source: string; codec: string
  width?: number; height?: number; bitrate_bps: number; loss_percent: number; jitter_ms: number
  subscribers: number; active: boolean; muted: boolean; nacks: number; plis: number
}
export interface SubscriberBrief {
  sid: string; identity: string; bitrate_bps: number; loss_percent: number; jitter_ms: number; rtt_ms: number
  bwe_bps: number; paused: boolean; muted: boolean; restarts: number
}
export interface ParticipantDetail {
  version: number; room: string; participant: Participant & { subscribed_total?: number }; names: Record<string, string>
  /** Count of subscriptions when the list is capped (500). */
  subscribed_total?: number
}
export interface TrackDetail {
  version: number; room: string; participant: string; participant_sid: string; track: Published
  subscribers: SubscriberBrief[]; subscribers_total: number
}
export interface CoordRecordingPage { total: number; offset: number; items: CoordRecording[] }
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
export interface Capacity { nodes: number; avg_load: number; max_load: number; busiest?: string; n1: boolean; note: string }
export interface ClusterState { self: string; time: number; error?: string; nodes: NodeView[] | null; alerts: Alert[] | null; point?: ClusterPoint; capacity?: Record<string, Capacity> }
/** A page merged from every reachable node (cluster/rooms, cluster/participants, …). */
export interface ClusterPage<T> { total: number; offset: number; items: T[]; errors: string[] | null; truncated?: boolean }
export type ClusterParticipantBrief = ParticipantBrief & { node: string }
export interface ClusterRoom { name: string; presenters: number; viewers: number; recording?: boolean; media: string[]; edges: string[] }
export interface NodeRoom { node: string; role: string; room?: Room; error?: string }

// Sessions: one participant connection, active or recently ended (node-local).
export interface QualityShare { excellent: number; good: number; poor: number; lost: number; unknown: number }
export interface SessionBrief {
  sid: string; identity: string; name?: string; room: string; role: string; state: 'active' | 'ended'
  started_at: string; ended_at?: string; duration_ms: number; quality: string; share: QualityShare
  max_rtt_ms: number; max_loss_percent: number; ice_type: string; client_sdk?: string; events: number
}
/** kind is open: the server may add kinds the browser does not know. */
export interface SessionEvent { t: string; kind: string; detail: string }
export interface Hops {
  uplink: { track_sid: string; kind: string; loss_percent: number; jitter_ms: number; bitrate_bps: number }[]
  downlink: { track_sid: string; presenter: string; kind: string; loss_percent: number; jitter_ms: number; rtt_ms: number; bitrate_bps: number; paused: boolean }[]
  sources: { presenter: string; track_sid: string; kind: string; uplink_loss_percent: number; on_this_node: boolean }[]
}
/** The contract's SessionBrief & { events: SessionEvent[] }: here `events` is the list, not the count. */
export type SessionDetail = Omit<SessionBrief, 'events'> & { events: SessionEvent[]; hops?: Hops }
