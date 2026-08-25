// Package client speaks the SSS HTTP contract. The CLI is a thin layer on top
// of this package: there is no second transfer protocol anywhere else.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sss/sss/internal/protocol"
	"github.com/sss/sss/internal/version"
)

// Client talks to one server over HTTPS or over the VPS-local Unix socket.
type Client struct {
	baseURL  string
	password string
	http     *http.Client
	local    bool
}

// New builds a remote client for a base URL.
func New(baseURL, password string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		password: password,
		http: &http.Client{
			Timeout: 0, // long uploads and downloads must not be cut off
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConnsPerHost:   8,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   15 * time.Second,
				ExpectContinueTimeout: 5 * time.Second,
				ResponseHeaderTimeout: 120 * time.Second,
			},
		},
	}
}

// NewLocal builds a client bound to the daemon's Unix socket. Filesystem
// permissions authorize these calls, so no password is sent.
func NewLocal(socketPath string) *Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &Client{
		baseURL: "http://localhost",
		local:   true,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", socketPath)
				},
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

// IsLocal reports whether this client uses the Unix socket.
func (c *Client) IsLocal() bool { return c.local }

// BaseURL reports the configured server URL.
func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	if !c.local {
		req.SetBasicAuth("sss", c.password)
	}
	return req, nil
}

// do performs a request and converts a failure response into a stable error.
func (c *Client) do(req *http.Request, expect ...int) (*http.Response, *protocol.Error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, protocol.Errorf(protocol.ErrNetwork, "%s %s: %v", req.Method, req.URL.Path, err)
	}
	for _, want := range expect {
		if resp.StatusCode == want {
			return resp, nil
		}
	}
	perr := parseError(resp)
	resp.Body.Close()
	return nil, perr
}

// parseError extracts the stable error from a failure response.
func parseError(resp *http.Response) *protocol.Error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	trimmed := strings.TrimSpace(string(body))
	var env protocol.Envelope
	if json.Unmarshal(body, &env) == nil && env.Err.Code != "" {
		e := env.Err
		return &e
	}
	if code, message, ok := strings.Cut(trimmed, ": "); ok && looksLikeCode(code) {
		return &protocol.Error{Code: code, Message: message, RequestID: resp.Header.Get("X-Request-Id")}
	}
	code := statusToCode(resp.StatusCode)
	message := trimmed
	if message == "" {
		message = resp.Status
	}
	return &protocol.Error{Code: code, Message: message, RequestID: resp.Header.Get("X-Request-Id")}
}

func looksLikeCode(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && r != '_' {
			return false
		}
	}
	return true
}

func statusToCode(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return protocol.ErrAuthInvalid
	case http.StatusNotFound:
		return protocol.ErrTransferNotFound
	case http.StatusGone:
		return protocol.ErrTransferExpired
	case http.StatusTooManyRequests:
		return protocol.ErrRateLimited
	case http.StatusConflict:
		return protocol.ErrStateConflict
	case http.StatusRequestEntityTooLarge:
		return protocol.ErrPayloadTooLarge
	case http.StatusInsufficientStorage:
		return protocol.ErrInsufficientStorage
	case http.StatusUpgradeRequired:
		return protocol.ErrProtocolMismatch
	case http.StatusUnprocessableEntity:
		return protocol.ErrInvalidRequest
	case http.StatusBadRequest:
		return protocol.ErrInvalidRequest
	default:
		if status >= 500 {
			return protocol.ErrInternal
		}
		return protocol.ErrNetwork
	}
}

func (c *Client) postJSON(ctx context.Context, path string, in, out any, headers map[string]string, expect ...int) *protocol.Error {
	data, err := json.Marshal(in)
	if err != nil {
		return protocol.Errorf(protocol.ErrInvalidRequest, "could not encode request: %v", err)
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(data))
	if err != nil {
		return protocol.Errorf(protocol.ErrInvalidRequest, "%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, perr := c.do(req, expect...)
	if perr != nil {
		return perr
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return protocol.Errorf(protocol.ErrNetwork, "could not decode response: %v", err)
	}
	return nil
}

// Info fetches server capabilities and verifies protocol compatibility.
func (c *Client) Info(ctx context.Context) (protocol.Info, *protocol.Error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/info", nil)
	if err != nil {
		return protocol.Info{}, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, perr := c.do(req, http.StatusOK)
	if perr != nil {
		return protocol.Info{}, perr
	}
	defer resp.Body.Close()
	var info protocol.Info
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return protocol.Info{}, protocol.Errorf(protocol.ErrNetwork, "could not decode /v1/info")
	}
	if perr := protocol.CheckCompatible(info.ProtocolVersion); perr != nil {
		return info, perr
	}
	return info, nil
}

// Metadata fetches the public document for a code.
func (c *Client) Metadata(ctx context.Context, code string) (protocol.TransferMetadata, *protocol.Error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/transfers/"+code, nil)
	if err != nil {
		return protocol.TransferMetadata{}, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, perr := c.do(req, http.StatusOK)
	if perr != nil {
		return protocol.TransferMetadata{}, perr
	}
	defer resp.Body.Close()
	var meta protocol.TransferMetadata
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return protocol.TransferMetadata{}, protocol.Errorf(protocol.ErrNetwork, "could not decode metadata")
	}
	return meta, nil
}

// CreateTransfer opens a resumable transfer.
func (c *Client) CreateTransfer(ctx context.Context, req protocol.CreateTransferRequest, idempotencyKey string) (protocol.CreateTransferResponse, *protocol.Error) {
	var out protocol.CreateTransferResponse
	headers := map[string]string{}
	if idempotencyKey != "" {
		headers["Idempotency-Key"] = idempotencyKey
	}
	perr := c.postJSON(ctx, "/v1/transfers", req, &out, headers, http.StatusCreated)
	return out, perr
}

// UploadOffset asks how many bytes of a segment the server already holds.
func (c *Client) UploadOffset(ctx context.Context, uploadPath string) (int64, int64, *protocol.Error) {
	req, err := c.newRequest(ctx, http.MethodHead, uploadPath, nil)
	if err != nil {
		return 0, 0, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	resp, perr := c.do(req, http.StatusOK)
	if perr != nil {
		return 0, 0, perr
	}
	defer resp.Body.Close()
	offset, _ := strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
	length, _ := strconv.ParseInt(resp.Header.Get("Upload-Length"), 10, 64)
	return offset, length, nil
}

// UploadPatch appends bytes at an exact offset and returns the accepted offset.
func (c *Client) UploadPatch(ctx context.Context, uploadPath string, offset int64, body io.Reader, length int64) (int64, *protocol.Error) {
	req, err := c.newRequest(ctx, http.MethodPatch, uploadPath, body)
	if err != nil {
		return 0, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err)
	}
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	req.ContentLength = length
	resp, perr := c.do(req, http.StatusNoContent)
	if perr != nil {
		return 0, perr
	}
	defer resp.Body.Close()
	accepted, _ := strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
	return accepted, nil
}

// Commit publishes a transfer and returns its code.
func (c *Client) Commit(ctx context.Context, transferID string, m protocol.Manifest) (protocol.CommitResponse, *protocol.Error) {
	var out protocol.CommitResponse
	perr := c.postJSON(ctx, "/v1/transfers/"+transferID+"/commit",
		protocol.CommitRequest{Manifest: m}, &out, nil, http.StatusOK, http.StatusCreated)
	return out, perr
}

// Claim opens a receive session for a code.
func (c *Client) Claim(ctx context.Context, code string) (protocol.ClaimResponse, *protocol.Error) {
	var out protocol.ClaimResponse
	perr := c.postJSON(ctx, "/v1/claims", protocol.ClaimRequest{Code: code}, &out, nil, http.StatusCreated)
	return out, perr
}

// SegmentReader opens a claim segment, optionally resuming from an offset.
func (c *Client) SegmentReader(ctx context.Context, path, token string, offset int64) (io.ReadCloser, *protocol.Error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err)
	}
	// The claim token replaces the base credential on this route.
	req.Header.Del("Authorization")
	req.Header.Set("Authorization", "Bearer "+token)
	expect := []int{http.StatusOK}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		expect = []int{http.StatusPartialContent, http.StatusOK}
	}
	resp, perr := c.do(req, expect...)
	if perr != nil {
		return nil, perr
	}
	if offset > 0 && resp.StatusCode == http.StatusOK {
		// The server ignored the range; the caller must restart from zero.
		resp.Body.Close()
		return nil, protocol.Errorf(protocol.ErrNetwork, "server did not honor the range request")
	}
	return resp.Body, nil
}

// SegmentRangeReader reads exactly length bytes of a segment from offset.
//
// Recovery from a failed verification uses this rather than the open-ended
// reader: re-requesting "everything from here" and then abandoning the stream
// makes the server produce, and the network carry, far more than the one group
// actually needed.
func (c *Client) SegmentRangeReader(ctx context.Context, path, token string, offset, length int64) (io.ReadCloser, *protocol.Error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err)
	}
	req.Header.Del("Authorization")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	resp, perr := c.do(req, http.StatusPartialContent, http.StatusOK)
	if perr != nil {
		return nil, perr
	}
	if resp.StatusCode == http.StatusOK && offset > 0 {
		resp.Body.Close()
		return nil, protocol.Errorf(protocol.ErrNetwork, "server did not honor the range request")
	}
	return resp.Body, nil
}

// OutboardBytes fetches a segment's verification tree in full. The tree is
// about 0.1% of the segment, so it is read into memory rather than streamed.
// A server that predates verification trees answers 404, and the caller falls
// back to whole-segment hashing.
func (c *Client) OutboardBytes(ctx context.Context, path, token string, max int64) ([]byte, *protocol.Error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err)
	}
	req.Header.Del("Authorization")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, perr := c.do(req, http.StatusOK)
	if perr != nil {
		return nil, perr
	}
	defer resp.Body.Close()
	// Bounded so a hostile or broken server cannot exhaust the receiver.
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, protocol.Errorf(protocol.ErrNetwork, "verification tree download interrupted: %v", err)
	}
	if int64(len(data)) != max {
		return nil, protocol.Errorf(protocol.ErrHashMismatch,
			"verification tree is %d bytes, expected %d", len(data), max)
	}
	return data, nil
}

// CompleteClaim records that a receive session finished.
func (c *Client) CompleteClaim(ctx context.Context, claimID, token string) *protocol.Error {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/claims/"+claimID+"/complete", nil)
	if err != nil {
		return protocol.Errorf(protocol.ErrInvalidRequest, "%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, perr := c.do(req, http.StatusNoContent)
	if perr != nil {
		return perr
	}
	resp.Body.Close()
	return nil
}

// LocalPath returns the existing payload path from the Unix socket.
func (c *Client) LocalPath(ctx context.Context, code string) (protocol.LocalClaim, *protocol.Error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/local/r/"+code, nil)
	if err != nil {
		return protocol.LocalClaim{}, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, perr := c.do(req, http.StatusOK)
	if perr != nil {
		return protocol.LocalClaim{}, perr
	}
	defer resp.Body.Close()
	var claim protocol.LocalClaim
	if err := json.NewDecoder(resp.Body).Decode(&claim); err != nil {
		return protocol.LocalClaim{}, protocol.Errorf(protocol.ErrNetwork, "could not decode local claim")
	}
	return claim, nil
}

// LocalStatus fetches the operational snapshot from the Unix socket.
func (c *Client) LocalStatus(ctx context.Context) (protocol.AdminStatus, *protocol.Error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/local/status", nil)
	if err != nil {
		return protocol.AdminStatus{}, protocol.Errorf(protocol.ErrInvalidRequest, "%v", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, perr := c.do(req, http.StatusOK)
	if perr != nil {
		return protocol.AdminStatus{}, perr
	}
	defer resp.Body.Close()
	var status protocol.AdminStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return protocol.AdminStatus{}, protocol.Errorf(protocol.ErrNetwork, "could not decode status")
	}
	return status, nil
}

// LocalCleanup runs an on-demand janitor pass through the Unix socket.
func (c *Client) LocalCleanup(ctx context.Context) (protocol.CleanupResult, *protocol.Error) {
	var out protocol.CleanupResult
	perr := c.postJSON(ctx, "/local/cleanup", struct{}{}, &out, nil, http.StatusOK)
	return out, perr
}

// LocalReachable reports whether the daemon socket answers with a compatible
// protocol. Locality is never inferred from DNS or hostname.
func LocalReachable(ctx context.Context, socketPath string) bool {
	c := NewLocal(socketPath)
	req, err := c.newRequest(ctx, http.MethodGet, "/local/healthz", nil)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := c.http.Do(req.WithContext(ctx))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		ProtocolVersion string `json:"protocol_version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false
	}
	return protocol.CheckCompatible(body.ProtocolVersion) == nil
}
