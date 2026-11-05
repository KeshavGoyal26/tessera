package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/auth"
)

// devTokenPath serves join tokens for the web demo. It is registered only when
// the server runs with --dev (never from a config file alone).
const devTokenPath = "/dev/token"

// devTokenHandler answers GET /dev/token?room=X&identity=Y[&name=Z][&record=1][&role=presenter|viewer] with
// {"token": "..."} signed by the given key. It sends permissive CORS headers so
// the demo dev server on another port can call it. Dev only: it hands out
// tokens to anyone who can reach the port.
func devTokenHandler(apiKey, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		room := strings.TrimSpace(q.Get("room"))
		identity := strings.TrimSpace(q.Get("identity"))
		if room == "" || identity == "" {
			http.Error(w, "room and identity are required", http.StatusBadRequest)
			return
		}
		record := q.Get("record") == "1"
		grant := &auth.RoomGrant{Room: room, RoomJoin: true}
		attrs := map[string]string{}
		switch role := q.Get("role"); role {
		case "", "participant":
		case "presenter":
			// Publishes only; never receives anyone's media.
			grant.CanSubscribe = auth.Bool(false)
			attrs["role"] = role
		case "viewer":
			// Receives only, and is invisible to presenters and other viewers.
			grant.CanPublish, grant.CanPublishData, grant.Hidden = auth.Bool(false), auth.Bool(false), true
			attrs["role"] = role
			record = false // nothing to record
		default:
			http.Error(w, "role must be presenter, viewer or participant", http.StatusBadRequest)
			return
		}
		if record {
			grant.AutoRecord = true
			attrs["recorded"] = "true" // lets every client show that this participant is being recorded
		}
		at := auth.NewAccessToken(apiKey, secret).SetIdentity(identity).SetName(q.Get("name")).
			SetGrant(grant).SetValidFor(6 * time.Hour)
		if len(attrs) > 0 {
			at.SetAttributes(attrs)
		}
		tok, err := at.ToJWT()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": tok})
	})
}
