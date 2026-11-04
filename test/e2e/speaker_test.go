package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/client"
	"github.com/KeshavGoyal26/tessera/pkg/room"
)

// levelTrack stamps the RFC 6464 audio level on every packet.
type levelTrack struct {
	*webrtc.TrackLocalStaticRTP
	level byte
}

func (t *levelTrack) Bind(ctx webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	return t.TrackLocalStaticRTP.Bind(&levelCtx{TrackLocalContext: ctx, t: t})
}

type levelCtx struct {
	webrtc.TrackLocalContext
	t *levelTrack
}

func (c *levelCtx) WriteStream() webrtc.TrackLocalWriter {
	w := &levelWriter{TrackLocalWriter: c.TrackLocalContext.WriteStream(), t: c.t}
	for _, e := range c.HeaderExtensions() {
		if e.URI == "urn:ietf:params:rtp-hdrext:ssrc-audio-level" {
			w.id = uint8(e.ID)
		}
	}
	return w
}

type levelWriter struct {
	webrtc.TrackLocalWriter
	t  *levelTrack
	id uint8
}

func (w *levelWriter) WriteRTP(h *rtp.Header, p []byte) (int, error) {
	if w.id != 0 {
		_ = h.SetExtension(w.id, []byte{0x80 | w.t.level})
	}
	return w.TrackLocalWriter.WriteRTP(h, p)
}

func TestActiveSpeakerAndConnectionQuality(t *testing.T) {
	oldQ := room.QualityInterval
	room.QualityInterval = 300 * time.Millisecond
	t.Cleanup(func() { room.QualityInterval = oldQ })
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	speakerC := connect(t, s, "spk", "talker")
	listener := connect(t, s, "spk", "listener")
	speakers := make(chan []*tesserav1.SpeakerInfo, 16)
	quality := make(chan *tesserav1.ConnectionQualityUpdate, 16)
	listener.SetCallbacks(client.Callbacks{
		OnSpeakersChanged:   func(s []*tesserav1.SpeakerInfo) { speakers <- s },
		OnConnectionQuality: func(u *tesserav1.ConnectionQualityUpdate) { quality <- u },
	})
	syn := newSynth(t, "audio", "talker-a")
	lt := &levelTrack{TrackLocalStaticRTP: syn.track, level: 20}
	syn.track = lt.TrackLocalStaticRTP
	if _, err := speakerC.PublishTrack(ctx, lt, client.PublishOptions{Name: "mic"}); err != nil {
		t.Fatal(err)
	}
	syn.run()
	t.Cleanup(syn.Stop)
	select {
	case sp := <-speakers:
		if len(sp) == 0 || sp[0].ParticipantSid != speakerC.LocalParticipant().Sid || !sp[0].Active {
			t.Fatalf("speakers %v", sp)
		}
	case <-ctx.Done():
		t.Fatal("no speakers_changed")
	}
	select {
	case u := <-quality:
		if len(u.Updates) == 0 {
			t.Fatal("empty quality update")
		}
	case <-ctx.Done():
		t.Fatal("no connection_quality")
	}
	snap := s.Manager.Snapshot()
	if len(snap.Rooms[0].ActiveSpeakers) != 1 || snap.Rooms[0].ActiveSpeakers[0].Identity != "talker" {
		t.Fatalf("stats speakers %+v", snap.Rooms[0].ActiveSpeakers)
	}
}
