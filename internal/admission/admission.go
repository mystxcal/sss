// Package admission decides whether the server has room to accept more bytes.
// It combines real filesystem free space with outstanding reservations so that
// several concurrent uploads cannot collectively overcommit the disk.
package admission

import (
	"context"

	"github.com/sss/sss/internal/platform"
	"github.com/sss/sss/internal/protocol"
)

// Reservations reports the currently outstanding pre-commit reservation total.
type Reservations interface {
	ReservedBytes(ctx context.Context) (int64, error)
}

// Controller enforces the configured disk high-watermark and size limits.
type Controller struct {
	dataDir          string
	highWatermarkPct int
	maxTransferBytes int64
	reservations     Reservations
}

// MaterializationOverhead accounts for packed bytes that exist both as a pack
// segment and as extracted payload before the transfer is complete.
const MaterializationOverhead = 2

// New builds a controller for a data directory.
func New(dataDir string, highWatermarkPct int, maxTransferBytes int64, r Reservations) *Controller {
	return &Controller{
		dataDir:          dataDir,
		highWatermarkPct: highWatermarkPct,
		maxTransferBytes: maxTransferBytes,
		reservations:     r,
	}
}

// Usage reports current filesystem capacity.
func (c *Controller) Usage() (platform.DiskUsage, error) { return platform.Disk(c.dataDir) }

// Status reports whether the server is currently accepting new transfers.
func (c *Controller) Status(ctx context.Context) (platform.DiskUsage, int64, bool) {
	usage, err := c.Usage()
	if err != nil {
		return platform.DiskUsage{}, 0, false
	}
	reserved, err := c.reservations.ReservedBytes(ctx)
	if err != nil {
		return usage, 0, false
	}
	return usage, reserved, c.headroom(usage, reserved) > 0
}

// headroom returns the bytes still admissible before the high watermark.
func (c *Controller) headroom(usage platform.DiskUsage, reserved int64) int64 {
	if usage.TotalBytes == 0 {
		return 0
	}
	// Bytes that may be consumed before crossing the watermark.
	limit := int64(usage.TotalBytes) * int64(c.highWatermarkPct) / 100
	used := int64(usage.TotalBytes - usage.FreeBytes)
	return limit - used - reserved
}

// Admit checks whether expectedBytes may be accepted. expectedBytes may be zero
// for streaming uploads of unknown length, which are admitted only while the
// server is below the watermark.
func (c *Controller) Admit(ctx context.Context, expectedBytes int64) *protocol.Error {
	if c.maxTransferBytes > 0 && expectedBytes > c.maxTransferBytes {
		return protocol.Errorf(protocol.ErrPayloadTooLarge,
			"transfer of %d bytes exceeds the configured maximum of %d bytes", expectedBytes, c.maxTransferBytes)
	}
	usage, err := c.Usage()
	if err != nil {
		return protocol.Errorf(protocol.ErrInternal, "cannot read filesystem capacity")
	}
	reserved, rerr := c.reservations.ReservedBytes(ctx)
	if rerr != nil {
		return protocol.Errorf(protocol.ErrInternal, "cannot read outstanding reservations")
	}
	headroom := c.headroom(usage, reserved)
	if headroom <= 0 {
		return protocol.Errorf(protocol.ErrInsufficientStorage,
			"server storage is above the %d%% high-watermark", c.highWatermarkPct)
	}
	if expectedBytes > 0 && expectedBytes*MaterializationOverhead > headroom {
		return protocol.Errorf(protocol.ErrInsufficientStorage,
			"not enough storage headroom for a %d byte transfer", expectedBytes)
	}
	return nil
}

// CheckStreaming is the per-chunk guard for uploads of unknown length. It is
// cheap enough to call every few megabytes.
func (c *Controller) CheckStreaming(ctx context.Context) *protocol.Error {
	usage, err := c.Usage()
	if err != nil {
		return protocol.Errorf(protocol.ErrInternal, "cannot read filesystem capacity")
	}
	if c.headroom(usage, 0) <= 0 {
		return protocol.Errorf(protocol.ErrInsufficientStorage,
			"server storage is above the %d%% high-watermark", c.highWatermarkPct)
	}
	return nil
}

// Reserve returns the bytes a transfer should reserve for the given declared
// size, including materialization overhead.
func Reserve(expectedBytes int64) int64 { return expectedBytes * MaterializationOverhead }

// MaxTransferBytes reports the configured hard cap (0 means disk-bound only).
func (c *Controller) MaxTransferBytes() int64 { return c.maxTransferBytes }

// HighWatermarkPercent reports the configured watermark.
func (c *Controller) HighWatermarkPercent() int { return c.highWatermarkPct }
