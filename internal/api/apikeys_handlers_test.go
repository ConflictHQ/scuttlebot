package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/conflicthq/scuttlebot/internal/auth"
	"github.com/conflicthq/scuttlebot/internal/registry"
)

// newAPIKeyTestServer creates a server backed by a real APIKeyStore. The
// admin token "admin-tok" already exists in the store; tests can mint
// additional keys via the handlers and exercise the rotate / revoke flow.
func newAPIKeyTestServer(t *testing.T) (*httptest.Server, *auth.APIKeyStore) {
	t.Helper()
	keys := auth.TestStore("admin-tok")
	reg := registry.New(newStubProvisioner(), []byte("key"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(reg, keys, nil, nil, nil, nil, nil, nil, nil, "", false, false, log).Handler())
	t.Cleanup(srv.Close)
	return srv, keys
}

func apikeyDo(t *testing.T, srv *httptest.Server, method, path string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req, _ := http.NewRequest(method, srv.URL+path, &buf)
	req.Header.Set("Authorization", "Bearer admin-tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func TestHandleCreateAPIKey(t *testing.T) {
	srv, keys := newAPIKeyTestServer(t)

	resp := apikeyDo(t, srv, http.MethodPost, "/v1/api-keys", map[string]any{
		"name":   "ci-bot",
		"scopes": []string{"agents", "channels"},
		"team":   "alpha",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}
	var got createAPIKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Token == "" {
		t.Error("expected plaintext token in response")
	}
	if got.Name != "ci-bot" {
		t.Errorf("name = %q, want ci-bot", got.Name)
	}
	if got.Team != "alpha" {
		t.Errorf("team = %q, want alpha", got.Team)
	}
	if len(got.Scopes) != 2 {
		t.Errorf("scopes = %v, want 2", got.Scopes)
	}

	// The new token should authenticate immediately.
	if k := keys.Lookup(got.Token); k == nil {
		t.Error("returned token should authenticate")
	}
}

func TestHandleCreateAPIKeyMissingName(t *testing.T) {
	srv, _ := newAPIKeyTestServer(t)

	resp := apikeyDo(t, srv, http.MethodPost, "/v1/api-keys", map[string]any{
		"scopes": []string{"agents"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestHandleCreateAPIKeyMissingScopes(t *testing.T) {
	srv, _ := newAPIKeyTestServer(t)

	resp := apikeyDo(t, srv, http.MethodPost, "/v1/api-keys", map[string]any{
		"name": "ci-bot",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestHandleCreateAPIKeyUnknownScope(t *testing.T) {
	srv, _ := newAPIKeyTestServer(t)

	resp := apikeyDo(t, srv, http.MethodPost, "/v1/api-keys", map[string]any{
		"name":   "x",
		"scopes": []string{"superuser"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown scope, got %d", resp.StatusCode)
	}
}

func TestHandleCreateAPIKeyInvalidExpiresIn(t *testing.T) {
	srv, _ := newAPIKeyTestServer(t)

	resp := apikeyDo(t, srv, http.MethodPost, "/v1/api-keys", map[string]any{
		"name":       "x",
		"scopes":     []string{"read"},
		"expires_in": "not-a-duration",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestHandleCreateAPIKeyWithExpiry(t *testing.T) {
	srv, _ := newAPIKeyTestServer(t)

	resp := apikeyDo(t, srv, http.MethodPost, "/v1/api-keys", map[string]any{
		"name":       "tmp",
		"scopes":     []string{"read"},
		"expires_in": "1h",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}
	var got createAPIKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ExpiresAt == nil {
		t.Error("expected expires_at to be set")
	}
}

func TestHandleListAPIKeys(t *testing.T) {
	srv, _ := newAPIKeyTestServer(t)

	// Create one key so the list has at least 2 (admin + new).
	create := apikeyDo(t, srv, http.MethodPost, "/v1/api-keys", map[string]any{
		"name":   "list-me",
		"scopes": []string{"read"},
	})
	create.Body.Close()

	resp := apikeyDo(t, srv, http.MethodGet, "/v1/api-keys", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got []apiKeyListEntry
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) < 2 {
		t.Errorf("want >= 2 keys, got %d", len(got))
	}
	// Hash must never appear on a list entry — type lacks a Hash field, so this
	// is enforced by the struct shape. Verify Active flag is set.
	for _, k := range got {
		if k.Name == "list-me" && !k.Active {
			t.Error("newly created key should be Active")
		}
	}
}

func TestHandleRotateAPIKey(t *testing.T) {
	srv, keys := newAPIKeyTestServer(t)

	// Create a key, capture its token + id, rotate, ensure new token works
	// and old token does not.
	create := apikeyDo(t, srv, http.MethodPost, "/v1/api-keys", map[string]any{
		"name":   "rot",
		"scopes": []string{"read"},
	})
	defer create.Body.Close()
	var initial createAPIKeyResponse
	if err := json.NewDecoder(create.Body).Decode(&initial); err != nil {
		t.Fatalf("decode initial: %v", err)
	}
	oldToken := initial.Token
	if keys.Lookup(oldToken) == nil {
		t.Fatal("old token should authenticate before rotation")
	}

	resp := apikeyDo(t, srv, http.MethodPut, "/v1/api-keys/"+initial.ID+"/rotate", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got createAPIKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Token == "" {
		t.Fatal("rotation should return a new token")
	}
	if got.Token == oldToken {
		t.Error("rotation should produce a new token")
	}
	if keys.Lookup(oldToken) != nil {
		t.Error("old token should no longer authenticate")
	}
	if keys.Lookup(got.Token) == nil {
		t.Error("new token should authenticate")
	}
}

func TestHandleRotateAPIKeyNotFound(t *testing.T) {
	srv, _ := newAPIKeyTestServer(t)

	resp := apikeyDo(t, srv, http.MethodPut, "/v1/api-keys/no-such-id/rotate", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestHandleRevokeAPIKey(t *testing.T) {
	srv, keys := newAPIKeyTestServer(t)

	create := apikeyDo(t, srv, http.MethodPost, "/v1/api-keys", map[string]any{
		"name":   "revoke-me",
		"scopes": []string{"read"},
	})
	defer create.Body.Close()
	var initial createAPIKeyResponse
	json.NewDecoder(create.Body).Decode(&initial)

	resp := apikeyDo(t, srv, http.MethodDelete, "/v1/api-keys/"+initial.ID, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}
	if keys.Lookup(initial.Token) != nil {
		t.Error("revoked token should no longer authenticate")
	}

	// Second delete returns 404 (already revoked).
	resp2 := apikeyDo(t, srv, http.MethodDelete, "/v1/api-keys/"+initial.ID, nil)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("want 404 on second revoke, got %d", resp2.StatusCode)
	}
}

func TestHandleRevokeAPIKeyNotFound(t *testing.T) {
	srv, _ := newAPIKeyTestServer(t)

	resp := apikeyDo(t, srv, http.MethodDelete, "/v1/api-keys/missing", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestHandleAPIKeyInsufficientScope(t *testing.T) {
	// Reads-only scope cannot create or list api keys.
	keys := auth.TestStoreWithTeam("admin-tok", "read-tok",
		[]auth.Scope{auth.ScopeRead}, "")
	reg := registry.New(newStubProvisioner(), []byte("key"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(reg, keys, nil, nil, nil, nil, nil, nil, nil, "", false, false, log).Handler())
	defer srv.Close()

	for _, ep := range []struct{ method, path string }{
		{http.MethodGet, "/v1/api-keys"},
		{http.MethodPost, "/v1/api-keys"},
		{http.MethodDelete, "/v1/api-keys/x"},
	} {
		req, _ := http.NewRequest(ep.method, srv.URL+ep.path, nil)
		req.Header.Set("Authorization", "Bearer read-tok")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s: want 403, got %d", ep.method, ep.path, resp.StatusCode)
		}
	}
}
