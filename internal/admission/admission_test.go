package admission

import (
	"context"
	"testing"

	"github.com/sss/sss/internal/protocol"
)

type fixedReservations int64

func (f fixedReservations) ReservedBytes(context.Context) (int64, error) { return int64(f), nil }

func TestAdmitRespectsMaxTransferBytes(t *testing.T) {
	c := New(t.TempDir(), 99, 1024, fixedReservations(0))
	if err := c.Admit(context.Background(), 512); err != nil {
		t.Fatalf("512 bytes rejected under a 1024 byte cap: %v", err)
	}
	err := c.Admit(context.Background(), 2048)
	if err == nil || err.Code != protocol.ErrPayloadTooLarge {
		t.Fatalf("err = %v, want PAYLOAD_TOO_LARGE", err)
	}
}

func TestAdmitRespectsWatermark(t *testing.T) {
	dir := t.TempDir()
	// A 50% watermark on a filesystem that is already fuller than that must
	// refuse; on an emptier one a huge transfer must still be refused.
	c := New(dir, 50, 0, fixedReservations(0))
	usage, err := c.Usage()
	if err != nil {
		t.Skipf("filesystem statistics unavailable: %v", err)
	}
	huge := int64(usage.TotalBytes) // asking for the whole volume
	perr := c.Admit(context.Background(), huge)
	if perr == nil || perr.Code != protocol.ErrInsufficientStorage {
		t.Fatalf("err = %v, want INSUFFICIENT_STORAGE", perr)
	}
}

func TestOutstandingReservationsConsumeHeadroom(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, 99, 0, fixedReservations(0))
	usage, err := c.Usage()
	if err != nil {
		t.Skipf("filesystem statistics unavailable: %v", err)
	}
	// Reserve everything the watermark allows; the next transfer must be denied
	// even though the filesystem itself still reports free space.
	reserved := int64(usage.TotalBytes)
	loaded := New(dir, 99, 0, fixedReservations(reserved))
	if perr := loaded.Admit(context.Background(), 1); perr == nil || perr.Code != protocol.ErrInsufficientStorage {
		t.Fatalf("err = %v, want INSUFFICIENT_STORAGE", perr)
	}
	if perr := c.Admit(context.Background(), 1); perr != nil {
		t.Fatalf("small transfer rejected without reservations: %v", perr)
	}
}

func TestReserveIncludesMaterializationOverhead(t *testing.T) {
	if got := Reserve(1000); got != 1000*MaterializationOverhead {
		t.Errorf("Reserve(1000) = %d, want %d", got, 1000*MaterializationOverhead)
	}
}
