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
| `BenchmarkDownTrackWriteRTP` | 2058 | 0 | 486k pkts/s per core incl. SRTP (target P1: 100k) |
| `BenchmarkReceiverFanout/1` | 2171 | 0 | 461k egress pkts/s |
| `BenchmarkReceiverFanout/10` | 20042 | 0 | 499k egress pkts/s |
| `BenchmarkReceiverFanout/100` | 230821 | 0 | 433k egress pkts/s |
| `BenchmarkReceiverFanout/1000` | 2565892 | 0 | 390k egress pkts/s (cache pressure from 1000 DownTracks) |

Measured 2026-11-04 with the first command above. The packet is ~1 kB VP8 with a 15-bit picture id, so
every egress packet also goes through the VP8 descriptor rewrite (payload copy).

## pprof notes

`BenchmarkReceiverFanout/100`, `go tool pprof -top`:

| flat % | function |
|--------|----------|
| 31% | `aes.encryptBlockAsm` (SRTP AES-CM keystream) |
| 27% | `sha1.blockSHANI` (SRTP HMAC-SHA1 auth tag) |
| ~8% | AES block wrapper + `srtp.xorBytesCTR` |
| ~9% | `simulcast.(*Forwarder)` (`forwardCurrent` + `translate`) |

About 85% of the forwarding CPU is SRTP crypto, which Tessera cannot avoid per DownTrack (every subscriber
has its own keys). Our own code (buffer, forwarder, VP8 rewrite, header marshal) is under 15%. Ways to cut
the crypto cost: negotiate `SRTP_AEAD_AES_128_GCM` first (AES-GCM with hardware GHASH is much cheaper than
AES-CM + HMAC-SHA1), which is a one-line SettingEngine change worth benchmarking with browsers.

## Next steps

- Parallel fan-out shards above `rtc.fanout_inline_max` (32) DownTracks: the inline loop is single-core, so a
  1000-subscriber webinar layer is bounded by one core's egress rate.
- `rtp.Header.MarshalTo` + Pion's interceptor chain run per DownTrack; writing pre-marshalled packets directly
  to the SRTP session would save the header marshal.
- `sendmmsg` batching and `SO_REUSEPORT` sharding of the UDP mux (WP5).
