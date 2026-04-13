package passgen

import (
	"strings"
	"testing"
)

func TestGenerateDefaults(t *testing.T) {
	pw, err := Generate(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pw) != 24 {
		t.Errorf("expected length 24, got %d", len(pw))
	}
	for _, c := range pw {
		if !strings.ContainsRune(alphanumChars, c) {
			t.Errorf("char %q not in alphanum charset", c)
		}
	}
}

func TestGenerateHex(t *testing.T) {
	pw, err := Generate(&Options{Length: 32, Charset: Hex})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pw) != 32 {
		t.Errorf("expected length 32, got %d", len(pw))
	}
	for _, c := range pw {
		if !strings.ContainsRune(hexChars, c) {
			t.Errorf("char %q not in hex charset", c)
		}
	}
}

func TestGenerateAlpha(t *testing.T) {
	pw, err := Generate(&Options{Length: 50, Charset: Alpha})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pw) != 50 {
		t.Errorf("expected length 50, got %d", len(pw))
	}
	for _, c := range pw {
		if !strings.ContainsRune(alphaChars, c) {
			t.Errorf("char %q not in alpha charset", c)
		}
	}
}

func TestGenerateASCII(t *testing.T) {
	pw, err := Generate(&Options{Length: 100, Charset: ASCII})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pw) != 100 {
		t.Errorf("expected length 100, got %d", len(pw))
	}
	excluded := `'"` + "`\\"
	for _, c := range pw {
		if c < 33 || c > 126 {
			t.Errorf("char %q (0x%x) outside printable ASCII range", c, c)
		}
		if strings.ContainsRune(excluded, c) {
			t.Errorf("char %q should be excluded from ASCII charset", c)
		}
	}
}

func TestGenerateCustomLength(t *testing.T) {
	lengths := []int{1, 8, 64, 256}
	for _, l := range lengths {
		pw, err := Generate(&Options{Length: l})
		if err != nil {
			t.Fatalf("length %d: unexpected error: %v", l, err)
		}
		if len(pw) != l {
			t.Errorf("expected length %d, got %d", l, len(pw))
		}
	}
}

func TestGenerateLengthZeroUsesDefault(t *testing.T) {
	// Length 0 in Options means "use default" (Go zero-value idiom).
	pw, err := Generate(&Options{Length: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pw) != 24 {
		t.Errorf("expected default length 24, got %d", len(pw))
	}
}

func TestGenerateLengthTooSmall(t *testing.T) {
	tests := []int{-1, -100}
	for _, l := range tests {
		_, err := Generate(&Options{Length: l})
		if err == nil {
			t.Errorf("length %d: expected error, got nil", l)
		}
	}
}

func TestGenerateUniqueness(t *testing.T) {
	// Two sequential calls should produce different passwords.
	// Collision probability for 24-char alphanum is negligible.
	a, _ := Generate(nil)
	b, _ := Generate(nil)
	if a == b {
		t.Errorf("two sequential Generate calls returned identical passwords: %q", a)
	}
}

func TestParseCharset(t *testing.T) {
	valid := []struct {
		input string
		want  Charset
	}{
		{"hex", Hex},
		{"alpha", Alpha},
		{"alphanum", Alphanum},
		{"ascii", ASCII},
	}
	for _, tt := range valid {
		got, err := ParseCharset(tt.input)
		if err != nil {
			t.Errorf("ParseCharset(%q): unexpected error: %v", tt.input, err)
		}
		if got != tt.want {
			t.Errorf("ParseCharset(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseCharsetInvalid(t *testing.T) {
	invalid := []string{"", "HEX", "Alphanum", "base64", "unicode", "foo"}
	for _, s := range invalid {
		_, err := ParseCharset(s)
		if err == nil {
			t.Errorf("ParseCharset(%q): expected error, got nil", s)
		}
	}
}
