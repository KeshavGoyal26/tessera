package stats

import (
	"encoding/json"
	"net/http"
)

// Path is where Handler is mounted on the operational listener.
const Path = "/stats"

// Handler serves the provider's snapshot as JSON. Query parameter
// room=<name> limits the output to one room.
func Handler(p Provider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := p.Snapshot()
		if name := r.URL.Query().Get("room"); name != "" {
			rooms := s.Rooms[:0]
			for _, rm := range s.Rooms {
				if rm.Name == name {
					rooms = append(rooms, rm)
				}
			}
			s.Rooms = rooms
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(s)
	})
}
