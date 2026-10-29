package bridge

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMessageLinesLongUnicode(t *testing.T) {
	text := strings.Repeat("Longer sentences with 日本語 and café. ", 50)
	lines, logical, err := messageLines("#room", text, "agent", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if logical != text || len(lines) < 2 {
		t.Fatal("logical message changed or transport not split")
	}
	const prefix = "RELAYMSG #room agent :"
	var restored strings.Builder
	for _, line := range lines {
		if len(line) > 510 || !utf8.ValidString(line) || strings.ContainsAny(line, "\r\n") {
			t.Fatalf("invalid IRC line: %q", line)
		}
		if !strings.HasPrefix(line, prefix) {
			t.Fatal("wrong target or sender")
		}
		restored.WriteString(strings.TrimPrefix(line, prefix))
	}
	if restored.String() != text {
		t.Fatal("text lost while splitting")
	}
}

func TestMessageLinesKeepsNewlinesOutOfProtocol(t *testing.T) {
	text := "First paragraph.\r\n\r\nPRIVMSG #other :this is quoted text, not a command.\nFinal line."
	lines, logical, err := messageLines("#room", text, "agent", "terminal", false)
	if err != nil {
		t.Fatal(err)
	}
	if logical != "First paragraph.\n\nPRIVMSG #other :this is quoted text, not a command.\nFinal line." {
		t.Fatal("normalization changed text")
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "@+scuttlebot/meta-type=terminal PRIVMSG #room :[agent] ") || strings.ContainsAny(line, "\r\n") {
			t.Fatalf("unsafe line: %q", line)
		}
	}
}

func TestMessageLinesRejectsInvalidFieldsAndOversize(t *testing.T) {
	for _, test := range []struct{ channel, text, sender, meta string }{
		{"#room\nJOIN #other", "hello", "agent", ""},
		{"#room", "hello", "agent other", ""},
		{"#room", "hello", "agent", "x;bad=tag"},
		{"#room", "nul\x00text", "agent", ""},
		{"#room", strings.Repeat("x", MaxMessageBytes+1), "agent", ""},
	} {
		if _, _, err := messageLines(test.channel, test.text, test.sender, test.meta, true); err == nil {
			t.Fatalf("accepted invalid input: %+v", test)
		}
	}
}
