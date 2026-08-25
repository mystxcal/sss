// Package sqlite is the metadata repository. SQL details do not leak past this
// package: callers see records and narrow operations.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sss/sss/internal/protocol"
)

// Transfer states, mirroring the documented state machine.
const (
	StateCreated   = "CREATED"
	StateUploading = "UPLOADING"
	StateVerifying = "VERIFYING"
	StateCommitted = "COMMITTED"
	StateExpired   = "EXPIRED"
	StateDeleting  = "DELETING"
	StateDeleted   = "DELETED"
	StateFailed    = "FAILED"
	StateAbandoned = "ABANDONED"
)

// Segment states.
const (
	SegmentPending  = "PENDING"
	SegmentReceived = "RECEIVED"
	SegmentVerified = "VERIFIED"
)

// Claim kinds.
const (
	ClaimRemote = "remote"
	ClaimLocal  = "local"
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("record not found")

// Transfer is one row of the transfers table.
type Transfer struct {
	ID                  string
	Code                string
	State               string
	SenderLabel         string
	CreatedAt           time.Time
	CommittedAt         time.Time
	ExpiresAt           time.Time
	RequestedTTLMinutes int
	Note                string
	ManifestDigest      string
	WireBytes           int64
	MaterializedBytes   int64
	ReservedBytes       int64
	RootPath            string
	LastErrorCode       string
}

// Committed reports whether the transfer currently holds a public code.
func (t Transfer) Committed() bool { return t.State == StateCommitted && t.Code != "" }

// Segment is one row of the segments table.
type Segment struct {
	ID                  string
	TransferID          string
	UploadID            string
	Kind                string
	ExpectedLength      int64
	ReceivedLength      int64
	DigestAlgorithm     string
	ExpectedDigest      string
	State               string
	RelativeStoragePath string
}

// Claim is one row of the claims table.
type Claim struct {
	ID          string
	TransferID  string
	Kind        string
	CreatedAt   time.Time
	LeaseUntil  time.Time
	CompletedAt time.Time
	TokenHash   string
}

// Idempotency is one recorded idempotent operation.
type Idempotency struct {
	KeyHash            string
	Operation          string
	TransferID         string
	ResponseCode       string
	CreatedAt          time.Time
	ExpiresAt          time.Time
	RequestFingerprint string
}

// Store is the metadata repository.
type Store struct {
	db *sql.DB
	// writeMu serializes writers. SQLite allows one writer at a time; taking
	// the lock in-process turns lock contention into a queue instead of
	// SQLITE_BUSY errors under concurrent uploads.
	writeMu sync.Mutex
}

// Open prepares the database file, applies migrations, and returns the store.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range splitStatements(m.SQL) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.Version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`,
			m.Version, time.Now().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d commit: %w", m.Version, err)
		}
	}
	return nil
}

func splitStatements(script string) []string {
	parts := strings.Split(script, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func scanTransfer(row interface{ Scan(...any) error }) (Transfer, error) {
	var (
		t           Transfer
		code        sql.NullString
		label       sql.NullString
		committed   sql.NullInt64
		expires     sql.NullInt64
		note        sql.NullString
		digest      sql.NullString
		lastErr     sql.NullString
		createdUnix int64
	)
	err := row.Scan(&t.ID, &code, &t.State, &label, &createdUnix, &committed, &expires,
		&t.RequestedTTLMinutes, &note, &digest, &t.WireBytes, &t.MaterializedBytes,
		&t.ReservedBytes, &t.RootPath, &lastErr)
	if err != nil {
		return Transfer{}, err
	}
	t.Code = code.String
	t.SenderLabel = label.String
	t.CreatedAt = time.Unix(createdUnix, 0).UTC()
	if committed.Valid {
		t.CommittedAt = time.Unix(committed.Int64, 0).UTC()
	}
	if expires.Valid {
		t.ExpiresAt = time.Unix(expires.Int64, 0).UTC()
	}
	t.Note = note.String
	t.ManifestDigest = digest.String
	t.LastErrorCode = lastErr.String
	return t, nil
}

const transferColumns = `id, code, state, sender_label, created_at, committed_at, expires_at,
    requested_ttl_minutes, note, manifest_digest, wire_bytes, materialized_bytes,
    reserved_bytes, root_path, last_error_code`

// CreateTransfer inserts a new pre-commit transfer.
func (s *Store) CreateTransfer(ctx context.Context, t Transfer) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO transfers
            (id, code, state, sender_label, created_at, committed_at, expires_at,
             requested_ttl_minutes, note, manifest_digest, wire_bytes, materialized_bytes,
             reserved_bytes, root_path, last_error_code)
            VALUES (?, NULL, ?, ?, ?, NULL, NULL, ?, ?, NULL, ?, ?, ?, ?, NULL)`,
			t.ID, t.State, nullString(t.SenderLabel), t.CreatedAt.Unix(),
			t.RequestedTTLMinutes, nullString(t.Note), t.WireBytes, t.MaterializedBytes,
			t.ReservedBytes, t.RootPath)
		return err
	})
}

// GetTransfer loads a transfer by internal ID.
func (s *Store) GetTransfer(ctx context.Context, id string) (Transfer, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+transferColumns+` FROM transfers WHERE id = ?`, id)
	t, err := scanTransfer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, ErrNotFound
	}
	return t, err
}

// GetTransferByCode loads a transfer by canonical (unhyphenated) code.
func (s *Store) GetTransferByCode(ctx context.Context, code string) (Transfer, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+transferColumns+` FROM transfers WHERE code = ?`, code)
	t, err := scanTransfer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, ErrNotFound
	}
	return t, err
}

// ListByState returns transfers in any of the given states.
func (s *Store) ListByState(ctx context.Context, states ...string) ([]Transfer, error) {
	if len(states) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(states)), ",")
	args := make([]any, len(states))
	for i, st := range states {
		args[i] = st
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+transferColumns+` FROM transfers WHERE state IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transfer
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetState moves a transfer to a new state.
func (s *Store) SetState(ctx context.Context, id, state string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE transfers SET state = ? WHERE id = ?`, state, id)
		return err
	})
}

// Fail records a terminal failure with its stable error code.
func (s *Store) Fail(ctx context.Context, id, errorCode string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE transfers SET state = ?, last_error_code = ? WHERE id = ?`,
			StateFailed, errorCode, id)
		return err
	})
}

// SetNoteAndTTL records the note and requested expiry chosen during a send.
// The simple upload path learns both only while streaming the request.
func (s *Store) SetNoteAndTTL(ctx context.Context, id, note string, ttlMinutes int) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE transfers SET note = ?, requested_ttl_minutes = ? WHERE id = ?`,
			nullString(note), ttlMinutes, id)
		return err
	})
}

// AddWireBytes accumulates received bytes for a transfer.
func (s *Store) AddWireBytes(ctx context.Context, id string, n int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE transfers SET wire_bytes = wire_bytes + ? WHERE id = ?`, n, id)
		return err
	})
}

// SetReservation replaces the outstanding disk reservation for a transfer.
func (s *Store) SetReservation(ctx context.Context, id string, bytes int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE transfers SET reserved_bytes = ? WHERE id = ?`, bytes, id)
		return err
	})
}

// ReservedBytes sums outstanding reservations for pre-commit transfers.
func (s *Store) ReservedBytes(ctx context.Context) (int64, error) {
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT SUM(reserved_bytes) FROM transfers
        WHERE state IN (?, ?, ?)`, StateCreated, StateUploading, StateVerifying).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n.Int64, nil
}

// Publish allocates a unique code and marks the transfer committed in one
// transaction. Publication is idempotent: an already-committed transfer returns
// its existing code unchanged.
func (s *Store) Publish(ctx context.Context, id string, now time.Time, ttlMinutes int, manifestDigest string, materializedBytes int64) (Transfer, error) {
	var out Transfer
	err := s.write(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+transferColumns+` FROM transfers WHERE id = ?`, id)
		t, err := scanTransfer(row)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if t.State == StateCommitted && t.Code != "" {
			out = t
			return nil
		}
		if t.State != StateCreated && t.State != StateUploading && t.State != StateVerifying {
			return protocol.Errorf(protocol.ErrStateConflict, "transfer is %s and cannot be committed", strings.ToLower(t.State))
		}
		committedAt := now.UTC().Truncate(time.Second)
		expiresAt := committedAt.Add(time.Duration(ttlMinutes) * time.Minute)
		// Retry on the astronomically unlikely code collision.
		for attempt := 0; attempt < 16; attempt++ {
			code := protocol.NewCode()
			canonical, _ := protocol.NormalizeCode(code)
			res, err := tx.ExecContext(ctx, `UPDATE transfers
                SET code = ?, state = ?, committed_at = ?, expires_at = ?,
                    manifest_digest = ?, materialized_bytes = ?, reserved_bytes = 0
                WHERE id = ? AND code IS NULL`,
				canonical, StateCommitted, committedAt.Unix(), expiresAt.Unix(),
				manifestDigest, materializedBytes, id)
			if err != nil {
				if isUniqueViolation(err) {
					continue
				}
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n == 0 {
				return protocol.Errorf(protocol.ErrStateConflict, "transfer already carries a code")
			}
			t.Code = canonical
			t.State = StateCommitted
			t.CommittedAt = committedAt
			t.ExpiresAt = expiresAt
			t.ManifestDigest = manifestDigest
			t.MaterializedBytes = materializedBytes
			out = t
			return nil
		}
		return protocol.Errorf(protocol.ErrInternal, "could not allocate a unique code")
	})
	return out, err
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

// InsertSegment records one declared segment.
func (s *Store) InsertSegment(ctx context.Context, seg Segment) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO segments
            (id, transfer_id, upload_id, kind, expected_length, received_length,
             digest_algorithm, expected_digest, state, relative_storage_path)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			seg.ID, seg.TransferID, nullString(seg.UploadID), seg.Kind, seg.ExpectedLength,
			seg.ReceivedLength, seg.DigestAlgorithm, nullString(seg.ExpectedDigest),
			seg.State, seg.RelativeStoragePath)
		return err
	})
}

const segmentColumns = `id, transfer_id, upload_id, kind, expected_length, received_length,
    digest_algorithm, expected_digest, state, relative_storage_path`

func scanSegment(row interface{ Scan(...any) error }) (Segment, error) {
	var (
		seg      Segment
		uploadID sql.NullString
		digest   sql.NullString
	)
	err := row.Scan(&seg.ID, &seg.TransferID, &uploadID, &seg.Kind, &seg.ExpectedLength,
		&seg.ReceivedLength, &seg.DigestAlgorithm, &digest, &seg.State, &seg.RelativeStoragePath)
	if err != nil {
		return Segment{}, err
	}
	seg.UploadID = uploadID.String
	seg.ExpectedDigest = digest.String
	return seg, nil
}

// GetSegmentByUploadID resolves a tus upload resource.
func (s *Store) GetSegmentByUploadID(ctx context.Context, uploadID string) (Segment, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+segmentColumns+` FROM segments WHERE upload_id = ?`, uploadID)
	seg, err := scanSegment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Segment{}, ErrNotFound
	}
	return seg, err
}

// GetSegment resolves a segment inside a transfer.
func (s *Store) GetSegment(ctx context.Context, transferID, segmentID string) (Segment, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+segmentColumns+` FROM segments WHERE transfer_id = ? AND id = ?`,
		transferID, segmentID)
	seg, err := scanSegment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Segment{}, ErrNotFound
	}
	return seg, err
}

// ListSegments returns every segment of a transfer.
func (s *Store) ListSegments(ctx context.Context, transferID string) ([]Segment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+segmentColumns+` FROM segments WHERE transfer_id = ? ORDER BY id`, transferID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Segment
	for rows.Next() {
		seg, err := scanSegment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	return out, rows.Err()
}

// SetSegmentProgress records the accepted offset for a resumable upload.
func (s *Store) SetSegmentProgress(ctx context.Context, transferID, id string, received int64, state string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE segments SET received_length = ?, state = ?
            WHERE transfer_id = ? AND id = ?`, received, state, transferID, id)
		return err
	})
}

// CreateClaim opens a receive session.
func (s *Store) CreateClaim(ctx context.Context, c Claim) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO claims
            (id, transfer_id, kind, created_at, lease_until, completed_at, token_hash)
            VALUES (?, ?, ?, ?, ?, NULL, ?)`,
			c.ID, c.TransferID, c.Kind, c.CreatedAt.Unix(), c.LeaseUntil.Unix(), nullString(c.TokenHash))
		return err
	})
}

// GetClaim loads a claim by ID.
func (s *Store) GetClaim(ctx context.Context, id string) (Claim, error) {
	var (
		c         Claim
		completed sql.NullInt64
		token     sql.NullString
		created   int64
		lease     int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, transfer_id, kind, created_at, lease_until, completed_at, token_hash
        FROM claims WHERE id = ?`, id).Scan(&c.ID, &c.TransferID, &c.Kind, &created, &lease, &completed, &token)
	if errors.Is(err, sql.ErrNoRows) {
		return Claim{}, ErrNotFound
	}
	if err != nil {
		return Claim{}, err
	}
	c.CreatedAt = time.Unix(created, 0).UTC()
	c.LeaseUntil = time.Unix(lease, 0).UTC()
	if completed.Valid {
		c.CompletedAt = time.Unix(completed.Int64, 0).UTC()
	}
	c.TokenHash = token.String
	return c, nil
}

// RenewClaim extends a lease for a session that is making progress.
func (s *Store) RenewClaim(ctx context.Context, id string, leaseUntil time.Time) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE claims SET lease_until = ? WHERE id = ? AND completed_at IS NULL`,
			leaseUntil.Unix(), id)
		return err
	})
}

// CompleteClaim records completion. It never consumes the transfer.
func (s *Store) CompleteClaim(ctx context.Context, id string, at time.Time) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE claims SET completed_at = ? WHERE id = ?`, at.Unix(), id)
		return err
	})
}

// ActiveClaimCount counts unfinished claims still holding a lease.
func (s *Store) ActiveClaimCount(ctx context.Context, transferID string, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM claims
        WHERE transfer_id = ? AND completed_at IS NULL AND lease_until > ?`,
		transferID, now.Unix()).Scan(&n)
	return n, err
}

// TotalActiveClaims counts leases across all transfers, for admin status.
func (s *Store) TotalActiveClaims(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM claims
        WHERE completed_at IS NULL AND lease_until > ?`, now.Unix()).Scan(&n)
	return n, err
}

// CountByState counts transfers in a state.
func (s *Store) CountByState(ctx context.Context, states ...string) (int, error) {
	if len(states) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(states)), ",")
	args := make([]any, len(states))
	for i, st := range states {
		args[i] = st
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transfers WHERE state IN (`+placeholders+`)`, args...).Scan(&n)
	return n, err
}

// ExpireDue marks committed transfers whose expiry has passed and returns how
// many changed state.
func (s *Store) ExpireDue(ctx context.Context, now time.Time) (int, error) {
	var n int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE transfers SET state = ?
            WHERE state = ? AND expires_at IS NOT NULL AND expires_at <= ?`,
			StateExpired, StateCommitted, now.Unix())
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

// UnpinnedExpired lists expired transfers with no active lease, ready to delete.
func (s *Store) UnpinnedExpired(ctx context.Context, now time.Time) ([]Transfer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+transferColumns+` FROM transfers t
        WHERE t.state IN (?, ?, ?)
          AND NOT EXISTS (SELECT 1 FROM claims c
                          WHERE c.transfer_id = t.id AND c.completed_at IS NULL AND c.lease_until > ?)`,
		StateExpired, StateFailed, StateAbandoned, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transfer
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// StaleStaging lists pre-commit transfers older than the staging timeout.
func (s *Store) StaleStaging(ctx context.Context, before time.Time) ([]Transfer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+transferColumns+` FROM transfers
        WHERE state IN (?, ?, ?) AND created_at < ?`,
		StateCreated, StateUploading, StateVerifying, before.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transfer
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Purge removes a transfer and its dependent rows after deletion completes.
func (s *Store) Purge(ctx context.Context, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM claims WHERE transfer_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM segments WHERE transfer_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE transfer_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM transfers WHERE id = ?`, id)
		return err
	})
}

// AllTransferIDs returns every known transfer ID, for reconciliation.
func (s *Store) AllTransferIDs(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, state FROM transfers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return nil, err
		}
		out[id] = state
	}
	return out, rows.Err()
}

// RememberIdempotency stores a completed idempotent operation. It returns the
// previously stored record when the key already exists.
func (s *Store) RememberIdempotency(ctx context.Context, rec Idempotency) (Idempotency, bool, error) {
	var existing Idempotency
	var found bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		prev, err := getIdempotency(ctx, tx, rec.KeyHash)
		if err == nil {
			existing, found = prev, true
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO idempotency_keys
            (key_hash, operation, transfer_id, response_code, created_at, expires_at, request_fingerprint)
            VALUES (?, ?, ?, ?, ?, ?, ?)`,
			rec.KeyHash, rec.Operation, nullString(rec.TransferID), nullString(rec.ResponseCode),
			rec.CreatedAt.Unix(), rec.ExpiresAt.Unix(), nullString(rec.RequestFingerprint))
		return err
	})
	return existing, found, err
}

// LookupIdempotency finds a stored idempotent result.
func (s *Store) LookupIdempotency(ctx context.Context, keyHash string) (Idempotency, error) {
	row := s.db.QueryRowContext(ctx, `SELECT key_hash, operation, transfer_id, response_code,
        created_at, expires_at, request_fingerprint FROM idempotency_keys WHERE key_hash = ?`, keyHash)
	return scanIdempotency(row)
}

func getIdempotency(ctx context.Context, tx *sql.Tx, keyHash string) (Idempotency, error) {
	row := tx.QueryRowContext(ctx, `SELECT key_hash, operation, transfer_id, response_code,
        created_at, expires_at, request_fingerprint FROM idempotency_keys WHERE key_hash = ?`, keyHash)
	return scanIdempotency(row)
}

func scanIdempotency(row interface{ Scan(...any) error }) (Idempotency, error) {
	var (
		rec         Idempotency
		transferID  sql.NullString
		responseVal sql.NullString
		fingerprint sql.NullString
		created     int64
		expires     int64
	)
	err := row.Scan(&rec.KeyHash, &rec.Operation, &transferID, &responseVal, &created, &expires, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return Idempotency{}, ErrNotFound
	}
	if err != nil {
		return Idempotency{}, err
	}
	rec.TransferID = transferID.String
	rec.ResponseCode = responseVal.String
	rec.CreatedAt = time.Unix(created, 0).UTC()
	rec.ExpiresAt = time.Unix(expires, 0).UTC()
	rec.RequestFingerprint = fingerprint.String
	return rec, nil
}

// CompleteIdempotency fills in the result once the operation succeeds.
func (s *Store) CompleteIdempotency(ctx context.Context, keyHash, transferID, responseCode string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE idempotency_keys SET transfer_id = ?, response_code = ? WHERE key_hash = ?`,
			nullString(transferID), nullString(responseCode), keyHash)
		return err
	})
}

// ForgetIdempotency drops a key whose operation failed, so a retry is allowed.
func (s *Store) ForgetIdempotency(ctx context.Context, keyHash string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE key_hash = ?`, keyHash)
		return err
	})
}

// PurgeExpiredIdempotency removes expired keys.
func (s *Store) PurgeExpiredIdempotency(ctx context.Context, now time.Time) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE expires_at <= ?`, now.Unix())
		return err
	})
}
