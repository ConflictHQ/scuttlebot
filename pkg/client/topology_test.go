package client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/conflicthq/scuttlebot/pkg/client"
)

func TestTopologyClientCreateChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/channels" {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Name  string `json:"name"`
			Topic string `json:"topic"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"channel":     req.Name,
			"type":        "task",
			"supervision": "#general",
			"autojoin":    []string{"bridge", "scribe"},
		})
	}))
	defer srv.Close()

	tc := client.NewTopologyClient(srv.URL, "tok")
	info, err := tc.CreateChannel(context.Background(), "#task.gh-42", "GitHub issue #42")
	if err != nil {
		t.Fatal(err)
	}
	if info.Channel != "#task.gh-42" {
		t.Errorf("Channel = %q, want #task.gh-42", info.Channel)
	}
	if info.Type != "task" {
		t.Errorf("Type = %q, want task", info.Type)
	}
	if info.Supervision != "#general" {
		t.Errorf("Supervision = %q, want #general", info.Supervision)
	}
	if len(info.Autojoin) != 2 {
		t.Errorf("Autojoin = %v, want 2 entries", info.Autojoin)
	}
}

func TestTopologyClientGetTopology(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/topology" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"static_channels": []string{"#general", "#alerts"},
			"types": []map[string]any{
				{"name": "task", "prefix": "task.", "autojoin": []string{"bridge"}},
			},
		})
	}))
	defer srv.Close()

	tc := client.NewTopologyClient(srv.URL, "tok")
	statics, types, err := tc.GetTopology(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(statics) != 2 || statics[0] != "#general" {
		t.Errorf("static_channels = %v", statics)
	}
	if len(types) != 1 || types[0].Name != "task" {
		t.Errorf("types = %v", types)
	}
}

func TestTopologyClientDropChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "wrong method", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/v1/topology/channels/task.gh-42" {
			http.Error(w, "wrong path: "+r.URL.Path, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	tc := client.NewTopologyClient(srv.URL, "tok")
	if err := tc.DropChannel(context.Background(), "#task.gh-42"); err != nil {
		t.Fatal(err)
	}
}
