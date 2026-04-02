package llm

import (
	"testing"
)

func models(ids ...string) []ModelInfo {
	out := make([]ModelInfo, len(ids))
	for i, id := range ids {
		out[i] = ModelInfo{ID: id, Name: id}
	}
	return out
}

func ids(ms []ModelInfo) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func TestNewModelFilterInvalidAllow(t *testing.T) {
	_, err := NewModelFilter([]string{"["}, nil)
	if err == nil {
		t.Fatal("expected error for invalid allow pattern, got nil")
	}
}

func TestNewModelFilterInvalidBlock(t *testing.T) {
	_, err := NewModelFilter(nil, []string{"["})
	if err == nil {
		t.Fatal("expected error for invalid block pattern, got nil")
	}
}

func TestFilterNoPatterns(t *testing.T) {
	f, err := NewModelFilter(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := models("gpt-4", "claude-3", "gemini-pro")
	got := f.Apply(input)
	if len(got) != len(input) {
		t.Errorf("no patterns: got %d models, want %d", len(got), len(input))
	}
}

func TestFilterAllowOnly(t *testing.T) {
	f, err := NewModelFilter([]string{"^claude"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := f.Apply(models("claude-3-sonnet", "gpt-4", "claude-haiku", "gemini-pro"))
	gotIDs := ids(got)
	want := []string{"claude-3-sonnet", "claude-haiku"}
	if len(gotIDs) != len(want) {
		t.Fatalf("allow-only: got %v, want %v", gotIDs, want)
	}
	for i, id := range gotIDs {
		if id != want[i] {
			t.Errorf("allow-only[%d]: got %q, want %q", i, id, want[i])
		}
	}
}

func TestFilterBlockOnly(t *testing.T) {
	f, err := NewModelFilter(nil, []string{"preview", "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	got := f.Apply(models("gpt-4", "gpt-4-preview", "claude-3", "claude-legacy"))
	gotIDs := ids(got)
	want := []string{"gpt-4", "claude-3"}
	if len(gotIDs) != len(want) {
		t.Fatalf("block-only: got %v, want %v", gotIDs, want)
	}
	for i, id := range gotIDs {
		if id != want[i] {
			t.Errorf("block-only[%d]: got %q, want %q", i, id, want[i])
		}
	}
}

func TestFilterAllowAndBlock(t *testing.T) {
	// Allow claude-*, block anything with "legacy".
	f, err := NewModelFilter([]string{"^claude"}, []string{"legacy"})
	if err != nil {
		t.Fatal(err)
	}
	got := f.Apply(models("claude-3", "claude-legacy", "gpt-4", "gemini"))
	gotIDs := ids(got)
	// Only claude-3 survives: claude-legacy is blocked, gpt-4/gemini not in allowlist.
	if len(gotIDs) != 1 || gotIDs[0] != "claude-3" {
		t.Errorf("allow+block: got %v, want [claude-3]", gotIDs)
	}
}

func TestFilterEmptyInput(t *testing.T) {
	f, err := NewModelFilter([]string{"^claude"}, []string{"legacy"})
	if err != nil {
		t.Fatal(err)
	}
	got := f.Apply(nil)
	if len(got) != 0 {
		t.Errorf("empty input: got %d models, want 0", len(got))
	}
}

func TestFilterBlockTakesPrecedenceOverAllow(t *testing.T) {
	// Pattern matches both allow and block — block wins.
	f, err := NewModelFilter([]string{"claude"}, []string{"claude-3"})
	if err != nil {
		t.Fatal(err)
	}
	got := f.Apply(models("claude-3", "claude-haiku"))
	gotIDs := ids(got)
	// claude-3 is blocked; claude-haiku passes allowlist.
	if len(gotIDs) != 1 || gotIDs[0] != "claude-haiku" {
		t.Errorf("block-over-allow: got %v, want [claude-haiku]", gotIDs)
	}
}

func TestFilterMultipleAllowPatterns(t *testing.T) {
	f, err := NewModelFilter([]string{"^claude", "^gemini"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := f.Apply(models("claude-3", "gpt-4", "gemini-pro", "llama"))
	gotIDs := ids(got)
	if len(gotIDs) != 2 {
		t.Fatalf("multi-allow: got %v, want [claude-3 gemini-pro]", gotIDs)
	}
}
