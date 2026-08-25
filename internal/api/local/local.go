// Package local implements the VPS-local Unix-socket API. Authorization is the
// socket's filesystem permissions; these routes are never exposed publicly.
package local

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/sss/sss/internal/api/public"
	"github.com/sss/sss/internal/janitor"
	"github.com/sss/sss/internal/protocol"
	"github.com/sss/sss/internal/transfer"
	"github.com/sss/sss/internal/version"
)

// API holds the local-plane handlers.
type API struct {
	svc     *transfer.Service
	janitor *janitor.Janitor
	log     *slog.Logger
}

// New builds the local API.
func New(svc *transfer.Service, j *janitor.Janitor, log *slog.Logger) *API {
	return &API{svc: svc, janitor: j, log: log}
}

// Register installs the local routes on a mux.
func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /local/r/{code}", a.handleLocalReceive)
	mux.HandleFunc("GET /local/status", a.handleStatus)
	mux.HandleFunc("POST /local/cleanup", a.handleCleanup)
	mux.HandleFunc("GET /local/healthz", a.handleHealthz)
}

// handleLocalReceive returns the existing committed payload path. No payload
// bytes are transferred and no second copy is created.
func (a *API) handleLocalReceive(w http.ResponseWriter, r *http.Request) {
	claim, perr := a.svc.LocalPath(r.Context(), r.PathValue("code"))
	if perr != nil {
		public.WriteError(w, r, perr)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		writeJSON(w, claim)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(claim.Path + "\n"))
}

func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := a.svc.Status(r.Context())
	if err != nil {
		public.WriteError(w, r, protocol.Errorf(protocol.ErrInternal, "could not read status"))
		return
	}
	status.Version = version.Version
	status.ProtocolVersion = version.Protocol
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writeJSON(w, status)
}

func (a *API) handleCleanup(w http.ResponseWriter, r *http.Request) {
	result, err := a.janitor.Sweep(r.Context())
	if err != nil {
		public.WriteError(w, r, protocol.Errorf(protocol.ErrInternal, "cleanup failed: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writeJSON(w, result)
}

func (a *API) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	writeJSON(w, map[string]any{
		"ok":               true,
		"ready":            a.svc.Ready(),
		"version":          version.Version,
		"protocol_version": version.Protocol,
	})
}
