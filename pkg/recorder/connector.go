package recorder

import (
	"context"
	"log/slog"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/client"
)

// ClientConnector joins rooms with pkg/client as a hidden recorder
// participant (ADR-0007). It is the production Connector.
type ClientConnector struct {
	Logger *slog.Logger
}

type clientRoom struct{ c client.Client }

func (r clientRoom) Close(ctx context.Context) error { return r.c.Disconnect(ctx) }

// Join implements Connector.
func (cc ClientConnector) Join(ctx context.Context, wsURL, token string, onTrack func(Track), onClosed func(error)) (Room, error) {
	c := client.New(client.Options{AutoSubscribe: true, SDK: tesserav1.ClientInfo_SDK_RECORDER, Logger: cc.Logger})
	c.SetCallbacks(client.Callbacks{
		OnTrackSubscribed: func(t *client.RemoteTrack) {
			kind := "audio"
			if t.Info.GetType() == tesserav1.TrackType_TRACK_TYPE_VIDEO {
				kind = "video"
			}
			onTrack(Track{SID: t.SID, ParticipantIdentity: t.ParticipantIdentity, Kind: kind, Source: sourceName(t.Info.GetSource()), RTP: t})
		},
		OnDisconnected: func(tesserav1.DisconnectReason) { onClosed(nil) },
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
