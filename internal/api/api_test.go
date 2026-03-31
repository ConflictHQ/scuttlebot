package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/conflicthq/scuttlebot/internal/api"
	"github.com/conflicthq/scuttlebot/internal/registry"
	"log/slog"
	"os"
)

var testLog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

// mockProvisioner for registry tests.
type mockProvisioner struct {
	mu       sync.Mutex
	accounts map[string]string
}

func newMock() *mockProvisioner {
	return &mockProvisioner{accounts: make(map[string]string)}
}

func (m *mockProvisioner) RegisterAccount(name, pass string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.accounts[name]; ok {
		return fmt.Errorf("ACCOUNT_EXISTS")
	}
	m.accounts[name] = pass
	return nil
}

func (m *mockProvisioner) ChangePassword(name, pass string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.accounts[name]; !ok {
		return fmt.Errorf("ACCOUNT_DOES_NOT_EXIST")
	}
	m.accounts[name] = pass
	return nil
}

const testToken = "test-api-token-abc123"

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	reg := registry.New(newMock(), []byte("test-signing-key"))
	srv := api.New(reg, []string{testToken}, testLog)
	return httptest.NewServer(srv.Handler())
}

func authHeader() http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+testToken)
	return h
}

func do(t *testing.T, srv *httptest.Server, method, path string, body any, headers http.Header) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req, err := http.NewRequest(method, srv.URL+path, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func TestAuthRequired(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	endpoints := []struct{ method, path string }{
		{"GET", "/v1/status"},
		{"GET", "/v1/agents"},
		{"POST", "/v1/agents/register"},
	}
	for _, e := range endpoints {
		t.Run(e.method+" "+e.path, func(t *testing.T) {
			resp := do(t, srv, e.method, e.path, nil, nil)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d", resp.StatusCode)
			}
		})
	}
}

func TestInvalidToken(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	h := http.Header{}
	h.Set("Authorization", "Bearer wrong-token")
	resp := do(t, srv, "GET", "/v1/status", nil, h)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestStatus(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := do(t, srv, "GET", "/v1/status", nil, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status: got %q, want ok", body["status"])
	}
}

func TestRegisterAndGet(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	// Register
	resp := do(t, srv, "POST", "/v1/agents/register", map[string]any{
		"nick":     "claude-01",
		"type":     "worker",
		"channels": []string{"#fleet"},
	}, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("register: expected 201, got %d", resp.StatusCode)
	}

	var regBody map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&regBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if regBody["credentials"] == nil {
		t.Error("credentials missing from response")
	}
	if regBody["payload"] == nil {
		t.Error("payload missing from response")
	}

	// Get
	resp2 := do(t, srv, "GET", "/v1/agents/claude-01", nil, authHeader())
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("get: expected 200, got %d", resp2.StatusCode)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	body := map[string]any{"nick": "agent-dup", "type": "worker"}
	do(t, srv, "POST", "/v1/agents/register", body, authHeader()).Body.Close()

	resp := do(t, srv, "POST", "/v1/agents/register", body, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 on duplicate, got %d", resp.StatusCode)
	}
}

func TestListAgents(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	for _, nick := range []string{"a1", "a2", "a3"} {
		do(t, srv, "POST", "/v1/agents/register", map[string]any{"nick": nick}, authHeader()).Body.Close()
	}

	resp := do(t, srv, "GET", "/v1/agents", nil, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	agents := body["agents"].([]any)
	if len(agents) != 3 {
		t.Errorf("expected 3 agents, got %d", len(agents))
	}
}

func TestRotate(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	do(t, srv, "POST", "/v1/agents/register", map[string]any{"nick": "rot-agent"}, authHeader()).Body.Close()

	resp := do(t, srv, "POST", "/v1/agents/rot-agent/rotate", nil, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("rotate: expected 200, got %d", resp.StatusCode)
	}
}

func TestRevoke(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	do(t, srv, "POST", "/v1/agents/register", map[string]any{"nick": "rev-agent"}, authHeader()).Body.Close()

	resp := do(t, srv, "POST", "/v1/agents/rev-agent/revoke", nil, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("revoke: expected 204, got %d", resp.StatusCode)
	}

	// Now get should 404.
	resp2 := do(t, srv, "GET", "/v1/agents/rev-agent", nil, authHeader())
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("get revoked: expected 404, got %d", resp2.StatusCode)
	}
}

func TestGetUnknown(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := do(t, srv, "GET", "/v1/agents/nobody", nil, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestRegisterMissingNick(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := do(t, srv, "POST", "/v1/agents/register", map[string]any{"type": "worker"}, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}
