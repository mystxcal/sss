// Package middleware holds the cross-cutting HTTP concerns: request IDs,
// authentication with failed-attempt rate limiting, panic recovery, and
// request logging that never records a credential.
package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/sss/sss/internal/auth"
	"github.com/sss/sss/internal/ids"
	"github.com/sss/sss/internal/observability"
	"github.com/sss/sss/internal/protocol"
)

// ErrorWriter renders a stable error to a response.
type ErrorWriter func(w http.ResponseWriter, r *http.Request, err *protocol.Error)

// RequestID assigns and echoes a correlation identifier.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ids.RequestID()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(observability.WithRequestID(r.Context(), id)))
	})
}

// Recover turns a panic into a stable INTERNAL error instead of a dropped
// connection, and logs it with the request ID.
func Recover(log *slog.Logger, writeErr ErrorWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					log.Error("panic while handling request",
						"request_id", observability.RequestID(r.Context()),
						"path", r.URL.Path,
						"panic", v)
					writeErr(w, r, protocol.Errorf(protocol.ErrInternal, "unexpected server failure"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Auth enforces HTTP Basic authentication with the shared base password.
func Auth(v *auth.Verifier, limiter *auth.Limiter, log *slog.Logger, writeErr ErrorWriter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := clientKey(r)
			if !limiter.Allow(key) {
				w.Header().Set("Retry-After", "60")
				writeErr(w, r, protocol.Errorf(protocol.ErrRateLimited, "too many failed attempts"))
				return
			}
			if err := v.CheckBasic(r); err != nil {
				limiter.Fail(key)
				w.Header().Set("WWW-Authenticate", `Basic realm="sss", charset="UTF-8"`)
				log.Warn("authentication failed",
					"request_id", observability.RequestID(r.Context()),
					"remote", key,
					"code", err.Code)
				writeErr(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// LogRequests records method, path, status, size, and duration.
func LogRequests(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			log.Info("request",
				"request_id", observability.RequestID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.written,
				"duration_ms", time.Since(start).Milliseconds())
		})
	}
}

// Chain applies middleware in order, outermost first.
func Chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

type recorder struct {
	http.ResponseWriter
	status  int
	written int64
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(p []byte) (int, error) {
	n, err := r.ResponseWriter.Write(p)
	r.written += int64(n)
	return n, err
}

// Flush lets streamed archives reach the client promptly.
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// clientKey identifies a caller for rate limiting. Behind a reverse proxy on
// loopback the forwarded address is more useful than the proxy's own address.
func clientKey(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if host, _, err := net.SplitHostPort(fwd); err == nil {
			return host
		}
		return firstAddress(fwd)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func firstAddress(list string) string {
	for i := 0; i < len(list); i++ {
		if list[i] == ',' {
			return trimSpace(list[:i])
		}
	}
	return trimSpace(list)
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
