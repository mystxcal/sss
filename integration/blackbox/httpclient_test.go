package blackbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sss/sss/internal/protocol"
)

// httpDo performs a request against the public listener.
func (s *server) do(req *http.Request) *http.Response {
	s.t.Helper()
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	return resp
}

func (s *server) request(method, path string, body io.Reader) *http.Request {
	s.t.Helper()
	req, err := http.NewRequest(method, s.baseURL+path, body)
	if err != nil {
		s.t.Fatalf("build request: %v", err)
	}
	req.SetBasicAuth("sss", testPassword)
	return req
}

// uploadFiles performs a multipart send exactly as `curl -F` would.
func (s *server) uploadFiles(files map[string][]byte, note string, ttl string) *http.Response {
	s.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, content := range files {
		w, err := mw.CreateFormFile("file", name)
		if err != nil {
			s.t.Fatalf("create form file: %v", err)
		}
		if _, err := w.Write(content); err != nil {
			s.t.Fatalf("write form file: %v", err)
		}
	}
	if note != "" {
		_ = mw.WriteField("note", note)
	}
	if ttl != "" {
		_ = mw.WriteField("ttl", ttl)
	}
	if err := mw.Close(); err != nil {
		s.t.Fatalf("close multipart: %v", err)
	}
	req := s.request(http.MethodPost, "/s", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return s.do(req)
}

// sendFile uploads one file and returns the allocated code.
func (s *server) sendFile(name string, content []byte, note, ttl string) string {
	s.t.Helper()
	resp := s.uploadFiles(map[string][]byte{name: content}, note, ttl)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		s.t.Fatalf("send %s: status %d body %s", name, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return strings.TrimSpace(string(body))
}

// receive downloads a code and returns the status and body.
func (s *server) receive(code, format string) (int, []byte, http.Header) {
	s.t.Helper()
	path := "/r/" + code
	if format != "" {
		path += "?format=" + format
	}
	resp := s.do(s.request(http.MethodGet, path, nil))
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body, resp.Header
}

// errorBody decodes a stable error from a response body.
func decodeError(t *testing.T, resp *http.Response) protocol.Error {
	t.Helper()
	body, _ := io.ReadAll(resp.Body)
	trimmed := strings.TrimSpace(string(body))
	var env protocol.Envelope
	if json.Unmarshal(body, &env) == nil && env.Err.Code != "" {
		return env.Err
	}
	code, message, ok := strings.Cut(trimmed, ": ")
	if !ok {
		t.Fatalf("unparsable error body %q", trimmed)
	}
	return protocol.Error{Code: code, Message: message}
}

// localGet calls the Unix-socket listener.
func (s *server) localGet(path, accept string) (int, []byte) {
	s.t.Helper()
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", s.socket)
			},
		},
		Timeout: 30 * time.Second,
	}
	req, err := http.NewRequest(http.MethodGet, "http://localhost"+path, nil)
	if err != nil {
		s.t.Fatalf("build local request: %v", err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := client.Do(req)
	if err != nil {
		s.t.Fatalf("local request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// localPost calls a local mutating endpoint.
func (s *server) localPost(path string) (int, []byte) {
	s.t.Helper()
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", s.socket)
			},
		},
		Timeout: 30 * time.Second,
	}
	resp, err := client.Post("http://localhost"+path, "application/json", strings.NewReader("{}"))
	if err != nil {
		s.t.Fatalf("local request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}
