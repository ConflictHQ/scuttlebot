package bridge

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxMessageBytes bounds a logical message, independently of IRC's line limit.
const MaxMessageBytes = 32 * 1024

// messageLines preserves one logical message for native clients while fitting
// its text into safe IRC trailing parameters. User newlines never become IRC
// commands, and every transport line retains its original target and sender.
func messageLines(channel, text, sender, metaType string, relay bool) ([]string, string, error) {
	if channel == "" || strings.ContainsAny(channel, " \t\r\n\x00") || strings.ContainsAny(sender, " \t\r\n\x00") {
		return nil, "", fmt.Errorf("bridge: invalid message target or sender")
	}
	if strings.ContainsAny(metaType, " ;\\\t\r\n\x00") {
		return nil, "", fmt.Errorf("bridge: invalid metadata type")
	}
	if strings.TrimSpace(text) == "" || len(text) > MaxMessageBytes || strings.ContainsRune(text, 0) || !utf8.ValidString(text) {
		return nil, "", fmt.Errorf("bridge: text must be nonempty UTF-8, at most %d bytes, without NUL", MaxMessageBytes)
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	tag := ""
	if metaType != "" {
		tag = "@+scuttlebot/meta-type=" + metaType + " "
	}
	prefix := tag + "PRIVMSG " + channel + " :"
	if sender != "" {
		if relay {
			prefix = tag + "RELAYMSG " + channel + " " + sender + " :"
		} else {
			prefix += "[" + sender + "] "
		}
	}
	budget := 510 - len(prefix)
	if budget > 350 {
		budget = 350
	}
	if budget < utf8.UTFMax {
		return nil, "", fmt.Errorf("bridge: message routing fields too long")
	}
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			out = append(out, prefix+" ")
			continue
		}
		for len(paragraph) > budget {
			cut := budget
			for cut > 0 && !utf8.RuneStart(paragraph[cut]) {
				cut--
			}
			if word := strings.LastIndexAny(paragraph[:cut], " \t"); word >= cut/2 {
				cut = word + 1
			}
			out = append(out, prefix+paragraph[:cut])
			paragraph = paragraph[cut:]
		}
		if paragraph != "" {
			out = append(out, prefix+paragraph)
		}
	}
	return out, text, nil
}
