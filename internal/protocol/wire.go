package protocol

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TTL bounds are frozen product decisions.
const (
	DefaultTTLMinutes = 30
	MinTTLMinutes     = 1
	MaxTTLMinutes     = 3000
)

// ValidateTTL checks a minute value against the frozen bounds.
func ValidateTTL(minutes int) *Error {
	if minutes < MinTTLMinutes || minutes > MaxTTLMinutes {
		return Errorf(ErrTTLOutOfRange, "ttl must be between %d and %d minutes", MinTTLMinutes, MaxTTLMinutes)
	}
	return nil
}

// ParseTTL accepts a bare minute count ("120") or a Go-style duration ("2h",
// "30m") and normalizes it to whole minutes within the frozen bounds.
func ParseTTL(s string) (int, *Error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultTTLMinutes, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		if e := ValidateTTL(n); e != nil {
			return 0, e
		}
		return n, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, Errorf(ErrInvalidRequest, "invalid duration %q", s)
	}
	if d <= 0 {
		return 0, Errorf(ErrTTLOutOfRange, "ttl must be positive")
	}
	if d%time.Minute != 0 {
		return 0, Errorf(ErrInvalidRequest, "ttl %q must be a whole number of minutes", s)
	}
	minutes := int(d / time.Minute)
	if e := ValidateTTL(minutes); e != nil {
		return 0, e
	}
	return minutes, nil
}

// Info is the capability document returned by GET /v1/info.
type Info struct {
	ApplicationVersion string         `json:"application_version"`
	ProtocolVersion    string         `json:"protocol_version"`
	DefaultTTLMinutes  int            `json:"default_ttl_minutes"`
	MaxTTLMinutes      int            `json:"max_ttl_minutes"`
	Capabilities       []string       `json:"capabilities"`
	Limits             map[string]any `json:"limits,omitempty"`
}

// SegmentPlan is one declared segment in an advanced transfer creation request.
type SegmentPlan struct {
	ID              string  `json:"id"`
	Kind            string  `json:"kind"`
	ExpectedLength  int64   `json:"expected_length"`
	DigestAlgorithm string  `json:"digest_algorithm"`
	ExpectedDigest  *string `json:"expected_digest,omitempty"`
}

// CreateTransferRequest opens an uncommitted resumable transfer.
type CreateTransferRequest struct {
	ClientTransferID          string        `json:"client_transfer_id,omitempty"`
	TTLMinutes                int           `json:"ttl_minutes"`
	Note                      string        `json:"note,omitempty"`
	ExpectedMaterializedBytes int64         `json:"expected_materialized_bytes,omitempty"`
	ExpectedFileCount         int           `json:"expected_file_count,omitempty"`
	Segments                  []SegmentPlan `json:"segments"`
}

// UploadResource tells a client where to push one segment.
type UploadResource struct {
	SegmentID  string `json:"segment_id"`
	UploadID   string `json:"upload_id"`
	UploadPath string `json:"upload_path"`
}

// CreateTransferResponse is returned by POST /v1/transfers.
type CreateTransferResponse struct {
	TransferID string           `json:"transfer_id"`
	Uploads    []UploadResource `json:"uploads"`
}

// CommitRequest carries the final manifest.
type CommitRequest struct {
	Manifest Manifest `json:"manifest"`
}

// CommitResponse is the published result of a commit.
type CommitResponse struct {
	Code        string    `json:"code"`
	CommittedAt time.Time `json:"committed_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// EntrySummary is the public listing form of a manifest entry.
type EntrySummary struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
}

// TransferMetadata is returned by GET /v1/transfers/{code}.
type TransferMetadata struct {
	Code        string         `json:"code"`
	Note        string         `json:"note,omitempty"`
	CommittedAt time.Time      `json:"committed_at"`
	ExpiresAt   time.Time      `json:"expires_at"`
	FileCount   int            `json:"file_count"`
	TotalBytes  int64          `json:"total_bytes"`
	Entries     []EntrySummary `json:"entries"`
}

// ClaimRequest opens a bounded receive session.
type ClaimRequest struct {
	Code string `json:"code"`
}

// ClaimSegment describes one downloadable immutable segment.
//
// Digest is the BLAKE3 digest of the segment and, identically, the root of its
// Bao verification tree. A receiver that fetches OutboardPath can therefore
// verify any group of the segment against Digest on its own, without the
// neighbouring bytes. OutboardPath is empty for transfers committed before
// verification trees existed, and receivers fall back to whole-segment hashing.
type ClaimSegment struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Length         int64  `json:"length"`
	Digest         string `json:"digest"`
	Path           string `json:"path"`
	OutboardPath   string `json:"outboard_path,omitempty"`
	OutboardLength int64  `json:"outboard_length,omitempty"`
}

// ClaimResponse is the receive session document.
type ClaimResponse struct {
	ClaimID    string         `json:"claim_id"`
	Token      string         `json:"token"`
	LeaseUntil time.Time      `json:"lease_until"`
	Manifest   Manifest       `json:"manifest"`
	Segments   []ClaimSegment `json:"segments"`
}

// LocalClaim is the JSON form of a VPS-local receipt.
type LocalClaim struct {
	OK         bool      `json:"ok"`
	Code       string    `json:"code"`
	Path       string    `json:"path"`
	ReadOnly   bool      `json:"read_only"`
	LeaseUntil time.Time `json:"lease_until"`
}

// AdminStatus is the operational snapshot served on the local socket.
type AdminStatus struct {
	Version          string         `json:"version"`
	ProtocolVersion  string         `json:"protocol_version"`
	Ready            bool           `json:"ready"`
	UptimeSeconds    int64          `json:"uptime_seconds"`
	Committed        int            `json:"committed_transfers"`
	Staging          int            `json:"staging_transfers"`
	ActiveClaims     int            `json:"active_claims"`
	DiskTotalBytes   uint64         `json:"disk_total_bytes"`
	DiskFreeBytes    uint64         `json:"disk_free_bytes"`
	DiskUsedPercent  int            `json:"disk_used_percent"`
	ReservedBytes    int64          `json:"reserved_bytes"`
	AdmissionOK      bool           `json:"admission_ok"`
	HighWatermarkPct int            `json:"disk_high_watermark_percent"`
	StorageDir       string         `json:"storage_dir"`
	Limits           map[string]any `json:"limits,omitempty"`
}

// CleanupResult reports what an on-demand janitor pass did.
type CleanupResult struct {
	Expired        int `json:"expired"`
	Deleted        int `json:"deleted"`
	StagingCleaned int `json:"staging_cleaned"`
	TrashEmptied   int `json:"trash_emptied"`
}

// CheckCompatible verifies a server's protocol version against this build.
func CheckCompatible(serverProtocol string) *Error {
	major := serverProtocol
	if i := strings.IndexByte(serverProtocol, '.'); i >= 0 {
		major = serverProtocol[:i]
	}
	n, err := strconv.Atoi(major)
	if err != nil {
		return Errorf(ErrProtocolMismatch, "server reported unparsable protocol version %q", serverProtocol)
	}
	if n != 1 {
		return Errorf(ErrProtocolMismatch, "server protocol %s is not compatible with this client (expects 1.x)", serverProtocol)
	}
	return nil
}

// HumanBytes renders a byte count for human-facing stderr output.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
