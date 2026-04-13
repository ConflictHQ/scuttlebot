package sessionrelay

import (
	"context"
	"encoding/json"
	"time"
)

// FilteredConnector wraps a Connector and adds level-aware posting via
// PostAtLevel. Per-channel resolution settings control which messages
// reach which channels. All other Connector methods delegate directly
// to the inner connector.
type FilteredConnector struct {
	inner       Connector
	resolutions map[string]Resolution // channel -> resolution; missing = defaultRes
	defaultRes  Resolution
}

// NewFilteredConnector wraps conn with per-channel resolution filtering.
// channelRes maps normalised channel names (e.g. "#general") to their resolution.
// defaultRes is used for channels not in the map.
func NewFilteredConnector(conn Connector, channelRes map[string]Resolution, defaultRes Resolution) *FilteredConnector {
	if channelRes == nil {
		channelRes = make(map[string]Resolution)
	}
	return &FilteredConnector{inner: conn, resolutions: channelRes, defaultRes: defaultRes}
}

// SetResolution sets the resolution for a channel at runtime.
func (f *FilteredConnector) SetResolution(channel string, res Resolution) {
	f.resolutions[normalizeChannel(channel)] = res
}

// PostAtLevel posts to all channels whose resolution accepts the given level.
// Channels that don't accept the level are silently skipped.
func (f *FilteredConnector) PostAtLevel(ctx context.Context, level Level, text string, meta json.RawMessage) error {
	for _, ch := range f.inner.Channels() {
		res, ok := f.resolutions[ch]
		if !ok {
			res = f.defaultRes
		}
		if !res.Accepts(level) {
			continue
		}
		if len(meta) > 0 {
			if err := f.inner.PostToWithMeta(ctx, ch, text, meta); err != nil {
				return err
			}
		} else {
			if err := f.inner.PostTo(ctx, ch, text); err != nil {
				return err
			}
		}
	}
	return nil
}

// Delegate all Connector methods to inner.

func (f *FilteredConnector) Connect(ctx context.Context) error {
	return f.inner.Connect(ctx)
}

func (f *FilteredConnector) Post(ctx context.Context, text string) error {
	return f.inner.Post(ctx, text)
}

func (f *FilteredConnector) PostTo(ctx context.Context, ch, text string) error {
	return f.inner.PostTo(ctx, ch, text)
}

func (f *FilteredConnector) PostWithMeta(ctx context.Context, text string, meta json.RawMessage) error {
	return f.inner.PostWithMeta(ctx, text, meta)
}

func (f *FilteredConnector) PostToWithMeta(ctx context.Context, ch, text string, meta json.RawMessage) error {
	return f.inner.PostToWithMeta(ctx, ch, text, meta)
}

func (f *FilteredConnector) MessagesSince(ctx context.Context, since time.Time) ([]Message, error) {
	return f.inner.MessagesSince(ctx, since)
}

func (f *FilteredConnector) Touch(ctx context.Context) error {
	return f.inner.Touch(ctx)
}

func (f *FilteredConnector) JoinChannel(ctx context.Context, ch string) error {
	return f.inner.JoinChannel(ctx, ch)
}

func (f *FilteredConnector) PartChannel(ctx context.Context, ch string) error {
	return f.inner.PartChannel(ctx, ch)
}

func (f *FilteredConnector) Channels() []string {
	return f.inner.Channels()
}

func (f *FilteredConnector) ControlChannel() string {
	return f.inner.ControlChannel()
}

func (f *FilteredConnector) Close(ctx context.Context) error {
	return f.inner.Close(ctx)
}
