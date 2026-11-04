package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/pion/rtp"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/client"
)

// receive reads n packets from a remote track and checks payload integrity,
// SN continuity and timestamp monotonicity.
func receive(t *testing.T, rt *client.RemoteTrack, video bool, n int) {
	t.Helper()
	buf := make([]byte, 1500)
	var last rtp.Header
	done := make(chan error, 1)
	go func() {
		for i := 0; i < n; i++ {
			k, err := rt.Read(buf)
			if err != nil {
				done <- err
				return
			}
			var p rtp.Packet
			if err := p.Unmarshal(buf[:k]); err != nil {
				done <- err
				return
			}
			if _, ok := checkPayload(t, video, p.Payload); !ok {
				t.Errorf("payload corrupted: %x", p.Payload)
			}
			if i > 0 {
				if p.SequenceNumber != last.SequenceNumber+1 {
					t.Errorf("SN gap %d -> %d", last.SequenceNumber, p.SequenceNumber)
				}
				if int32(p.Timestamp-last.Timestamp) < 0 {
					t.Errorf("TS went back")
				}
			}
			last = p.Header
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("timeout receiving %s", rt.SID)
	}
}

func TestTwoParticipantsExchangeMedia(t *testing.T) {
	s := startServer(t)
	alice := connect(t, s, "r1", "alice")
	bob := connect(t, s, "r1", "bob")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	type pub struct {
		c     client.Client
		audio *tesserav1.TrackInfo
		video *tesserav1.TrackInfo
	}
	var pubs []pub
	for _, c := range []client.Client{alice, bob} {
		id := c.LocalParticipant().Identity
		a, v := newSynth(t, "audio", id+"-a"), newSynth(t, "video", id+"-v")
		ai, err := c.PublishTrack(ctx, a.track, client.PublishOptions{Name: "mic"})
		if err != nil {
			t.Fatal(err)
		}
		vi, err := c.PublishTrack(ctx, v.track, client.PublishOptions{Name: "cam", Width: 640, Height: 360})
		if err != nil {
			t.Fatal(err)
		}
		a.run()
		v.run()
		t.Cleanup(a.Stop)
		t.Cleanup(v.Stop)
		pubs = append(pubs, pub{c, ai, vi})
	}
	for i, p := range pubs {
		other := pubs[1-i].c
		for _, ti := range []*tesserav1.TrackInfo{p.audio, p.video} {
			rt, err := client.WaitTrack(ctx, other, ti.Sid)
			if err != nil {
				t.Fatalf("%s never got %s: %v", other.LocalParticipant().Identity, ti.Sid, err)
			}
			receive(t, rt, ti.Type == tesserav1.TrackType_TRACK_TYPE_VIDEO, 50)
		}
	}
	// Stats see both participants and their tracks.
	snap := s.Manager.Snapshot()
	if len(snap.Rooms) != 1 || len(snap.Rooms[0].Participants) != 2 {
		t.Fatalf("snapshot %+v", snap.Rooms)
	}
	for _, p := range snap.Rooms[0].Participants {
		if len(p.Published) != 2 || len(p.Subscribed) != 2 {
			t.Fatalf("participant stats %+v", p)
		}
	}
}
