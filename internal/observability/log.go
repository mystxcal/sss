// Package observability provides the small logging surface the service needs:
// structured logs to stdout/journald with request correlation and no secrets.
package observability

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type ctxKey int

const requestIDKey ctxKey = iota

// New builds the process logger. Levels: debug, info, warn, error.
func New(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:       lvl,
		ReplaceAttr: redact,
	})
	return slog.New(h)
}

// sensitiveKeys never appear in logs, regardless of caller mistakes.
var sensitiveKeys = map[string]bool{
	"authorization":    true,
	"password":         true,
	"password_hash":    true,
	"sss_password":     true,
	"token":            true,
	"claim_token":      true,
	"www-authenticate": true,
}

func redact(_ []string, a slog.Attr) slog.Attr {
	if sensitiveKeys[strings.ToLower(a.Key)] {
		return slog.String(a.Key, "[redacted]")
	}
	return a
}

// WithRequestID stores a request correlation ID on a context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID reads the correlation ID from a context, if any.
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}
