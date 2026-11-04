# WP1 forwarding performance notes

Hot-path micro-benchmarks for the SFU engine (ARCHITECTURE.md section 9, targets P1 and P2).

## How to run

```bash
go test -run '^$' -bench . -benchmem ./pkg/rtc/ ./pkg/rtc/buffer/ ./pkg/rtc/simulcast/
```

```bash
go test -run TestForwardingDoesNotAllocate ./pkg/rtc/
```

```bash
go test -run '^$' -bench 'ReceiverFanout/100$' -cpuprofile cpu.out ./pkg/rtc/ && go tool pprof -top cpu.out
```

## What is measured

| Benchmark | Path |
|-----------|------|
| `BenchmarkBufferWrite` | RTP header parse, SN/TS unwrap, ring copy, VP8 descriptor parse, jitter/loss counters |
| `BenchmarkForwarderGetTranslation` | layer decision + SN/TS/picture-id munging + NACK history record |
| `BenchmarkDownTrackWriteRTP` | forwarder + VP8 descriptor rewrite + RTP header marshal + **SRTP AES-128-CM/HMAC-SHA1-80 encryption** of a ~1 kB packet (socket write excluded) |
| `BenchmarkReceiverFanout/{1,10,100,1000}` | buffer write once + inline fan-out to N DownTracks, each doing the full DownTrackWriteRTP path |

`TestForwardingDoesNotAllocate` runs as a normal test and fails if a forwarded packet allocates
(`testing.AllocsPerRun` over buffer write + fan-out to 3 DownTracks).

## Results

Machine: cloud container, 4 vCPU (x86-64), 15 GB RAM, Linux 6.18, go1.27.3, `GOMAXPROCS=4`.
Numbers are single-goroutine (one core).

| Benchmark | ns/op | allocs/op | Notes |
|-----------|-------|-----------|-------|
| `BenchmarkBufferWrite` | 103 | 0 | ~9.9 GB/s copy into the ring |
| `BenchmarkForwarderGetTranslation` | 54 | 0 | |
| `BenchmarkDownTrackWriteRTP` | not yet recorded | 0 (verified by `TestForwardingDoesNotAllocate`) | run the command above |
| `BenchmarkReceiverFanout/*` | not yet recorded | 0 (same test) | run the command above |

The SRTP and fan-out benchmarks landed at the end of session A; the tooling was unavailable at that
point, so their numbers still have to be recorded here. The zero-allocation property of the full
forwarding path (buffer write, forwarder, VP8 rewrite, header marshal, SRTP encryption) is enforced by
`TestForwardingDoesNotAllocate` in every `go test` run.

## pprof notes

Not done yet. Expected top entries from code inspection: AES-CTR + HMAC-SHA1 in `srtp.EncryptRTP`
(dominant per egress packet), `rtp.Header.MarshalTo`, the forwarder mutex, and the VP8 payload copy in
`simulcast.RewriteVP8` (only for packets carrying a picture id).

## Next steps

- Parallel fan-out shards above `rtc.fanout_inline_max` (32) DownTracks: the inline loop is single-core, so a
  1000-subscriber webinar layer is bounded by one core's egress rate.
- `rtp.Header.MarshalTo` + Pion's interceptor chain run per DownTrack; writing pre-marshalled packets directly
  to the SRTP session would save the header marshal.
- `sendmmsg` batching and `SO_REUSEPORT` sharding of the UDP mux (WP5).
