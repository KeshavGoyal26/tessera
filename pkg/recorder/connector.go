package recorder

import (
	"context"
	"fmt"
	"log/slog"

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

// Join implements Connector.
func (cc ClientConnector) Join(ctx context.Context, wsURL, token string, onTrack func(Track), onClosed func(error)) (Room, error) {
	c := client.New(client.Options{AutoSubscribe: true, SDK: tesserav1.ClientInfo_SDK_RECORDER, SettingEngine: cc.SettingEngine, Logger: cc.Logger})
	c.SetCallbacks(client.Callbacks{
		OnTrackSubscribed: func(t *client.RemoteTrack) {
			kind := "audio"
			if t.Info.GetType() == tesserav1.TrackType_TRACK_TYPE_VIDEO {
				kind = "video"
			}
			w, h := t.Info.GetWidth(), t.Info.GetHeight()
			for _, l := range t.Info.GetLayers() {
				if l.GetWidth() > w {
					w, h = l.GetWidth(), l.GetHeight()
				}
			}
			onTrack(Track{SID: t.SID, ParticipantIdentity: t.ParticipantIdentity, Kind: kind, Source: sourceName(t.Info.GetSource()),
				Width: w, Height: h, RTP: t})
		},
		OnDisconnected: func(reason tesserav1.DisconnectReason) { onClosed(disconnectErr(reason)) },
	})
	if err := c.Connect(ctx, wsURL, token); err != nil {
		return nil, err
	}
	return clientRoom{c}, nil
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
