// Package version holds build and protocol version information.
// Version and Commit are set at build time with -ldflags "-X".
package version

// Build information, overridden by the linker.
var (
	Version = "0.0.0-dev"
	Commit  = "unknown"
)

// Signaling protocol versions supported by this build (see docs/PROTOCOL.md).
const (
	ProtocolVersion    = 1
	MinProtocolVersion = 1
)
