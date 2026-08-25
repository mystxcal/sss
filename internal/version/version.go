// Package version exposes build metadata embedded at link time.
package version

// Values are overridden with -ldflags -X at release build time.
var (
	Version   = "1.0.0-dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// Protocol is the public contract version reported by /v1/info as major.minor.
// Major changes are incompatible; minor changes add backward-compatible capability.
const Protocol = "1.0"

// ProtocolMajor is the major component clients must match.
const ProtocolMajor = 1

// ManifestSchemaVersion is the only manifest schema this release validates.
const ManifestSchemaVersion = 1

// UserAgent identifies this build in outbound requests.
func UserAgent() string { return "sss/" + Version }
