package public

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/sss/sss/internal/observability"
	"github.com/sss/sss/internal/protocol"
)

// WriteError renders a stable error. JSON endpoints always receive the error
// envelope; the deliberately plain simple endpoints receive a single line with
// the same status unless the caller asked for JSON.
func WriteError(w http.ResponseWriter, r *http.Request, err *protocol.Error) {
	if err == nil {
		err = protocol.Errorf(protocol.ErrInternal, "unspecified failure")
	}
	err.RequestID = observability.RequestID(r.Context())
	status := protocol.HTTPStatus(err.Code)
	if wantsJSON(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(protocol.Envelope{Err: *err})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(err.PlainLine() + "\n"))
}

// wantsJSON reports whether a JSON error envelope is appropriate.
func wantsJSON(r *http.Request) bool {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		return true
	}
	p := r.URL.Path
	return strings.HasPrefix(p, "/v1/") || p == "/v1" || strings.HasPrefix(p, "/local/status") || strings.HasPrefix(p, "/local/cleanup")
}

// writeJSON renders a successful JSON body.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writePlain renders a successful plain-text body.
func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
