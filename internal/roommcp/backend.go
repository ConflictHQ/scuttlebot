// Package roommcp exposes persistent room identities and mailboxes as MCP tools.
// It uses the existing HTTP API so all scope and team checks still apply.
package roommcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Backend interface {
	Call(context.Context, string, string, any, any) error
}

type HTTPBackend struct {
	URL    string
	Token  string
	Client *http.Client
}

func (b *HTTPBackend) Call(ctx context.Context, method, path string, body, result any) error {
	var data bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&data).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(b.URL, "/")+path, &data)
	if err != nil {
		return fmt.Errorf("room API: invalid request")
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	req.Header.Set("Content-Type", "application/json")
	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("room API: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not return an arbitrary proxy response, which might contain credentials.
		return &APIError{Status: resp.StatusCode}
	}
	if result == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(result)
}

type APIError struct{ Status int }

func (e *APIError) Error() string { return fmt.Sprintf("room API returned HTTP %d", e.Status) }

// HandlerTransport sends API calls through the same authenticated router without
// a loopback network connection. Only the caller's bearer token is forwarded.
type HandlerTransport struct{ Handler http.Handler }

func (t HandlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := &bufferResponse{header: make(http.Header), status: http.StatusOK}
	t.Handler.ServeHTTP(w, r)
	return &http.Response{StatusCode: w.status, Header: w.header, Body: io.NopCloser(&w.body), Request: r}, nil
}

type bufferResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
	wrote  bool
}

func (w *bufferResponse) Header() http.Header { return w.header }
func (w *bufferResponse) WriteHeader(status int) {
	if !w.wrote {
		w.status = status
		w.wrote = true
	}
}
func (w *bufferResponse) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(p)
}
