package protocol

import (
	"errors"
	"fmt"
	"net/http"
)

// Stable error codes shared by the HTTP API and the CLI. Messages may improve;
// these codes and their meanings are stable within a protocol major version.
const (
	ErrAuthRequired        = "AUTH_REQUIRED"
	ErrAuthInvalid         = "AUTH_INVALID"
	ErrRateLimited         = "RATE_LIMITED"
	ErrInvalidRequest      = "INVALID_REQUEST"
	ErrInvalidCode         = "INVALID_CODE"
	ErrTransferNotFound    = "TRANSFER_NOT_FOUND"
	ErrTransferExpired     = "TRANSFER_EXPIRED"
	ErrTTLOutOfRange       = "TTL_OUT_OF_RANGE"
	ErrNoFiles             = "NO_FILES"
	ErrUnsupportedEntry    = "UNSUPPORTED_ENTRY"
	ErrInvalidPath         = "INVALID_PATH"
	ErrDuplicatePath       = "DUPLICATE_PATH"
	ErrSourceChanged       = "SOURCE_CHANGED"
	ErrOffsetMismatch      = "OFFSET_MISMATCH"
	ErrStateConflict       = "STATE_CONFLICT"
	ErrIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	ErrPayloadTooLarge     = "PAYLOAD_TOO_LARGE"
	ErrTooManyFiles        = "TOO_MANY_FILES"
	ErrInsufficientStorage = "INSUFFICIENT_STORAGE"
	ErrHashMismatch        = "HASH_MISMATCH"
	ErrClaimExpired        = "CLAIM_EXPIRED"
	ErrDestinationExists   = "DESTINATION_EXISTS"
	ErrNetwork             = "NETWORK_ERROR"
	ErrProtocolMismatch    = "PROTOCOL_MISMATCH"
	ErrInternal            = "INTERNAL"
)

// httpStatus maps each stable code to its documented HTTP status.
var httpStatus = map[string]int{
	ErrAuthRequired:        http.StatusUnauthorized,
	ErrAuthInvalid:         http.StatusUnauthorized,
	ErrRateLimited:         http.StatusTooManyRequests,
	ErrInvalidRequest:      http.StatusBadRequest,
	ErrInvalidCode:         http.StatusBadRequest,
	ErrTransferNotFound:    http.StatusNotFound,
	ErrTransferExpired:     http.StatusGone,
	ErrTTLOutOfRange:       http.StatusUnprocessableEntity,
	ErrNoFiles:             http.StatusUnprocessableEntity,
	ErrUnsupportedEntry:    http.StatusUnprocessableEntity,
	ErrInvalidPath:         http.StatusUnprocessableEntity,
	ErrDuplicatePath:       http.StatusUnprocessableEntity,
	ErrSourceChanged:       http.StatusConflict,
	ErrOffsetMismatch:      http.StatusConflict,
	ErrStateConflict:       http.StatusConflict,
	ErrIdempotencyConflict: http.StatusConflict,
	ErrPayloadTooLarge:     http.StatusRequestEntityTooLarge,
	ErrTooManyFiles:        http.StatusRequestEntityTooLarge,
	ErrInsufficientStorage: http.StatusInsufficientStorage,
	ErrHashMismatch:        http.StatusUnprocessableEntity,
	ErrClaimExpired:        http.StatusGone,
	ErrProtocolMismatch:    http.StatusUpgradeRequired,
	ErrInternal:            http.StatusInternalServerError,
}

// exitCode maps each stable code to its documented CLI exit code.
var exitCode = map[string]int{
	ErrAuthRequired:        3,
	ErrAuthInvalid:         3,
	ErrRateLimited:         5,
	ErrInvalidRequest:      2,
	ErrInvalidCode:         2,
	ErrTransferNotFound:    4,
	ErrTransferExpired:     4,
	ErrTTLOutOfRange:       2,
	ErrNoFiles:             2,
	ErrUnsupportedEntry:    7,
	ErrInvalidPath:         7,
	ErrDuplicatePath:       7,
	ErrSourceChanged:       6,
	ErrOffsetMismatch:      6,
	ErrStateConflict:       6,
	ErrIdempotencyConflict: 6,
	ErrPayloadTooLarge:     7,
	ErrTooManyFiles:        7,
	ErrInsufficientStorage: 7,
	ErrHashMismatch:        6,
	ErrClaimExpired:        4,
	ErrDestinationExists:   7,
	ErrNetwork:             5,
	ErrProtocolMismatch:    5,
	ErrInternal:            8,
}

// HTTPStatus returns the documented status for a stable code.
func HTTPStatus(code string) int {
	if s, ok := httpStatus[code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// ExitCode returns the documented CLI exit code for a stable code.
func ExitCode(code string) int {
	if c, ok := exitCode[code]; ok {
		return c
	}
	return 8
}

// Error is a stable, transportable failure with an optional request ID.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// Envelope is the JSON error body used by JSON endpoints.
type Envelope struct {
	Err Error `json:"error"`
}

// Error implements the error interface.
func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf builds a stable error with a formatted message.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// AsError extracts a stable error, or synthesizes an INTERNAL one.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if err == nil {
		return nil
	}
	return &Error{Code: ErrInternal, Message: err.Error()}
}

// PlainLine renders the plain-text form used by the simple endpoints.
func (e *Error) PlainLine() string { return e.Code + ": " + e.Message }
