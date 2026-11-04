package hls_test

import (
	"context"
	"testing"
	"time"

	"github.com/pion/rtp"

	"github.com/KeshavGoyal26/tessera/pkg/hls"
	"github.com/KeshavGoyal26/tessera/pkg/hls/hlstest"
)

type discardSink struct{ bytes int }

func (d *discardSink) WriteInit(_ context.Context, _ string, b []byte) error {
	d.bytes += len(b)
	return nil
}
func (d *discardSink) WriteSegment(_ context.Context, s hls.Segment) error {
	d.bytes += len(s.Data)
	return nil
}
func (d *discardSink) WritePlaylist(_ context.Context, _ string, b []byte) error {
	d.bytes += len(b)
	return nil
}

// BenchmarkRemuxH264 measures the CPU cost of recording one 12 s, 25 fps H.264
// track (RTP unmarshal, depacketize, mux, playlist; storage excluded). The
// reported ms/media-second must stay below 10 for the target of 100 concurrent
// track recordings per vCPU (docs/RECORDING.md).
func BenchmarkRemuxH264(b *testing.B) {
	hlstest.NeedFFmpeg(b)
	aus := hlstest.H264(b, 12)
	p := &hlstest.Packetizer{SSRC: 1}
	var raw [][]byte
	for i, au := range aus {
		for _, pkt := range p.Packetize(au, uint32(i*3600)) {
			bb, _ := pkt.Marshal()
			raw = append(raw, bb)
		}
	}
	var sps, pps []byte
	for _, n := range aus[0] {
		switch n[0] & 0x1f {
		case 7:
			sps = n
		case 8:
			pps = n
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		sink := &discardSink{}
		m, err := hls.NewMuxer(hls.MuxerConfig{Live: true, TargetSegmentDuration: 4 * time.Second}, hls.TrackSpec{ID: 1, Codec: hls.CodecH264, Timescale: 90000, SPS: sps, PPS: pps}, sink)
		if err != nil {
			b.Fatal(err)
		}
		d := &hls.H264Depacketizer{}
		var u hls.Unwrapper
		for _, r := range raw {
			var pkt rtp.Packet
			if err := pkt.Unmarshal(r); err != nil {
				b.Fatal(err)
			}
			aus, _ := d.Push(&pkt)
			for _, a := range aus {
				if err := m.WriteSample(context.Background(), hls.Sample{DTS: u.Unwrap(a.RTPTimestamp), KeyFrame: a.KeyFrame, Data: a.AVCC()}); err != nil {
					b.Fatal(err)
				}
			}
		}
		if err := m.Close(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
	perSecond := time.Since(start).Seconds() / float64(b.N) / 12
	b.ReportMetric(perSecond*1000, "ms/media-sec")
	b.ReportMetric(1/perSecond, "tracks/core")
}
