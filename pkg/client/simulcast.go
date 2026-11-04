package client

import (
	"sync/atomic"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

const (
	uriMID = "urn:ietf:params:rtp-hdrext:sdes:mid"
	uriRID = "urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id"
)

// ridTrack wraps a simulcast layer so every packet carries the MID and RID
// header extensions the SFU needs to demultiplex layers (Pion does not add
// them when sending).
type ridTrack struct {
	webrtc.TrackLocal
	mid atomic.Pointer[string]
}

func (t *ridTrack) Bind(ctx webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	return t.TrackLocal.Bind(&ridContext{TrackLocalContext: ctx, t: t})
}

type ridContext struct {
	webrtc.TrackLocalContext
	t *ridTrack
}

func (c *ridContext) WriteStream() webrtc.TrackLocalWriter {
	w := &ridWriter{TrackLocalWriter: c.TrackLocalContext.WriteStream(), rid: []byte(c.t.RID()), t: c.t}
	for _, e := range c.HeaderExtensions() {
		switch e.URI {
		case uriMID:
			w.midID = uint8(e.ID)
		case uriRID:
			w.ridID = uint8(e.ID)
		}
	}
	return w
}

type ridWriter struct {
	webrtc.TrackLocalWriter
	t            *ridTrack
	midID, ridID uint8
	rid          []byte
}

func (w *ridWriter) WriteRTP(h *rtp.Header, payload []byte) (int, error) {
	if w.midID != 0 {
		if mid := w.t.mid.Load(); mid != nil {
			_ = h.SetExtension(w.midID, []byte(*mid))
		}
	}
	if w.ridID != 0 && len(w.rid) > 0 {
		_ = h.SetExtension(w.ridID, w.rid)
	}
	return w.TrackLocalWriter.WriteRTP(h, payload)
}

func (w *ridWriter) Write(b []byte) (int, error) {
	var p rtp.Packet
	if err := p.Unmarshal(b); err != nil {
		return 0, err
	}
	return w.WriteRTP(&p.Header, p.Payload)
}
