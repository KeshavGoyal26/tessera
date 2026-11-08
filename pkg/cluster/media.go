package cluster

import (
	"context"
	"log/slog"
	"time"

	tesserav1 "github.com/KeshavGoyal26/tessera/gen/go/tessera/v1"
)

// heartbeatEvery is how often nodes publish their entry; refreshRoomsEvery how often a
// media node renews the claims of the rooms it hosts.
const (
	heartbeatEvery    = 2 * time.Second
	refreshRoomsEvery = 5 * time.Second
)

// Rooms is what a node reports about its rooms.
type Rooms interface {
	ListRooms(ctx context.Context, names []string) ([]*tesserav1.Room, error)
	ListParticipants(ctx context.Context, room string) ([]*tesserav1.ParticipantInfo, error)
	Ready() error
}

// Run publishes the node's heartbeat until ctx ends, then removes its entry. On a
// media node (rooms non-nil) it also records the rooms it hosts and renews its
// presenters' placement.
func (c *Client) Run(ctx context.Context, self NodeInfo, rooms Rooms, logger *slog.Logger) {
	hb := time.NewTicker(heartbeatEvery)
	defer hb.Stop()
	refresh := time.NewTicker(refreshRoomsEvery)
	defer refresh.Stop()
	beat := func() []string {
		n := self
		var names []string
		if rooms != nil {
			list, _ := rooms.ListRooms(ctx, nil)
			for _, r := range list {
				names = append(names, r.GetName())
				n.Sessions += int(r.GetNumParticipants())
			}
			n.Rooms = len(list)
			n.Draining = rooms.Ready() != nil
		}
		if err := c.Heartbeat(ctx, n); err != nil && ctx.Err() == nil {
			logger.Warn("cluster heartbeat failed", "err", err)
		}
		return names
	}
	keep := func(names []string) {
		if rooms == nil {
			return
		}
		presenters := map[string][]string{}
		for _, r := range names {
			ps, _ := rooms.ListParticipants(ctx, r)
			ids := []string{}
			for _, p := range ps {
				if p.GetKind() == tesserav1.ParticipantInfo_KIND_STANDARD {
					ids = append(ids, p.GetIdentity())
				}
			}
			presenters[r] = ids
		}
		if err := c.KeepRooms(ctx, self.ID, presenters); err != nil && ctx.Err() == nil {
			logger.Warn("cluster: renewing room and presenter placement failed", "err", err)
		}
	}
	keep(beat())
	for {
		select {
		case <-ctx.Done():
			ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = c.Leave(ctx2, self.ID)
			cancel()
			return
		case <-hb.C:
			beat()
		case <-refresh.C:
			keep(beat())
		}
	}
}
