package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/conflicthq/scuttlebot/internal/api"
	"github.com/conflicthq/scuttlebot/internal/auth"
	"github.com/conflicthq/scuttlebot/internal/registry"
	"net/http/httptest"
	"path/filepath"
)

// newAdminStore creates an AdminStore backed by a temp file.
func newAdminStore(t *testing.T) *auth.AdminStore {
	t.Helper()
	s, err := auth.NewAdminStore(filepath.Join(t.TempDir(), "admins.json"))
	if err != nil {
		t.Fatalf("NewAdminStore: %v", err)
	}
	return s
}

// newTestServerWithAdmins creates a test server with admin auth configured.
func newTestServerWithAdmins(t *testing.T) (*httptest.Server, *auth.AdminStore) {
	t.Helper()
	admins := newAdminStore(t)
	if err := admins.Add("admin", "hunter2"); err != nil {
		t.Fatalf("Add admin: %v", err)
	}
	reg := registry.New(newMock(), []byte("test-signing-key"))
	srv := api.New(reg, []string{testToken}, nil, nil, admins, nil, nil, nil, "", testLog)
	return httptest.NewServer(srv.Handler()), admins
}

func TestLoginNoAdmins(t *testing.T) {
	// When admins is nil, login returns 404.
	reg := registry.New(newMock(), []byte("test-signing-key"))
	srv := api.New(reg, []string{testToken}, nil, nil, nil, nil, nil, nil, "", testLog)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := do(t, ts, "POST", "/login", map[string]any{"username": "admin", "password": "pw"}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 when no admins configured, got %d", resp.StatusCode)
	}
}

func TestLoginValidCredentials(t *testing.T) {
	ts, _ := newTestServerWithAdmins(t)
	defer ts.Close()

	resp := do(t, ts, "POST", "/login", map[string]any{"username": "admin", "password": "hunter2"}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["token"] == "" {
		t.Error("expected token in response")
	}
	if body["username"] != "admin" {
		t.Errorf("expected username=admin, got %q", body["username"])
	}
}

func TestLoginWrongPassword(t *testing.T) {
	ts, _ := newTestServerWithAdmins(t)
	defer ts.Close()

	resp := do(t, ts, "POST", "/login", map[string]any{"username": "admin", "password": "wrong"}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestLoginUnknownUser(t *testing.T) {
	ts, _ := newTestServerWithAdmins(t)
	defer ts.Close()

	resp := do(t, ts, "POST", "/login", map[string]any{"username": "nobody", "password": "hunter2"}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestLoginBadBody(t *testing.T) {
	ts, _ := newTestServerWithAdmins(t)
	defer ts.Close()

	resp := do(t, ts, "POST", "/login", "not-json", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	ts, _ := newTestServerWithAdmins(t)
	defer ts.Close()

	// 11 attempts from the same IP — the 11th should be rate-limited.
	var lastStatus int
	for i := 0; i < 11; i++ {
		resp := do(t, ts, "POST", "/login", map[string]any{"username": "admin", "password": "wrong"}, nil)
		lastStatus = resp.StatusCode
		resp.Body.Close()
	}
	if lastStatus != http.StatusTooManyRequests {
		t.Errorf("expected 429 on 11th attempt, got %d", lastStatus)
	}
}

// --- admin management endpoints ---

func TestAdminList(t *testing.T) {
	ts, _ := newTestServerWithAdmins(t)
	defer ts.Close()

	resp := do(t, ts, "GET", "/v1/admins", nil, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	admins := body["admins"].([]any)
	if len(admins) != 1 {
		t.Errorf("expected 1 admin, got %d", len(admins))
	}
}

func TestAdminAdd(t *testing.T) {
	ts, _ := newTestServerWithAdmins(t)
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/admins", map[string]any{"username": "bob", "password": "passw0rd"}, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}

	// List should now have 2.
	resp2 := do(t, ts, "GET", "/v1/admins", nil, authHeader())
	defer resp2.Body.Close()
	var body map[string]any
	json.NewDecoder(resp2.Body).Decode(&body)
	admins := body["admins"].([]any)
	if len(admins) != 2 {
		t.Errorf("expected 2 admins after add, got %d", len(admins))
	}
}

func TestAdminAddDuplicate(t *testing.T) {
	ts, _ := newTestServerWithAdmins(t)
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/admins", map[string]any{"username": "admin", "password": "pw"}, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 on duplicate, got %d", resp.StatusCode)
	}
}

func TestAdminAddMissingFields(t *testing.T) {
	ts, _ := newTestServerWithAdmins(t)
	defer ts.Close()

	resp := do(t, ts, "POST", "/v1/admins", map[string]any{"username": "bob"}, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 when password missing, got %d", resp.StatusCode)
	}
}

func TestAdminRemove(t *testing.T) {
	ts, admins := newTestServerWithAdmins(t)
	defer ts.Close()

	// Add a second admin first so we're not removing the only one.
	_ = admins.Add("bob", "pw")

	resp := do(t, ts, "DELETE", "/v1/admins/admin", nil, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}

	resp2 := do(t, ts, "GET", "/v1/admins", nil, authHeader())
	defer resp2.Body.Close()
	var body map[string]any
	json.NewDecoder(resp2.Body).Decode(&body)
	remaining := body["admins"].([]any)
	if len(remaining) != 1 {
		t.Errorf("expected 1 admin remaining, got %d", len(remaining))
	}
}

func TestAdminRemoveUnknown(t *testing.T) {
	ts, _ := newTestServerWithAdmins(t)
	defer ts.Close()

	resp := do(t, ts, "DELETE", "/v1/admins/nobody", nil, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestAdminSetPassword(t *testing.T) {
	ts, admins := newTestServerWithAdmins(t)
	defer ts.Close()

	resp := do(t, ts, "PUT", "/v1/admins/admin/password", map[string]any{"password": "newpass"}, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}

	if !admins.Authenticate("admin", "newpass") {
		t.Error("new password should authenticate")
	}
}

func TestAdminSetPasswordMissing(t *testing.T) {
	ts, _ := newTestServerWithAdmins(t)
	defer ts.Close()

	resp := do(t, ts, "PUT", "/v1/admins/admin/password", map[string]any{"password": ""}, authHeader())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestAdminEndpointsRequireAuth(t *testing.T) {
	ts, _ := newTestServerWithAdmins(t)
	defer ts.Close()

	endpoints := []struct{ method, path string }{
		{"GET", "/v1/admins"},
		{"POST", "/v1/admins"},
		{"DELETE", "/v1/admins/admin"},
		{"PUT", "/v1/admins/admin/password"},
	}
	for _, e := range endpoints {
		t.Run(e.method+" "+e.path, func(t *testing.T) {
			resp := do(t, ts, e.method, e.path, nil, nil)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d", resp.StatusCode)
			}
		})
	}
}
