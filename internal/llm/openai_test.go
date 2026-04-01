package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAISummarizeRetriesWithMaxCompletionTokens(t *testing.T) {
	t.Helper()

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		switch requests {
		case 1:
			if _, ok := body["max_tokens"]; !ok {
				t.Fatalf("first request missing max_tokens: %#v", body)
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","param":"max_tokens"}}`))
		case 2:
			if _, ok := body["max_completion_tokens"]; !ok {
				t.Fatalf("second request missing max_completion_tokens: %#v", body)
			}
			if _, ok := body["max_tokens"]; ok {
				t.Fatalf("second request still included max_tokens: %#v", body)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"relay smoke test succeeded"}}]}`))
		default:
			t.Fatalf("unexpected extra request %d", requests)
		}
	}))
	defer srv.Close()

	p := newOpenAIProvider("test-key", srv.URL, "gpt-5.4-mini", srv.Client())
	got, err := p.Summarize(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("Summarize returned error: %v", err)
	}
	if got != "relay smoke test succeeded" {
		t.Fatalf("Summarize = %q, want %q", got, "relay smoke test succeeded")
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want 2", requests)
	}
}

func TestShouldRetryWithMaxCompletionTokens(t *testing.T) {
	t.Helper()

	body := []byte(`{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","param":"max_tokens"}}`)
	if !shouldRetryWithMaxCompletionTokens(http.StatusBadRequest, body) {
		t.Fatalf("expected retry to be enabled")
	}
	if shouldRetryWithMaxCompletionTokens(http.StatusUnauthorized, body) {
		t.Fatalf("unexpected retry on unauthorized response")
	}
}
