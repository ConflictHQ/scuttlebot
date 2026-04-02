package apiclient_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/conflicthq/scuttlebot/cmd/scuttlectl/internal/apiclient"
)

func newServer(t *testing.T, handler http.Handler) (*httptest.Server, *apiclient.Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, apiclient.New(srv.URL, "test-token")
}

func TestStatus(t *testing.T) {
	srv, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		assertBearer(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	_ = srv

	raw, err := client.Status()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "ok" {
		t.Errorf("status: got %q", got["status"])
	}
}

func TestListAgents(t *testing.T) {
	_, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agents":[{"nick":"claude-1"}]}`))
	}))

	raw, err := client.ListAgents()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Error("expected non-empty response")
	}
}

func TestGetAgent(t *testing.T) {
	_, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		if r.URL.Path != "/v1/agents/claude-1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nick":"claude-1","type":"worker"}`))
	}))

	raw, err := client.GetAgent("claude-1")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["nick"] != "claude-1" {
		t.Errorf("nick: got %q", got["nick"])
	}
}

func TestRegisterAgent(t *testing.T) {
	var gotBody map[string]any
	_, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"nick":"claude-1","credentials":{"passphrase":"secret"}}`))
	}))

	raw, err := client.RegisterAgent("claude-1", "worker", []string{"#general"})
	if err != nil {
		t.Fatal(err)
	}
	if raw == nil {
		t.Error("expected response body")
	}
	if gotBody["nick"] != "claude-1" {
		t.Errorf("body nick: got %v", gotBody["nick"])
	}
	if gotBody["type"] != "worker" {
		t.Errorf("body type: got %v", gotBody["type"])
	}
}

func TestRevokeAgent(t *testing.T) {
	called := false
	_, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		if r.URL.Path == "/v1/agents/claude-1/revoke" && r.Method == http.MethodPost {
			called = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		} else {
			http.NotFound(w, r)
		}
	}))

	if err := client.RevokeAgent("claude-1"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("revoke endpoint not called")
	}
}

func TestRotateAgent(t *testing.T) {
	_, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"passphrase":"newpass"}`))
	}))

	raw, err := client.RotateAgent("claude-1")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["passphrase"] != "newpass" {
		t.Errorf("passphrase: got %q", got["passphrase"])
	}
}

func TestDeleteAgent(t *testing.T) {
	called := false
	_, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/agents/claude-1" {
			called = true
			w.WriteHeader(http.StatusNoContent)
		} else {
			http.NotFound(w, r)
		}
	}))

	if err := client.DeleteAgent("claude-1"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("delete endpoint not called")
	}
}

func TestListChannels(t *testing.T) {
	_, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"channels":["#general","#ops"]}`))
	}))

	raw, err := client.ListChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Error("expected non-empty response")
	}
}

func TestDeleteChannel(t *testing.T) {
	called := false
	_, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/channels/general" {
			called = true
			w.WriteHeader(http.StatusNoContent)
		} else {
			http.NotFound(w, r)
		}
	}))

	// should strip the leading #
	if err := client.DeleteChannel("#general"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("delete channel endpoint not called")
	}
}

func TestGetLLMBackend(t *testing.T) {
	_, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"backends":[{"name":"anthropic","backend":"anthropic"},{"name":"ollama","backend":"ollama"}]}`))
	}))

	raw, err := client.GetLLMBackend("ollama")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["name"] != "ollama" {
		t.Errorf("name: got %q", got["name"])
	}
}

func TestGetLLMBackendNotFound(t *testing.T) {
	_, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"backends":[]}`))
	}))

	_, err := client.GetLLMBackend("nonexistent")
	if err == nil {
		t.Error("expected error for missing backend, got nil")
	}
}

func TestAPIError(t *testing.T) {
	_, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	}))

	_, err := client.Status()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "API error 401: invalid token" {
		t.Errorf("error message: got %q", err.Error())
	}
}

func TestAddAdmin(t *testing.T) {
	var gotBody map[string]string
	_, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"username":"alice"}`))
	}))

	raw, err := client.AddAdmin("alice", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if raw == nil {
		t.Error("expected response")
	}
	if gotBody["username"] != "alice" || gotBody["password"] != "hunter2" {
		t.Errorf("body: got %v", gotBody)
	}
}

func TestRemoveAdmin(t *testing.T) {
	called := false
	_, client := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertBearer(t, r)
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/admins/alice" {
			called = true
			w.WriteHeader(http.StatusNoContent)
		} else {
			http.NotFound(w, r)
		}
	}))

	if err := client.RemoveAdmin("alice"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("remove admin endpoint not called")
	}
}

func assertBearer(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("Authorization") != "Bearer test-token" {
		t.Errorf("Authorization header: got %q", r.Header.Get("Authorization"))
	}
}
