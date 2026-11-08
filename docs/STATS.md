# Live statistics (`pkg/stats`)

`pkg/stats` is the single statistics model of a Tessera node. The admin dashboard, the
recorder, the load tester and Prometheus all read the same data.

## Consuming

| Consumer | How |
|----------|-----|
| Go code in the `tessera` process | `stats.Provider.Snapshot()` (the room manager implements it; see `room.Manager` / `cmd/tessera`) |
| Dashboard, scripts, other processes | `GET http://<node>:6789/stats` (JSON `stats.Snapshot`), optional `?room=<name>` filter |
| Prometheus / Grafana | `GET http://<node>:6789/metrics`, metric names below |

`Snapshot()` is designed to be called about once per second. It takes control-plane locks
(room and participant lists) and allocates the result, but it **never** locks anything the
packet path uses: per-track counters are atomics (`stats.Meter`, `stats.Counter`,
`stats.Gauge`) that the forwarding goroutines update with single atomic adds. Rates
(`*_per_sec`, `bitrate_bps`, `loss_percent`) are computed inside the meters over windows of
at least `stats.MinRateWindow` (900 ms), so several consumers polling concurrently see
consistent values and polling faster than 1 Hz just returns cached rates.

## JSON model

```
Snapshot
├── time
├── node: NodeStats
│     node_id, version, started_at, num_cpu
│     cpu_percent (100 = one core), rss_bytes, heap_bytes, goroutines
│     gc_pause_p99_ns, gc_cpu_percent, num_gc
│     udp_packets_in_per_sec, udp_packets_out_per_sec, udp_bytes_in_per_sec, udp_bytes_out_per_sec
│     peer_connections, rooms, participants, published_tracks, subscribed_tracks
└── rooms[]: RoomStats
      sid, name, created_at, active_recording
      active_speakers[]: { participant_sid, identity, level (0..1) }   loudest first
      participants[]: ParticipantStats
          sid, identity, name, joined_at, state, kind (standard|recorder|loadtest), hidden, client_sdk
          ice_candidate_type (host|srflx|prflx|relay|tcp), ice_setup_ms, dtls_setup_ms
          rtt_ms, connection_quality (unknown|lost|poor|good|excellent), quality_score (1..5)
          bandwidth_estimate_bps                                           subscriber downlink BWE
          published[]: PublishedTrackStats
              sid, kind (audio|video), source, codec (MIME), muted, subscribers
              ssrc, width, height (announced), active                      one stream per track
              bitrate_bps, packets_recv, packets_lost, loss_percent, jitter_ms
              nacks, plis, firs                                            sent to the publisher
              audio_level (0..1)
          subscribed[]: SubscribedTrackStats
              track_sid, publisher_sid, kind, codec
              bandwidth_estimate_bps, bitrate_bps, packets_sent
              packets_lost, loss_percent, jitter_ms, rtt_ms                from subscriber RRs
              restarts (video starts on a keyframe), nacks, plis, retransmits
              paused (allocator or subscriber), muted
```

Field semantics are documented on the Go structs in `pkg/stats/stats.go`. Fields are only
ever added, never renamed (consumers should ignore unknown fields).

## Prometheus metrics

All names are prefixed `tessera_`. Labels come only from small closed sets; room,
participant and track identifiers are **never** labels, so the series count is constant
with load (about 60 series plus Go/process collectors).

| Metric | Type | Labels |
|--------|------|--------|
| `node_cpu_percent`, `node_rss_bytes`, `node_heap_bytes`, `node_goroutines` | gauge | |
| `node_gc_pause_p99_seconds`, `node_gc_cpu_percent` | gauge | |
| `peer_connections`, `rooms` | gauge | |
| `udp_packets_per_second`, `udp_bytes_per_second` | gauge | `direction` = in, out |
| `participants` | gauge | `kind` = standard, recorder, loadtest |
| `published_tracks` | gauge | `kind`, `codec` (MIME) |
| `subscribed_tracks` | gauge | `kind` |
| `published_bitrate_bps`, `subscribed_bitrate_bps` | gauge | `kind` |
| `subscribed_paused_tracks` | gauge | |
| `connection_quality_participants` | gauge | `quality` |
| `ice_candidate_participants` | gauge | `type` |
| `participant_rtt_ms_avg`, `ice_setup_ms_avg`, `dtls_setup_ms_avg` | gauge | |
| `active_speakers` | gauge | |
| `nack_received_total`, `nack_sent_total`, `pli_received_total`, `pli_sent_total`, `fir_sent_total` | counter | |
| `retransmits_total`, `participant_joins_total`, `track_publishes_total` | counter | |

The gauges are computed from one snapshot per scrape (`stats.Collector`); the counters are
process-global atomics that never decrease.

## Producing (engine side)

- Per received packet: `Meter.Add(len)` on the stream meter; buffer statistics are atomics.
- Per forwarded packet: `Meter.Add(len)` on the DownTrack meter.
- Media sockets: `stats.UDPIn.Add` / `stats.UDPOut.Add` from the mux socket wrapper.
- Events (NACK, PLI, switches): the per-track `Counter` plus the global counter.
