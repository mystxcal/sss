package sqlite

// migrations are applied in order and recorded in schema_migrations. Never edit
// a released migration; append a new one instead.
var migrations = []struct {
	Version int
	SQL     string
}{
	{
		Version: 1,
		SQL: `
CREATE TABLE transfers (
    id                    TEXT PRIMARY KEY,
    code                  TEXT UNIQUE,
    state                 TEXT NOT NULL,
    sender_label          TEXT,
    created_at            INTEGER NOT NULL,
    committed_at          INTEGER,
    expires_at            INTEGER,
    requested_ttl_minutes INTEGER NOT NULL,
    note                  TEXT,
    manifest_digest       TEXT,
    wire_bytes            INTEGER NOT NULL DEFAULT 0,
    materialized_bytes    INTEGER NOT NULL DEFAULT 0,
    reserved_bytes        INTEGER NOT NULL DEFAULT 0,
    root_path             TEXT NOT NULL,
    last_error_code       TEXT
);
CREATE INDEX idx_transfers_state ON transfers(state);
CREATE INDEX idx_transfers_expires ON transfers(expires_at);

CREATE TABLE segments (
    -- Segment identifiers are chosen by the client and are only unique inside
    -- one transfer, so the primary key is the pair.
    id                    TEXT NOT NULL,
    transfer_id           TEXT NOT NULL REFERENCES transfers(id) ON DELETE CASCADE,
    upload_id             TEXT UNIQUE,
    kind                  TEXT NOT NULL,
    expected_length       INTEGER NOT NULL,
    received_length       INTEGER NOT NULL DEFAULT 0,
    digest_algorithm      TEXT NOT NULL,
    expected_digest       TEXT,
    state                 TEXT NOT NULL,
    relative_storage_path TEXT NOT NULL,
    PRIMARY KEY (transfer_id, id)
);
CREATE INDEX idx_segments_transfer ON segments(transfer_id);

CREATE TABLE claims (
    id           TEXT PRIMARY KEY,
    transfer_id  TEXT NOT NULL REFERENCES transfers(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    lease_until  INTEGER NOT NULL,
    completed_at INTEGER,
    token_hash   TEXT
);
CREATE INDEX idx_claims_transfer ON claims(transfer_id);
CREATE INDEX idx_claims_lease ON claims(lease_until);

CREATE TABLE idempotency_keys (
    key_hash            TEXT PRIMARY KEY,
    operation           TEXT NOT NULL,
    transfer_id         TEXT,
    response_code       TEXT,
    created_at          INTEGER NOT NULL,
    expires_at          INTEGER NOT NULL,
    request_fingerprint TEXT
);
CREATE INDEX idx_idempotency_expires ON idempotency_keys(expires_at);
`,
	},
}
