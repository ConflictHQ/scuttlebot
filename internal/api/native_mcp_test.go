package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/conflicthq/scuttlebot/internal/api"
	"github.com/conflicthq/scuttlebot/internal/auth"
	"github.com/conflicthq/scuttlebot/internal/registry"
	"github.com/conflicthq/scuttlebot/internal/roommcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpAuthTransport struct{ token string }

func (t mcpAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}

func TestNativeMCPPreservesScopesAndTeamBoundaries(t *testing.T) {
	keys := auth.TestStoreWithTeam("admin-token", teamToken, []auth.Scope{auth.ScopeAgents, auth.ScopeChannels, auth.ScopeChat}, "alpha")
	readToken, _, err := keys.Create("read-only", []auth.Scope{auth.ScopeChannels}, time.Time{}, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	store, err := roommcp.OpenStore(filepath.Join(t.TempDir(), "rooms.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	br := &teamScopeBridge{}
	reg := registry.New(newMock(), []byte("key"))
	s := api.New(reg, keys, br, nil, nil, nil, nil, nil, nil, "", false, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.SetRoomStore(store)
	host := httptest.NewServer(s.Handler())
	defer host.Close()
	resp, err := http.Get(host.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("anonymous status=%d", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connect := func(token string) *mcp.ClientSession {
		t.Helper()
		cl, err := mcp.NewClient(&mcp.Implementation{Name: "integration-test", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: host.URL + "/mcp", HTTPClient: &http.Client{Transport: mcpAuthTransport{token}}, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cl.Close() })
		return cl
	}
	cl := connect(teamToken)
	call := func(client *mcp.ClientSession, name, room string) *mcp.CallToolResult {
		t.Helper()
		r, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"session": "integration", "channel": room}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := call(cl, "open_session", "#team-alpha-dev"); r.IsError {
		t.Fatalf("own team denied: %+v", r)
	}
	if r := call(cl, "open_session", "#team-bravo-dev"); !r.IsError {
		t.Fatal("other team's room allowed")
	}
	if r := call(connect(readToken), "open_session", "#team-alpha-dev"); !r.IsError {
		t.Fatal("read-only token registered an agent")
	}
	if r := call(connect(readToken), "read_messages", "#team-alpha-dev"); !r.IsError {
		t.Fatal("another key read a private session")
	}
}
