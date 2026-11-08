package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/client"
	"github.com/KeshavGoyal26/tessera/pkg/config"
)

// With room.lock_published_tracks a client cannot mute or unpublish its own tracks
// (presenters' camera and mic stay on, ADR-0012).
func TestLockedPublishedTracks(t *testing.T) {
	s := startServer(t, func(c *config.Config) { c.Room.LockPublishedTracks = true })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := connect(t, s, "lock", "pres")
	a := newSynth(t, "audio", "mic")
	info, err := c.PublishTrack(ctx, a.track, client.PublishOptions{Name: "mic"})
	if err != nil {
		t.Fatal(err)
	}
	a.run()
	t.Cleanup(a.Stop)
	if err := c.SetTrackMuted(ctx, info.Sid, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	for _, p := range s.Manager.Snapshot().Rooms[0].Participants {
		for _, tr := range p.Published {
			if tr.SID == info.Sid && tr.Muted {
				t.Fatal("a locked track was muted by the client")
			}
		}
	}
}
