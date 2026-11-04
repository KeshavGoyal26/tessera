package e2e

import (
	"context"
	"testing"
	"time"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
	"github.com/KeshavGoyal26/tessera/pkg/client"
)

func TestDataPackets(t *testing.T) {
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	alice := connect(t, s, "data", "alice")
	bob := connect(t, s, "data", "bob")
	carol := connect(t, s, "data", "carol")
	got := map[string]chan *tesserav1.DataPacket{"bob": make(chan *tesserav1.DataPacket, 4), "carol": make(chan *tesserav1.DataPacket, 4)}
	bob.SetCallbacks(client.Callbacks{OnDataPacket: func(p *tesserav1.DataPacket) { got["bob"] <- p }})
	carol.SetCallbacks(client.Callbacks{OnDataPacket: func(p *tesserav1.DataPacket) { got["carol"] <- p }})
	time.Sleep(300 * time.Millisecond) // let subscriber data channels open
	if err := alice.PublishData(ctx, &tesserav1.DataPacket{Kind: tesserav1.DataPacket_KIND_RELIABLE, Topic: "chat", Payload: []byte("hi all")}); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"bob", "carol"} {
		select {
		case p := <-got[who]:
			if p.ParticipantIdentity != "alice" || string(p.Payload) != "hi all" || p.Topic != "chat" {
				t.Fatalf("%s got %v", who, p)
			}
		case <-ctx.Done():
			t.Fatalf("%s got nothing", who)
		}
	}
	// Targeted packet reaches only carol.
	if err := alice.PublishData(ctx, &tesserav1.DataPacket{Kind: tesserav1.DataPacket_KIND_LOSSY, Payload: []byte("psst"), DestinationIdentities: []string{"carol"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-got["carol"]:
		if string(p.Payload) != "psst" {
			t.Fatal(p)
		}
	case <-ctx.Done():
		t.Fatal("carol got nothing")
	}
	select {
	case p := <-got["bob"]:
		t.Fatalf("bob got targeted packet %v", p)
	case <-time.After(300 * time.Millisecond):
	}
}
