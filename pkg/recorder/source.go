package recorder

import "github.com/pion/webrtc/v4"

// RTPSource decouples the recording pipeline from pkg/client so pipelines
// can be tested with synthetic and fixture RTP before the client exists.
// *client.RemoteTrack satisfies it.
type RTPSource interface {
	// Read reads one RTP packet into b.
	Read(b []byte) (n int, err error)
	Codec() webrtc.RTPCodecParameters
	// RequestKeyFrame asks the publisher for a keyframe (PLI via the SFU).
	RequestKeyFrame() error
}
