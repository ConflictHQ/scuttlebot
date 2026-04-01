package sessionrelay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type BrokerCommand struct {
	Name    string
	Channel string
}

func ParseEnvChannels(primary, raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return normalizeChannels(primary, nil)
	}

	parts := strings.Split(raw, ",")
	return normalizeChannels(primary, parts)
}

func ChannelSlugs(channels []string) []string {
	out := make([]string, 0, len(channels))
	for _, channel := range channels {
		if slug := channelSlug(channel); slug != "" {
			out = append(out, slug)
		}
	}
	return out
}

func FormatChannels(channels []string) string {
	if len(channels) == 0 {
		return "(none)"
	}
	return strings.Join(normalizeChannels("", channels), ", ")
}

func WriteChannelStateFile(path, control string, channels []string) error {
	if path == "" {
		return nil
	}
	control = channelSlug(control)
	channels = normalizeChannels(control, channels)
	if len(channels) == 0 {
		return fmt.Errorf("sessionrelay: channel state requires at least one channel")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data := strings.Join([]string{
		"SCUTTLEBOT_CHANNEL=" + control,
		"SCUTTLEBOT_CHANNELS=" + strings.Join(ChannelSlugs(channels), ","),
		"",
	}, "\n")

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(data), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func RemoveChannelStateFile(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func ParseBrokerCommand(text string) (BrokerCommand, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return BrokerCommand{}, false
	}

	switch strings.ToLower(fields[0]) {
	case "/join":
		cmd := BrokerCommand{Name: "join"}
		if len(fields) > 1 {
			cmd.Channel = normalizeChannel(fields[1])
		}
		return cmd, true
	case "/part":
		cmd := BrokerCommand{Name: "part"}
		if len(fields) > 1 {
			cmd.Channel = normalizeChannel(fields[1])
		}
		return cmd, true
	case "/channels":
		return BrokerCommand{Name: "channels"}, true
	default:
		return BrokerCommand{}, false
	}
}
