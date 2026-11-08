package rtc

import (
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/KeshavGoyal26/tessera/pkg/rtc/buffer"
)

type closeCountingDown struct {
	id     string
	closed atomic.Bool
}

func (d *closeCountingDown) Bind(webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	return webrtc.RTPCodecParameters{}, nil
}
func (d *closeCountingDown) Unbind(webrtc.TrackLocalContext) error { return nil }
func (d *closeCountingDown) ID() string                            { return "TR_x" }
func (d *closeCountingDown) RID() string                           { return "" }
func (d *closeCountingDown) StreamID() string                      { return "PA_x" }
func (d *closeCountingDown) Kind() webrtc.RTPCodecType             { return webrtc.RTPCodecTypeVideo }
func (d *closeCountingDown) SubscriberID() string                  { return d.id }
func (d *closeCountingDown) WriteRTP(*buffer.ExtPacket) error      { return nil }
func (d *closeCountingDown) SetDisabled(bool)                      {}
func (d *closeCountingDown) Mute(bool)                             {}
func (d *closeCountingDown) HandleRTCP([]rtcp.Packet)              {}
func (d *closeCountingDown) Close()                                { d.closed.Store(true) }

// A Close that runs between AddDownTrack's fast check and taking dtMu must
// still close the DownTrack (regression: the check only ran outside dtMu).
func TestReceiverCloseDuringAddDownTrack(t *testing.T) {
	r := NewReceiver(ReceiverParams{TrackID: "TR_x", StreamID: "PA_x", Kind: webrtc.RTPCodecTypeVideo,
		Codec: webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}}})
	testHookAddDownTrack = r.Close
	t.Cleanup(func() { testHookAddDownTrack = func() {} })
	d := &closeCountingDown{id: "a"}
	if err := r.AddDownTrack(d); err == nil && !d.closed.Load() {
		t.Fatal("AddDownTrack accepted a DownTrack after Close and it was never closed")
	}
}

// Every DownTrack that AddDownTrack accepts must be closed by a concurrent
// Receiver.Close (regression: the closed check ran outside dtMu).
func TestReceiverCloseClosesConcurrentlyAddedDownTracks(t *testing.T) {
	for range 200 {
		r := NewReceiver(ReceiverParams{TrackID: "TR_x", StreamID: "PA_x", Kind: webrtc.RTPCodecTypeVideo,
			Codec: webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}}})
		var accepted []*closeCountingDown
		var mu sync.Mutex
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				d := &closeCountingDown{id: string(rune('a' + i))}
				if r.AddDownTrack(d) == nil {
					mu.Lock()
					accepted = append(accepted, d)
					mu.Unlock()
				}
			}()
		}
		r.Close()
		wg.Wait()
		for _, d := range accepted {
			if !d.closed.Load() {
				t.Fatalf("DownTrack %s accepted but never closed", d.id)
			}
		}
	}
}

type blockingSource struct{ done chan struct{} }

func (s blockingSource) Read([]byte) (int, error) { <-s.done; return 0, io.EOF }
func (blockingSource) SSRC() uint32               { return 42 }

type lossyDown struct {
	closeCountingDown
	frac uint8
}

func (d *lossyDown) ReportedFractionLost() uint8 { return d.frac }

// The receiver report for an audio track carries the worst subscriber loss, so the
// publisher's Opus encoder adds in-band FEC for listeners behind lossy links.
func TestAudioReceiverReportCarriesSubscriberLoss(t *testing.T) {
	for _, tc := range []struct {
		mime string
		kind webrtc.RTPCodecType
		want uint8
	}{{webrtc.MimeTypeOpus, webrtc.RTPCodecTypeAudio, 64}, {webrtc.MimeTypeH264, webrtc.RTPCodecTypeVideo, 0}} {
		clock := uint32(48000)
		if tc.kind == webrtc.RTPCodecTypeVideo {
			clock = 90000
		}
		r := NewReceiver(ReceiverParams{TrackID: "TR_x", StreamID: "PA_x", Kind: tc.kind,
			Codec: webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: tc.mime, ClockRate: clock}}})
		buf := buffer.New(buffer.Config{Slots: 64, ClockRate: clock, MimeType: tc.mime})
		for i := range 10 {
			p := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: uint16(100 + i), Timestamp: uint32(960 * i), SSRC: 42}, Payload: []byte{1, 2, 3}}
			raw, _ := p.Marshal()
			if _, err := buf.Write(raw, int64(i)*20e6); err != nil {
				t.Fatal(err)
			}
		}
		src := blockingSource{done: make(chan struct{})}
		if err := r.Start(src, buf); err != nil {
			t.Fatal(err)
		}
		if err := r.AddDownTrack(&lossyDown{closeCountingDown: closeCountingDown{id: "a"}, frac: 64}); err != nil {
			t.Fatal(err)
		}
		rrs := r.ReceptionReports()
		close(src.done)
		r.Close()
		if len(rrs) != 1 || rrs[0].FractionLost != tc.want {
			t.Fatalf("%s: reports %+v, want fraction lost %d", tc.mime, rrs, tc.want)
		}
	}
}
