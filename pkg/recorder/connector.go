package recorder

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/pion/webrtc/v4"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/client"
)

// ClientConnector joins rooms with pkg/client as a hidden recorder
// participant (ADR-0007). It is the production Connector.
type ClientConnector struct {
	Logger *slog.Logger
	// SettingEngine customises Pion (loopback ICE in tests); nil = defaults.
	SettingEngine *webrtc.SettingEngine
}

type clientRoom struct{ c client.Client }

func (r clientRoom) Close(ctx context.Context) error { return r.c.Disconnect(ctx) }

// Join implements Connector. Only tracks that want accepts are subscribed: a recording
// joins as its own participant, and subscribing to the whole room made every
// recording receive (and the server send) every participant's media, which grew
// with the square of the room size.
func (cc ClientConnector) Join(ctx context.Context, wsURL, token string, want func(Track) bool, onTrack func(Track), onClosed func(error)) (Room, error) {
	c := client.New(client.Options{AutoSubscribe: false, SDK: tesserav1.ClientInfo_SDK_RECORDER, SettingEngine: cc.SettingEngine, Logger: cc.Logger})
	var mu sync.Mutex
	asked := map[string]bool{}
	subscribe := func(ps []*tesserav1.ParticipantInfo) {
		var sids []string
		mu.Lock()
		for _, p := range ps {
			for _, t := range p.GetTracks() {
				tr := Track{SID: t.GetSid(), ParticipantIdentity: p.GetIdentity(), Kind: kindName(t.GetType()), Source: sourceName(t.GetSource())}
				if !asked[tr.SID] && want(tr) {
					asked[tr.SID] = true
					sids = append(sids, tr.SID)
				}
			}
		}
		mu.Unlock()
		if len(sids) == 0 {
			return
		}
		if err := c.Subscribe(ctx, sids...); err != nil { // not connected yet: retried after Connect
			mu.Lock()
			for _, sid := range sids {
				delete(asked, sid)
			}
			mu.Unlock()
		}
	}
	c.SetCallbacks(client.Callbacks{
		OnParticipantUpdate: subscribe,
		OnTrackSubscribed: func(t *client.RemoteTrack) {
			onTrack(Track{SID: t.SID, ParticipantIdentity: t.ParticipantIdentity, Kind: kindName(t.Info.GetType()),
				Source: sourceName(t.Info.GetSource()), RTP: t})
		},
		OnDisconnected: func(reason tesserav1.DisconnectReason) { onClosed(disconnectErr(reason)) },
	})
	if err := c.Connect(ctx, wsURL, token); err != nil {
		return nil, err
	}
	subscribe(c.RemoteParticipants()) // tracks already in the room
	return clientRoom{c}, nil
}

func kindName(t tesserav1.TrackType) string {
	if t == tesserav1.TrackType_TRACK_TYPE_VIDEO {
		return "video"
	}
	return "audio"
}

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

// disconnectErr is nil for an orderly end (left, removed, room deleted, server shutdown) and an
// error when the connection was lost, so a cut-short recording is reported as failed, not complete.
func disconnectErr(reason tesserav1.DisconnectReason) error {
	switch reason {
	case tesserav1.DisconnectReason_DISCONNECT_REASON_SIGNAL_TIMEOUT, tesserav1.DisconnectReason_DISCONNECT_REASON_MEDIA_FAILURE,
		tesserav1.DisconnectReason_DISCONNECT_REASON_STATE_MISMATCH, tesserav1.DisconnectReason_DISCONNECT_REASON_JOIN_FAILURE:
		return fmt.Errorf("connection lost (%s)", reason)
	}
	return nil
}
