package recorder

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/auth"
	"github.com/KeshavGoyal26/tessera/pkg/room"
	"github.com/KeshavGoyal26/tessera/pkg/rtc"
)

// LocalConnector records inside the media server. Each wanted track gets a DownTrack,
// exactly as a subscriber would (layer selection, keyframe requests on switches, RED
// stripping), but bound to an in-memory queue instead of a PeerConnection: no second
// WebRTC session, no re-encryption, no loopback hop.
type LocalConnector struct {
	Rooms    room.TrackWatcher
	Verifier auth.Verifier
}

var _ Connector = LocalConnector{}

// tapQueue bounds the packets waiting for the recorder per track. The forwarding path
// never blocks on it: when full, packets are dropped and the recorder sees the gap
// (it waits for, and asks for, the next keyframe).
const tapQueue = 1024

// Join implements Connector. The token (minted by the coordinator for the job) names
// the room; its identity names this recording's DownTracks.
func (lc LocalConnector) Join(_ context.Context, _, token string, want func(Track) bool, onTrack func(Track), onClosed func(error)) (Room, error) {
	claims, err := lc.Verifier.Verify(token)
	if err != nil {
		return nil, err
	}
	lr := &localRoom{taps: map[string]*tap{}, left: make(chan struct{})}
	stop, done, err := lc.Rooms.WatchTracks(claims.Grants.Room, func(pt room.PublishedTrack) {
		tr := Track{SID: pt.Info.GetSid(), ParticipantIdentity: pt.Identity, Kind: kindName(pt.Info.GetType()), Source: sourceName(pt.Info.GetSource())}
		if !want(tr) {
			return
		}
		lr.mu.Lock()
		if lr.closed || lr.taps[tr.SID] != nil {
			lr.mu.Unlock()
			return
		}
		t, err := newTap(pt.Receiver, claims.Identity(), tr.SID, pt.Identity)
		if err != nil {
			lr.mu.Unlock()
			return
		}
		lr.taps[tr.SID] = t
		lr.mu.Unlock()
		tr.RTP = t
		onTrack(tr)
	})
	if err != nil {
		return nil, err
	}
	lr.stop = stop
	go func() {
		select {
		case <-done: // room closed
			onClosed(nil)
		case <-lr.left:
		}
	}()
	return lr, nil
}

type localRoom struct {
	stop   func()
	left   chan struct{}
	mu     sync.Mutex
	closed bool
	taps   map[string]*tap
}

// Close implements Room: detaches every tap from its receiver.
func (lr *localRoom) Close(context.Context) error {
	lr.mu.Lock()
	if lr.closed {
		lr.mu.Unlock()
		return nil
	}
	lr.closed = true
	taps := lr.taps
	lr.mu.Unlock()
	lr.stop()
	close(lr.left)
	for _, t := range taps {
		t.close()
	}
	return nil
}

// tap is an RTPSource fed by a DownTrack bound to it (it is the DownTrack's writer).
type tap struct {
	dt      *rtc.DownTrackImpl
	codec   webrtc.RTPCodecParameters
	ch      chan []byte
	done    chan struct{}
	endOnce sync.Once
	dropped atomic.Uint64
	onSR    atomic.Pointer[func(ntp uint64, rtpTS uint32)]
}

var (
	_ RTPSource               = (*tap)(nil)
	_ SRSource                = (*tap)(nil)
	_ webrtc.TrackLocalWriter = (*tap)(nil)
)

func newTap(recv *rtc.ReceiverImpl, subscriber, trackID, streamID string) (*tap, error) {
	c := recv.Codec()
	if strings.EqualFold(c.MimeType, rtc.MimeTypeRED) {
		// Offer plain Opus: the DownTrack then sends each packet's primary frame.
		c.MimeType, c.SDPFmtpLine = webrtc.MimeTypeOpus, "minptime=10;useinbandfec=1"
	}
	t := &tap{codec: c, ch: make(chan []byte, tapQueue), done: make(chan struct{})}
	dt := rtc.NewDownTrack(rtc.DownTrackParams{SubscriberID: subscriber, Receiver: recv, TrackID: trackID, StreamID: streamID})
	if _, err := dt.Bind(tapContext{t}); err != nil {
		return nil, err
	}
	t.dt = dt
	if err := recv.AddDownTrack(dt); err != nil {
		return nil, err
	}
	go t.watch()
	return t, nil
}

// watch emits sender reports (audio/video alignment) and ends the stream when the
// DownTrack is closed (track unpublished, publisher left, room closed).
func (t *tap) watch() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-tick.C:
			if t.dt.Closed() {
				t.end()
				return
			}
			if f := t.onSR.Load(); f != nil {
				if sr := t.dt.SenderReport(); sr != nil {
					(*f)(sr.NTPTime, sr.RTPTime)
				}
			}
		}
	}
}

func (t *tap) end() { t.endOnce.Do(func() { close(t.done) }) }

func (t *tap) close() {
	t.dt.Close()
	t.end()
}

// WriteRTP implements webrtc.TrackLocalWriter (called by the DownTrack on the
// forwarding path: must not block).
func (t *tap) WriteRTP(h *rtp.Header, payload []byte) (int, error) {
	b, err := (&rtp.Packet{Header: *h, Payload: payload}).Marshal()
	if err != nil {
		return 0, err
	}
	return t.push(b), nil
}

// Write implements webrtc.TrackLocalWriter.
func (t *tap) Write(b []byte) (int, error) { return t.push(append([]byte(nil), b...)), nil }

func (t *tap) push(b []byte) int {
	select {
	case t.ch <- b:
	default:
		t.dropped.Add(1) // recorder behind: drop rather than stall forwarding
	}
	return len(b)
}

// Read implements RTPSource.
func (t *tap) Read(b []byte) (int, error) {
	select {
	case p := <-t.ch:
		return copy(b, p), nil
	case <-t.done:
		select {
		case p := <-t.ch:
			return copy(b, p), nil
		default:
			return 0, io.EOF
		}
	}
}

// Codec implements RTPSource.
func (t *tap) Codec() webrtc.RTPCodecParameters { return t.codec }

// RequestKeyFrame implements RTPSource.
func (t *tap) RequestKeyFrame() error {
	t.dt.Receiver().SendPLI(t.dt.TargetLayer().Spatial, true)
	return nil
}

// OnSenderReport implements SRSource.
func (t *tap) OnSenderReport(f func(ntp uint64, rtpTimestamp uint32)) { t.onSR.Store(&f) }

// tapContext binds a DownTrack to a tap instead of a PeerConnection.
type tapContext struct{ t *tap }

func (c tapContext) CodecParameters() []webrtc.RTPCodecParameters {
	return []webrtc.RTPCodecParameters{c.t.codec}
}
func (tapContext) HeaderExtensions() []webrtc.RTPHeaderExtensionParameter { return nil }
func (tapContext) SSRC() webrtc.SSRC                                      { return 1 }
func (tapContext) SSRCRetransmission() webrtc.SSRC                        { return 0 }
func (tapContext) SSRCForwardErrorCorrection() webrtc.SSRC                { return 0 }
func (c tapContext) WriteStream() webrtc.TrackLocalWriter                 { return c.t }
func (tapContext) ID() string                                             { return "recorder" }
func (tapContext) RTCPReader() interceptor.RTCPReader                     { return nil }

func sourceName(s tesserav1.TrackSource) string {
	switch s {
	case tesserav1.TrackSource_TRACK_SOURCE_CAMERA:
		return "camera"
	case tesserav1.TrackSource_TRACK_SOURCE_MICROPHONE:
		return "microphone"
	case tesserav1.TrackSource_TRACK_SOURCE_SCREEN_SHARE:
		return "screen_share"
	case tesserav1.TrackSource_TRACK_SOURCE_SCREEN_SHARE_AUDIO:
		return "screen_share_audio"
	}
	return ""
}
func kindName(t tesserav1.TrackType) string {
	if t == tesserav1.TrackType_TRACK_TYPE_VIDEO {
		return "video"
	}
	return "audio"
}
