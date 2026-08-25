// Package public implements the HTTPS-facing API. The HTTP interface is the
// product: every behavior here must work from raw curl with no client install.
package public

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sss/sss/internal/archive"
	"github.com/sss/sss/internal/observability"
	"github.com/sss/sss/internal/protocol"
	"github.com/sss/sss/internal/store/sqlite"
	"github.com/sss/sss/internal/transfer"
	"github.com/sss/sss/internal/version"
)

// maxJSONBody bounds control-plane request bodies. Payload bytes never travel
// through a JSON endpoint.
const maxJSONBody = 8 << 20

// idempotencyTTL is how long a key is remembered.
const idempotencyTTL = 24 * time.Hour

// API holds the handlers.
type API struct {
	svc *transfer.Service
	log *slog.Logger
}

// New builds the public API.
func New(svc *transfer.Service, log *slog.Logger) *API { return &API{svc: svc, log: log} }

// Register installs the public routes. authed wraps the routes that require the
// shared base password; unauthenticated liveness and claim-token routes are
// registered directly.
func (a *API) Register(mux *http.ServeMux, authed func(http.Handler) http.Handler) {
	open := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, h) }
	protected := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, authed(h)) }

	open("GET /healthz", a.handleHealthz)
	open("GET /readyz", a.handleReadyz)

	protected("GET /v1/info", a.handleInfo)
	protected("POST /s", a.handleSimpleUpload)
	protected("POST /s/raw", a.handleRawUpload)
	protected("GET /r/{code}", a.handleDownload)
	protected("HEAD /r/{code}", a.handleDownload)
	protected("GET /v1/transfers/{code}", a.handleMetadata)
	protected("POST /v1/transfers", a.handleCreateTransfer)
	protected("POST /v1/transfers/{transfer_id}/commit", a.handleCommit)
	protected("HEAD /v1/uploads/{upload_id}", a.handleUploadHead)
	protected("PATCH /v1/uploads/{upload_id}", a.handleUploadPatch)
	protected("POST /v1/claims", a.handleCreateClaim)

	// Claim sessions authenticate with their own bearer token, so they cannot
	// also carry the Basic credential in the same Authorization header.
	open("GET /v1/claims/{claim_id}/segments/{segment_id}", a.handleClaimSegment)
	open("GET /v1/claims/{claim_id}/segments/{segment_id}/outboard", a.handleClaimOutboard)
	open("POST /v1/claims/{claim_id}/complete", a.handleClaimComplete)
}

func (a *API) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writePlain(w, http.StatusOK, "ok\n")
}

func (a *API) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !a.svc.Ready() {
		writePlain(w, http.StatusServiceUnavailable, "reconciling\n")
		return
	}
	writePlain(w, http.StatusOK, "ready\n")
}

func (a *API) handleInfo(w http.ResponseWriter, r *http.Request) {
	info := a.svc.Info()
	info.ApplicationVersion = version.Version
	info.ProtocolVersion = version.Protocol
	writeJSON(w, http.StatusOK, info)
}

// handleSimpleUpload streams a multipart send straight to staging.
func (a *API) handleSimpleUpload(w http.ResponseWriter, r *http.Request) {
	key, replay, perr := a.beginIdempotent(r, "simple-send")
	if perr != nil {
		WriteError(w, r, perr)
		return
	}
	if replay != "" {
		a.respondCode(w, r, protocol.CommitResponse{Code: replay}, http.StatusOK)
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		a.forget(r, key)
		WriteError(w, r, protocol.Errorf(protocol.ErrInvalidRequest, "expected a multipart/form-data body"))
		return
	}
	sess, perr := a.svc.BeginSimple(r.Context())
	if perr != nil {
		a.forget(r, key)
		WriteError(w, r, perr)
		return
	}
	committed := false
	defer func() {
		if !committed {
			sess.Abort(r.Context())
		}
	}()

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			a.forget(r, key)
			WriteError(w, r, protocol.Errorf(protocol.ErrNetwork, "multipart stream failed: %v", err))
			return
		}
		switch part.FormName() {
		case "file":
			if perr := sess.AddFile(r.Context(), part.FileName(), part); perr != nil {
				part.Close()
				a.forget(r, key)
				WriteError(w, r, perr)
				return
			}
		case "note":
			value, perr := readField(part, protocol.MaxNoteBytesHard+1)
			if perr != nil {
				part.Close()
				a.forget(r, key)
				WriteError(w, r, perr)
				return
			}
			if perr := sess.SetNote(value); perr != nil {
				part.Close()
				a.forget(r, key)
				WriteError(w, r, perr)
				return
			}
		case "ttl":
			value, perr := readField(part, 32)
			if perr != nil {
				part.Close()
				a.forget(r, key)
				WriteError(w, r, perr)
				return
			}
			minutes, perr := protocol.ParseTTL(strings.TrimSpace(value))
			if perr != nil {
				part.Close()
				a.forget(r, key)
				WriteError(w, r, perr)
				return
			}
			if perr := sess.SetTTL(minutes); perr != nil {
				part.Close()
				a.forget(r, key)
				WriteError(w, r, perr)
				return
			}
		default:
			// Unknown fields are ignored so future clients stay compatible,
			// but their bytes are still drained.
			_, _ = io.Copy(io.Discard, io.LimitReader(part, 1<<20))
		}
		part.Close()
	}

	resp, perr := sess.Commit(r.Context())
	if perr != nil {
		a.forget(r, key)
		WriteError(w, r, perr)
		return
	}
	committed = true
	a.completeIdempotent(r, key, sess.ID(), resp.Code)
	a.respondCode(w, r, resp, http.StatusCreated)
}

// handleRawUpload streams a single named body into a handoff.
func (a *API) handleRawUpload(w http.ResponseWriter, r *http.Request) {
	key, replay, perr := a.beginIdempotent(r, "raw-send")
	if perr != nil {
		WriteError(w, r, perr)
		return
	}
	if replay != "" {
		a.respondCode(w, r, protocol.CommitResponse{Code: replay}, http.StatusOK)
		return
	}
	filename := strings.TrimSpace(r.Header.Get("X-SSS-Filename"))
	if filename == "" {
		a.forget(r, key)
		WriteError(w, r, protocol.Errorf(protocol.ErrInvalidRequest, "X-SSS-Filename is required"))
		return
	}
	minutes, perr := a.svc.SimpleRawTTL(r.Header.Get("X-SSS-TTL"))
	if perr != nil {
		a.forget(r, key)
		WriteError(w, r, perr)
		return
	}
	sess, perr := a.svc.BeginSimple(r.Context())
	if perr != nil {
		a.forget(r, key)
		WriteError(w, r, perr)
		return
	}
	committed := false
	defer func() {
		if !committed {
			sess.Abort(r.Context())
		}
	}()
	if perr := sess.SetTTL(minutes); perr != nil {
		a.forget(r, key)
		WriteError(w, r, perr)
		return
	}
	if note := r.Header.Get("X-SSS-Note"); note != "" {
		if perr := sess.SetNote(note); perr != nil {
			a.forget(r, key)
			WriteError(w, r, perr)
			return
		}
	}
	if perr := sess.AddFile(r.Context(), filename, r.Body); perr != nil {
		a.forget(r, key)
		WriteError(w, r, perr)
		return
	}
	resp, perr := sess.Commit(r.Context())
	if perr != nil {
		a.forget(r, key)
		WriteError(w, r, perr)
		return
	}
	committed = true
	a.completeIdempotent(r, key, sess.ID(), resp.Code)
	a.respondCode(w, r, resp, http.StatusCreated)
}

// respondCode returns the allocated code. Plain text keeps `CODE=$(curl ...)`
// working; JSON is available to clients that ask for it.
func (a *API) respondCode(w http.ResponseWriter, r *http.Request, resp protocol.CommitResponse, status int) {
	w.Header().Set("Location", "/v1/transfers/"+resp.Code)
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, status, resp)
		return
	}
	writePlain(w, status, resp.Code+"\n")
}

func readField(r io.Reader, limit int64) (string, *protocol.Error) {
	data, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return "", protocol.Errorf(protocol.ErrNetwork, "could not read form field")
	}
	if int64(len(data)) >= limit {
		return "", protocol.Errorf(protocol.ErrInvalidRequest, "form field is too large")
	}
	return string(data), nil
}

// handleDownload serves the simple receive path.
func (a *API) handleDownload(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "auto"
	}
	switch format {
	case "auto", "raw", "zip", "tar":
	default:
		WriteError(w, r, protocol.Errorf(protocol.ErrInvalidRequest, "unsupported format %q", format))
		return
	}
	dl, perr := a.svc.OpenDownload(r.Context(), r.PathValue("code"))
	if perr != nil {
		WriteError(w, r, perr)
		return
	}
	release, perr := a.svc.AcquireDownload(r.Context())
	if perr != nil {
		WriteError(w, r, perr)
		return
	}
	defer release()

	single, isSingle := dl.Manifest.SingleRootFile()
	if format == "raw" && !isSingle {
		WriteError(w, r, protocol.Errorf(protocol.ErrInvalidRequest, "format=raw requires a single regular file"))
		return
	}
	code := protocol.FormatCode(dl.Transfer.Code)

	if format == "raw" || (format == "auto" && isSingle) {
		a.serveRawFile(w, r, dl, single)
		return
	}
	if format == "tar" {
		w.Header().Set("Content-Type", "application/x-tar")
		setDisposition(w, "sss-"+code+".tar")
	} else {
		w.Header().Set("Content-Type", "application/zip")
		setDisposition(w, "sss-"+code+".zip")
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		// Archive length is not precomputed: doing so would require generating
		// the whole archive first.
		w.WriteHeader(http.StatusOK)
		return
	}
	var err error
	if format == "tar" {
		err = archive.StreamTar(r.Context(), w, dl.PayloadDir, dl.Manifest)
	} else {
		err = archive.StreamZip(r.Context(), w, dl.PayloadDir, dl.Manifest)
	}
	if err != nil && r.Context().Err() == nil {
		// Headers are already sent; the truncated body plus this log is all we
		// can report.
		a.log.Warn("archive stream failed",
			"request_id", observability.RequestID(r.Context()),
			"code", code, "error", err.Error())
	}
}

func (a *API) serveRawFile(w http.ResponseWriter, r *http.Request, dl transfer.Download, entry protocol.Entry) {
	path, err := safeJoin(dl.PayloadDir, entry.Path)
	if err != nil {
		WriteError(w, r, protocol.Errorf(protocol.ErrInternal, "payload path is unsafe"))
		return
	}
	f, err := openFile(path)
	if err != nil {
		WriteError(w, r, protocol.Errorf(protocol.ErrInternal, "payload file is unavailable"))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	setDisposition(w, entry.Path)
	http.ServeContent(w, r, entry.Path, dl.Transfer.CommittedAt, f)
}

// setDisposition writes a safe Content-Disposition header for a filename.
func setDisposition(w http.ResponseWriter, name string) {
	ascii := sanitizeHeaderFilename(name)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, ascii, url.PathEscape(name)))
}

// sanitizeHeaderFilename reduces a name to quoted-string-safe ASCII.
func sanitizeHeaderFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteByte('_')
		case r == '"' || r == '\\' || r == '/':
			b.WriteByte('_')
		case r > 0x7f:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "download"
	}
	return out
}

func (a *API) handleMetadata(w http.ResponseWriter, r *http.Request) {
	meta, perr := a.svc.Metadata(r.Context(), r.PathValue("code"))
	if perr != nil {
		WriteError(w, r, perr)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

func (a *API) handleCreateTransfer(w http.ResponseWriter, r *http.Request) {
	key, replay, perr := a.beginIdempotent(r, "create-transfer")
	if perr != nil {
		WriteError(w, r, perr)
		return
	}
	if replay != "" {
		// A create replay cannot reconstruct upload resources, so the client is
		// told to use a fresh key rather than receiving a misleading response.
		WriteError(w, r, protocol.Errorf(protocol.ErrIdempotencyConflict, "this key already created a transfer"))
		return
	}
	var req protocol.CreateTransferRequest
	if perr := decodeJSON(r, &req); perr != nil {
		a.forget(r, key)
		WriteError(w, r, perr)
		return
	}
	resp, perr := a.svc.CreateTransfer(r.Context(), req)
	if perr != nil {
		a.forget(r, key)
		WriteError(w, r, perr)
		return
	}
	a.completeIdempotent(r, key, resp.TransferID, "")
	writeJSON(w, http.StatusCreated, resp)
}

func (a *API) handleUploadHead(w http.ResponseWriter, r *http.Request) {
	offset, length, perr := a.svc.UploadState(r.Context(), r.PathValue("upload_id"))
	if perr != nil {
		WriteError(w, r, perr)
		return
	}
	w.Header().Set("Tus-Resumable", "1.0.0")
	w.Header().Set("Upload-Offset", strconv.FormatInt(offset, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

func (a *API) handleUploadPatch(w http.ResponseWriter, r *http.Request) {
	if v := r.Header.Get("Tus-Resumable"); v != "" && v != "1.0.0" {
		WriteError(w, r, protocol.Errorf(protocol.ErrProtocolMismatch, "unsupported Tus-Resumable version %q", v))
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mediatype, _, err := mime.ParseMediaType(ct)
		if err != nil || mediatype != "application/offset+octet-stream" {
			WriteError(w, r, protocol.Errorf(protocol.ErrInvalidRequest,
				"Content-Type must be application/offset+octet-stream"))
			return
		}
	}
	raw := r.Header.Get("Upload-Offset")
	offset, err := strconv.ParseInt(raw, 10, 64)
	if raw == "" || err != nil || offset < 0 {
		WriteError(w, r, protocol.Errorf(protocol.ErrInvalidRequest, "Upload-Offset must be a non-negative integer"))
		return
	}
	newOffset, perr := a.svc.UploadPatch(r.Context(), r.PathValue("upload_id"), offset, r.Body)
	w.Header().Set("Tus-Resumable", "1.0.0")
	w.Header().Set("Upload-Offset", strconv.FormatInt(newOffset, 10))
	if perr != nil {
		WriteError(w, r, perr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleCommit(w http.ResponseWriter, r *http.Request) {
	var req protocol.CommitRequest
	if perr := decodeJSON(r, &req); perr != nil {
		WriteError(w, r, perr)
		return
	}
	resp, created, perr := a.svc.Commit(r.Context(), r.PathValue("transfer_id"), req.Manifest)
	if perr != nil {
		WriteError(w, r, perr)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	w.Header().Set("Location", "/v1/transfers/"+resp.Code)
	writeJSON(w, status, resp)
}

func (a *API) handleCreateClaim(w http.ResponseWriter, r *http.Request) {
	var req protocol.ClaimRequest
	if perr := decodeJSON(r, &req); perr != nil {
		WriteError(w, r, perr)
		return
	}
	resp, perr := a.svc.CreateClaim(r.Context(), req.Code)
	if perr != nil {
		WriteError(w, r, perr)
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (a *API) handleClaimSegment(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="sss-claim"`)
		WriteError(w, r, protocol.Errorf(protocol.ErrAuthRequired, "claim token required"))
		return
	}
	release, perr := a.svc.AcquireDownload(r.Context())
	if perr != nil {
		WriteError(w, r, perr)
		return
	}
	defer release()

	f, size, perr := a.svc.ClaimSegmentFile(r.Context(), r.PathValue("claim_id"), token, r.PathValue("segment_id"))
	if perr != nil {
		WriteError(w, r, perr)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-SSS-Segment-Length", strconv.FormatInt(size, 10))
	// Segments are immutable, so range requests are always safe to serve.
	http.ServeContent(w, r, r.PathValue("segment_id"), time.Time{}, f)
}

// handleClaimOutboard serves a segment's BLAKE3 verification tree. It is about
// 0.1% of the segment's size and lets the receiver verify each group as it
// arrives, so a corrupt range costs one group rather than the whole segment.
func (a *API) handleClaimOutboard(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="sss-claim"`)
		WriteError(w, r, protocol.Errorf(protocol.ErrAuthRequired, "claim token required"))
		return
	}
	f, size, perr := a.svc.ClaimOutboardFile(r.Context(), r.PathValue("claim_id"), token, r.PathValue("segment_id"))
	if perr != nil {
		WriteError(w, r, perr)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-SSS-Outboard-Length", strconv.FormatInt(size, 10))
	http.ServeContent(w, r, r.PathValue("segment_id")+".obao", time.Time{}, f)
}

func (a *API) handleClaimComplete(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="sss-claim"`)
		WriteError(w, r, protocol.Errorf(protocol.ErrAuthRequired, "claim token required"))
		return
	}
	if perr := a.svc.CompleteClaim(r.Context(), r.PathValue("claim_id"), token); perr != nil {
		WriteError(w, r, perr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

func decodeJSON(r *http.Request, dst any) *protocol.Error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody))
	if err := dec.Decode(dst); err != nil {
		return protocol.Errorf(protocol.ErrInvalidRequest, "malformed JSON body")
	}
	return nil
}

// beginIdempotent reserves an Idempotency-Key. It returns a previously
// allocated code when the same key already produced a result.
func (a *API) beginIdempotent(r *http.Request, operation string) (keyHash string, replayCode string, perr *protocol.Error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return "", "", nil
	}
	if len(key) < 8 || len(key) > 200 {
		return "", "", protocol.Errorf(protocol.ErrInvalidRequest, "Idempotency-Key must be 8 to 200 characters")
	}
	sum := sha256.Sum256([]byte(operation + "\x00" + key))
	hash := hex.EncodeToString(sum[:])
	fingerprint := requestFingerprint(r)
	now := a.svc.Now()
	existing, found, err := a.svc.Store().RememberIdempotency(r.Context(), sqlite.Idempotency{
		KeyHash:            hash,
		Operation:          operation,
		CreatedAt:          now,
		ExpiresAt:          now.Add(idempotencyTTL),
		RequestFingerprint: fingerprint,
	})
	if err != nil {
		return "", "", protocol.Errorf(protocol.ErrInternal, "could not record idempotency key")
	}
	if !found {
		return hash, "", nil
	}
	if existing.RequestFingerprint != fingerprint {
		return "", "", protocol.Errorf(protocol.ErrIdempotencyConflict, "this key was used for a different request")
	}
	if existing.ResponseCode != "" {
		return hash, existing.ResponseCode, nil
	}
	if existing.TransferID != "" {
		return hash, "", protocol.Errorf(protocol.ErrIdempotencyConflict, "this key already created a transfer")
	}
	return "", "", protocol.Errorf(protocol.ErrStateConflict, "a request with this key is still in flight")
}

func (a *API) completeIdempotent(r *http.Request, keyHash, transferID, code string) {
	if keyHash == "" {
		return
	}
	if err := a.svc.Store().CompleteIdempotency(r.Context(), keyHash, transferID, code); err != nil {
		a.log.Warn("could not record idempotent result", "error", err.Error())
	}
}

// forget releases a key whose operation failed so a retry may reuse it.
func (a *API) forget(r *http.Request, keyHash string) {
	if keyHash == "" {
		return
	}
	if err := a.svc.Store().ForgetIdempotency(r.Context(), keyHash); err != nil {
		a.log.Warn("could not release idempotency key", "error", err.Error())
	}
}

func requestFingerprint(r *http.Request) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		r.Method,
		r.URL.Path,
		strconv.FormatInt(r.ContentLength, 10),
		r.Header.Get("X-SSS-Filename"),
		r.Header.Get("X-SSS-TTL"),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}
