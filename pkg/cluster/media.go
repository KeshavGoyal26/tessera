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
	refreshRoomsEvery = 10 * time.Second
)

// Rooms is what a node reports about its rooms.
type Rooms interface {
	ListRooms(ctx context.Context, names []string) ([]*tesserav1.Room, error)
	Ready() error
}

// Run publishes the node's heartbeat until ctx ends, then removes its entry. On a
// media node (rooms non-nil) it also keeps the claims of the rooms it hosts.
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
		conflicts, err := c.KeepRooms(ctx, self.ID, names)
		if err != nil && ctx.Err() == nil {
			logger.Warn("cluster: renewing room claims failed", "err", err)
		}
		for _, r := range conflicts {
			logger.Error("cluster: room is hosted on another node too", "room", r)
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
