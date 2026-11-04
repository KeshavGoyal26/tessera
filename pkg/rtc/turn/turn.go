// Package turn runs the embedded TURN server (pion/turn) with ephemeral
// REST-style credentials (ADR-0005).
package turn

import (
	"log/slog"
	"time"

	"github.com/KeshavGoyal26/tessera/pkg/config"
	"github.com/KeshavGoyal26/tessera/pkg/errs"
)

// Server is the embedded TURN server.
type Server struct{}

// NewServer starts the TURN listeners. It returns (nil, nil) when TURN is disabled.
func NewServer(cfg config.TURNConfig, logger *slog.Logger) (*Server, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	return nil, errs.NotImplemented("turn.NewServer")
}

// Credentials returns an ephemeral username/password for a participant.
func (s *Server) Credentials(participantSID string, ttl time.Duration) (username, password string) {
	return "", ""
}

// URLs returns the ICE server URLs clients should use (turn:, turns:).
func (s *Server) URLs() []string { return nil }

// Close stops all listeners.
func (s *Server) Close() error { return nil }
