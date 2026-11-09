package auth

import (
	"testing"

	"google.golang.org/protobuf/proto"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
)

// TestWithPermissionRoundTrip: a token re-signed from the participant's current
// permission (after an admin changed it) gives back that permission.
func TestWithPermissionRoundTrip(t *testing.T) {
	g := RoomGrant{Room: "r", RoomJoin: true}
	perm := g.ToPermission()
	perm.CanPublish, perm.CanUpdateMetadata = false, true
	perm.CanPublishSources = []tesserav1.TrackSource{tesserav1.TrackSource_TRACK_SOURCE_SCREEN_SHARE}
	back := g.WithPermission(perm)
	if back.Room != "r" || !back.RoomJoin || !proto.Equal(back.ToPermission(), perm) {
		t.Fatalf("%+v -> %v, want %v", back, back.ToPermission(), perm)
	}
}
