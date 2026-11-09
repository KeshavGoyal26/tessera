// Package auth implements API keys and HS256 JWT access tokens (ADR-0004).
//
// The JSON claim names in this file are a public contract shared with the
// Java and browser SDKs; changing them breaks integrators.
package auth

import (
	"strings"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
)

// RoomGrant is the "grants" claim of an access token.
type RoomGrant struct {
	// Room the grant applies to.
	Room string `json:"room,omitempty"`

	RoomJoin       bool `json:"room_join,omitempty"`
	RoomCreate     bool `json:"room_create,omitempty"`
	RoomList       bool `json:"room_list,omitempty"`
	RoomAdmin      bool `json:"room_admin,omitempty"`
	RoomRecord     bool `json:"room_record,omitempty"`
	RecorderWorker bool `json:"recorder_worker,omitempty"`

	// CanPublish, CanSubscribe and CanPublishData default to true when nil.
	CanPublish        *bool    `json:"can_publish,omitempty"`
	CanPublishSources []string `json:"can_publish_sources,omitempty"`
	CanSubscribe      *bool    `json:"can_subscribe,omitempty"`
	CanPublishData    *bool    `json:"can_publish_data,omitempty"`

	CanUpdateOwnMetadata bool `json:"can_update_own_metadata,omitempty"`
	Hidden               bool `json:"hidden,omitempty"`
	Recorder             bool `json:"recorder,omitempty"`
	// Relay marks an edge's relay session (ADR-0012): it subscribes over the private
	// network and is never paused for bandwidth. Use with Hidden.
	Relay bool `json:"relay,omitempty"`
	// AutoRecord records this participant from the moment they join until they leave.
	AutoRecord bool `json:"auto_record,omitempty"`

	// RoomConfig applies when a join auto-creates the room.
	RoomConfig *RoomConfig `json:"room_config,omitempty"`
}

// RoomConfig is the optional room configuration inside a grant.
type RoomConfig struct {
	EmptyTimeoutSeconds     uint32 `json:"empty_timeout_seconds,omitempty"`
	DepartureTimeoutSeconds uint32 `json:"departure_timeout_seconds,omitempty"`
	MaxParticipants         uint32 `json:"max_participants,omitempty"`
	RecordingEnabled        bool   `json:"recording_enabled,omitempty"`
}

// Bool returns a pointer to b, for the optional grant fields.
func Bool(b bool) *bool { return &b }

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// Source names used in can_publish_sources.
const (
	SourceCamera           = "camera"
	SourceMicrophone       = "microphone"
	SourceScreenShare      = "screen_share"
	SourceScreenShareAudio = "screen_share_audio"
)

// SourceToProto converts a grant source name to the protobuf enum.
func SourceToProto(s string) tesserav1.TrackSource {
	switch strings.ToLower(s) {
	case SourceCamera:
		return tesserav1.TrackSource_TRACK_SOURCE_CAMERA
	case SourceMicrophone:
		return tesserav1.TrackSource_TRACK_SOURCE_MICROPHONE
	case SourceScreenShare:
		return tesserav1.TrackSource_TRACK_SOURCE_SCREEN_SHARE
	case SourceScreenShareAudio:
		return tesserav1.TrackSource_TRACK_SOURCE_SCREEN_SHARE_AUDIO
	default:
		return tesserav1.TrackSource_TRACK_SOURCE_UNSPECIFIED
	}
}

// ToPermission returns the participant permission implied by the grant.
func (g *RoomGrant) ToPermission() *tesserav1.ParticipantPermission {
	p := &tesserav1.ParticipantPermission{
		CanPublish:        boolOr(g.CanPublish, true),
		CanSubscribe:      boolOr(g.CanSubscribe, true),
		CanPublishData:    boolOr(g.CanPublishData, true),
		Hidden:            g.Hidden,
		Recorder:          g.Recorder,
		Relay:             g.Relay,
		CanUpdateMetadata: g.CanUpdateOwnMetadata,
	}
	for _, s := range g.CanPublishSources {
		if src := SourceToProto(s); src != tesserav1.TrackSource_TRACK_SOURCE_UNSPECIFIED {
			p.CanPublishSources = append(p.CanPublishSources, src)
		}
	}
	return p
}

// WithPermission returns a copy of g carrying perm (the inverse of ToPermission), so a
// token re-signed after an admin changed the participant's permission keeps the change.
func (g RoomGrant) WithPermission(perm *tesserav1.ParticipantPermission) RoomGrant {
	g.CanPublish, g.CanSubscribe, g.CanPublishData = Bool(perm.GetCanPublish()), Bool(perm.GetCanSubscribe()), Bool(perm.GetCanPublishData())
	g.Hidden, g.Recorder, g.Relay, g.CanUpdateOwnMetadata = perm.GetHidden(), perm.GetRecorder(), perm.GetRelay(), perm.GetCanUpdateMetadata()
	g.CanPublishSources = nil
	for _, s := range perm.GetCanPublishSources() {
		g.CanPublishSources = append(g.CanPublishSources, strings.ToLower(strings.TrimPrefix(s.String(), "TRACK_SOURCE_")))
	}
	return g
}

// Claims are the JWT claims of an access token.
type Claims struct {
	Issuer     string            `json:"iss"`
	Subject    string            `json:"sub,omitempty"`
	NotBefore  int64             `json:"nbf,omitempty"`
	Expiry     int64             `json:"exp"`
	ID         string            `json:"jti,omitempty"`
	Name       string            `json:"name,omitempty"`
	Metadata   string            `json:"metadata,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Grants     *RoomGrant        `json:"grants,omitempty"`
}

// Identity returns the participant identity (the "sub" claim).
func (c *Claims) Identity() string { return c.Subject }

// APIKey returns the API key that signed the token (the "iss" claim).
func (c *Claims) APIKey() string { return c.Issuer }
