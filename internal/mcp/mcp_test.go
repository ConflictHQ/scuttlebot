package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/conflicthq/scuttlebot/internal/mcp"
	"github.com/conflicthq/scuttlebot/internal/registry"
	"log/slog"
	"os"
)

var testLog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

const testToken = "test-mcp-token"

// --- mocks ---

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

type mockChannelLister struct {
	channels []mcp.ChannelInfo
}

func (m *mockChannelLister) ListChannels() ([]mcp.ChannelInfo, error) {
	return m.channels, nil
}

type mockSender struct {
	sent []string
}

func (m *mockSender) Send(_ context.Context, channel, msgType string, _ any) error {
	m.sent = append(m.sent, channel+"/"+msgType)
	return nil
}

type mockHistory struct {
	entries map[string][]mcp.HistoryEntry
}

func (m *mockHistory) Query(channel string, limit int) ([]mcp.HistoryEntry, error) {
	entries := m.entries[channel]
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

// --- test server setup ---

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	reg := registry.New(newMock(), []byte("test-signing-key"))
	channels := &mockChannelLister{channels: []mcp.ChannelInfo{
		{Name: "#fleet", Topic: "main coordination", Count: 3},
		{Name: "#task.abc", Count: 1},
	}}
	sender := &mockSender{}
	hist := &mockHistory{entries: map[string][]mcp.HistoryEntry{
		"#fleet": {
			{Nick: "agent-01", MessageType: "task.create", MessageID: "01HX", Raw: `{"v":1}`},
		},
	}}
	srv := mcp.New(reg, channels, []string{testToken}, testLog).
		WithSender(sender).
		WithHistory(hist)
	return httptest.NewServer(srv.Handler())
}

func rpc(t *testing.T, srv *httptest.Server, method string, params any, token string) map[string]any {
	t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		body["params"] = params
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+"/mcp", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return result
}

// --- tests ---

func TestAuthRequired(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := rpc(t, srv, "initialize", nil, "") // no token
	if resp["error"] == nil {
		t.Error("expected error for missing auth, got none")
	}
}

func TestAuthInvalid(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := rpc(t, srv, "initialize", nil, "wrong-token")
	if resp["error"] == nil {
		t.Error("expected error for invalid token")
	}
}

func TestInitialize(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := rpc(t, srv, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "1"},
	}, testToken)

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", resp)
	}
	if result["protocolVersion"] == nil {
		t.Error("missing protocolVersion in initialize response")
	}
}

func TestToolsList(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := rpc(t, srv, "tools/list", nil, testToken)
	result, _ := resp["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) == 0 {
		t.Error("expected at least one tool")
	}
	// Check all expected tool names are present.
	want := map[string]bool{
		"get_status": false, "list_channels": false,
		"register_agent": false, "send_message": false, "get_history": false,
	}
	for _, tool := range tools {
		m, _ := tool.(map[string]any)
		if name, ok := m["name"].(string); ok {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tool %q missing from tools/list", name)
		}
	}
}

func TestToolGetStatus(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := rpc(t, srv, "tools/call", map[string]any{
		"name":      "get_status",
		"arguments": map[string]any{},
	}, testToken)

	if resp["error"] != nil {
		t.Fatalf("unexpected rpc error: %v", resp["error"])
	}
	result := toolText(t, resp)
	if result == "" {
		t.Error("expected non-empty status text")
	}
}

func TestToolListChannels(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := rpc(t, srv, "tools/call", map[string]any{
		"name":      "list_channels",
		"arguments": map[string]any{},
	}, testToken)

	text := toolText(t, resp)
	if !contains(text, "#fleet") {
		t.Errorf("expected #fleet in channel list, got: %s", text)
	}
}

func TestToolRegisterAgent(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := rpc(t, srv, "tools/call", map[string]any{
		"name": "register_agent",
		"arguments": map[string]any{
			"nick":     "mcp-agent",
			"type":     "worker",
			"channels": []any{"#fleet"},
		},
	}, testToken)

	if isToolError(resp) {
		t.Fatalf("unexpected tool error: %s", toolText(t, resp))
	}
	text := toolText(t, resp)
	if !contains(text, "mcp-agent") {
		t.Errorf("expected nick in response, got: %s", text)
	}
	if !contains(text, "password") {
		t.Errorf("expected password in response, got: %s", text)
	}
}

func TestToolRegisterAgentMissingNick(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := rpc(t, srv, "tools/call", map[string]any{
		"name":      "register_agent",
		"arguments": map[string]any{},
	}, testToken)

	if !isToolError(resp) {
		t.Error("expected tool error for missing nick")
	}
}

func TestToolSendMessage(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := rpc(t, srv, "tools/call", map[string]any{
		"name": "send_message",
		"arguments": map[string]any{
			"channel": "#fleet",
			"type":    "task.update",
			"payload": map[string]any{"status": "done"},
		},
	}, testToken)

	if isToolError(resp) {
		t.Fatalf("unexpected tool error: %s", toolText(t, resp))
	}
}

func TestToolGetHistory(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := rpc(t, srv, "tools/call", map[string]any{
		"name": "get_history",
		"arguments": map[string]any{
			"channel": "#fleet",
			"limit":   float64(10),
		},
	}, testToken)

	if isToolError(resp) {
		t.Fatalf("unexpected tool error: %s", toolText(t, resp))
	}
	text := toolText(t, resp)
	if !contains(text, "#fleet") {
		t.Errorf("expected #fleet in history, got: %s", text)
	}
}

func TestUnknownTool(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := rpc(t, srv, "tools/call", map[string]any{
		"name":      "no_such_tool",
		"arguments": map[string]any{},
	}, testToken)

	if resp["error"] == nil {
		t.Error("expected rpc error for unknown tool")
	}
}

func TestUnknownMethod(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := rpc(t, srv, "no_such_method", nil, testToken)
	if resp["error"] == nil {
		t.Error("expected rpc error for unknown method")
	}
}

// --- helpers ---

func toolText(t *testing.T, resp map[string]any) string {
	t.Helper()
	result, _ := resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	item, _ := content[0].(map[string]any)
	text, _ := item["text"].(string)
	return text
}

func isToolError(resp map[string]any) bool {
	result, _ := resp["result"].(map[string]any)
	isErr, _ := result["isError"].(bool)
	return isErr
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
