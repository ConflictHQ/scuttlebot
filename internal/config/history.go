package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// HistoryEntry describes a single config snapshot in the history directory.
type HistoryEntry struct {
	// Filename is the base name of the snapshot file (e.g. "scuttlebot.yaml.20260402-143022").
	Filename string `json:"filename"`

	// Timestamp is when the snapshot was taken, parsed from the filename.
	Timestamp time.Time `json:"timestamp"`

	// Size is the file size in bytes.
	Size int64 `json:"size"`
}

const historyTimestampFormat = "20060102-150405"

// SnapshotConfig copies the file at configPath into historyDir, naming it
// "<basename>.<timestamp>". It creates historyDir if it does not exist.
// It is a no-op if configPath does not exist yet.
func SnapshotConfig(historyDir, configPath string) error {
	src, err := os.Open(configPath)
	if os.IsNotExist(err) {
		return nil // nothing to snapshot
	}
	if err != nil {
		return fmt.Errorf("config history: open %s: %w", configPath, err)
	}
	defer src.Close()

	if err := os.MkdirAll(historyDir, 0o700); err != nil {
		return fmt.Errorf("config history: mkdir %s: %w", historyDir, err)
	}

	base := filepath.Base(configPath)
	stamp := time.Now().Format(historyTimestampFormat)
	dst := filepath.Join(historyDir, base+"."+stamp)

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("config history: create snapshot %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, src); err != nil {
		return fmt.Errorf("config history: write snapshot %s: %w", dst, err)
	}
	return nil
}

// PruneHistory removes the oldest snapshots in historyDir until at most keep
// files remain. It only considers files whose names start with base (the
// basename of the config file). keep ≤ 0 means unlimited (no pruning).
func PruneHistory(historyDir, base string, keep int) error {
	if keep <= 0 {
		return nil
	}
	entries, err := listHistory(historyDir, base)
	if err != nil {
		return err
	}
	for len(entries) > keep {
		oldest := entries[0]
		if err := os.Remove(filepath.Join(historyDir, oldest.Filename)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("config history: remove %s: %w", oldest.Filename, err)
		}
		entries = entries[1:]
	}
	return nil
}

// ListHistory returns all snapshots for base (the config file basename) in
// historyDir, sorted oldest-first.
func ListHistory(historyDir, base string) ([]HistoryEntry, error) {
	return listHistory(historyDir, base)
}

func listHistory(historyDir, base string) ([]HistoryEntry, error) {
	des, err := os.ReadDir(historyDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config history: readdir %s: %w", historyDir, err)
	}
	var out []HistoryEntry
	prefix := base + "."
	for _, de := range des {
		if de.IsDir() || !strings.HasPrefix(de.Name(), prefix) {
			continue
		}
		stamp := strings.TrimPrefix(de.Name(), prefix)
		t, err := time.ParseInLocation(historyTimestampFormat, stamp, time.Local)
		if err != nil {
			continue // skip files with non-matching suffix
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		out = append(out, HistoryEntry{
			Filename:  de.Name(),
			Timestamp: t,
			Size:      info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.Before(out[j].Timestamp)
	})
	return out, nil
}
