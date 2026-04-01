package scribe

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EntryKind describes how a log entry was parsed.
type EntryKind string

const (
	EntryKindEnvelope EntryKind = "envelope" // parsed as a valid JSON envelope
	EntryKindRaw      EntryKind = "raw"      // could not be parsed, logged as-is
)

// Entry is a single structured log record written by scribe.
type Entry struct {
	At          time.Time `json:"at"`
	Channel     string    `json:"channel"`
	Nick        string    `json:"nick"`
	Kind        EntryKind `json:"kind"`
	MessageType string    `json:"message_type,omitempty"` // envelope type if Kind == envelope
	MessageID   string    `json:"message_id,omitempty"`   // envelope ID if Kind == envelope
	Raw         string    `json:"raw"`
}

// Store is the storage backend for scribe log entries.
type Store interface {
	Append(entry Entry) error
	Query(channel string, limit int) ([]Entry, error)
}

// MemoryStore is an in-memory Store used for testing.
type MemoryStore struct {
	mu      sync.RWMutex
	entries []Entry
}

func (s *MemoryStore) Append(entry Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
	return nil
}

func (s *MemoryStore) Query(channel string, limit int) ([]Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []Entry
	for _, e := range s.entries {
		if channel == "" || e.Channel == channel {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// All returns all entries (test helper).
func (s *MemoryStore) All() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// ---------------------------------------------------------------------------
// FileStore
// ---------------------------------------------------------------------------

// FileStoreConfig controls FileStore behaviour.
type FileStoreConfig struct {
	Dir        string // base directory; created on first write if absent
	Format     string // "jsonl" (default) | "csv" | "text"
	Rotation   string // "none" (default) | "daily" | "weekly" | "size"
	MaxSizeMB  int    // size rotation threshold in MiB; 0 = no limit
	PerChannel bool   // true: one file per channel; false: single combined file
	MaxAgeDays int    // prune rotated files older than N days; 0 = keep all
}

// FileStore writes log entries to rotating files on disk.
// It is safe for concurrent use.
type FileStore struct {
	cfg   FileStoreConfig
	mu    sync.Mutex
	files map[string]*openFile // key: sanitized channel name or "_all"
}

type openFile struct {
	f      *os.File
	size   int64
	bucket string // date bucket for time-based rotation ("YYYY-MM-DD", "YYYY-Www")
	path   string // absolute path of the current file (for size rotation)
}

// NewFileStore creates a FileStore with the given config.
// Defaults: Format="jsonl", Rotation="none".
func NewFileStore(cfg FileStoreConfig) *FileStore {
	if cfg.Format == "" {
		cfg.Format = "jsonl"
	}
	if cfg.Rotation == "" {
		cfg.Rotation = "none"
	}
	return &FileStore{
		cfg:   cfg,
		files: make(map[string]*openFile),
	}
}

func (s *FileStore) Append(entry Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.cfg.Dir, 0755); err != nil {
		return fmt.Errorf("scribe filestore: mkdir %s: %w", s.cfg.Dir, err)
	}

	key := "_all"
	if s.cfg.PerChannel {
		key = sanitizeChannel(entry.Channel)
	}

	of, err := s.getFile(key, entry.At)
	if err != nil {
		return err
	}

	line, err := s.formatEntry(entry)
	if err != nil {
		return err
	}

	n, err := fmt.Fprintln(of.f, line)
	if err != nil {
		return fmt.Errorf("scribe filestore: write: %w", err)
	}
	of.size += int64(n)
	return nil
}

// getFile returns the open file for the given key, rotating if necessary.
// Caller must hold s.mu.
func (s *FileStore) getFile(key string, now time.Time) (*openFile, error) {
	of := s.files[key]
	bucket := s.timeBucket(now)

	if of != nil {
		needRotate := false
		switch s.cfg.Rotation {
		case "daily", "weekly":
			needRotate = of.bucket != bucket
		case "size":
			if s.cfg.MaxSizeMB > 0 {
				needRotate = of.size >= int64(s.cfg.MaxSizeMB)*1024*1024
			}
		}
		if needRotate {
			_ = of.f.Close()
			if s.cfg.Rotation == "size" {
				s.shiftSizeBackups(of.path)
			}
			of = nil
			delete(s.files, key)
		}
	}

	if of == nil {
		path := s.filePath(key, now)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("scribe filestore: open %s: %w", path, err)
		}
		var size int64
		if fi, err := f.Stat(); err == nil {
			size = fi.Size()
		}
		of = &openFile{f: f, size: size, bucket: bucket, path: path}
		s.files[key] = of
	}
	return of, nil
}

// shiftSizeBackups renames path → path.1, path.1 → path.2 … up to .10.
func (s *FileStore) shiftSizeBackups(path string) {
	for i := 9; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
	}
	_ = os.Rename(path, path+".1")
}

func (s *FileStore) timeBucket(t time.Time) string {
	switch s.cfg.Rotation {
	case "daily":
		return t.Format("2006-01-02")
	case "weekly":
		year, week := t.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", year, week)
	case "monthly":
		return t.Format("2006-01")
	case "yearly":
		return t.Format("2006")
	default:
		return "current"
	}
}

func (s *FileStore) filePath(key string, now time.Time) string {
	extMap := map[string]string{"jsonl": ".jsonl", "csv": ".csv", "text": ".log"}
	ext, ok := extMap[s.cfg.Format]
	if !ok {
		ext = ".jsonl"
	}

	var suffix string
	switch s.cfg.Rotation {
	case "daily":
		suffix = "-" + now.Format("2006-01-02")
	case "weekly":
		year, week := now.ISOWeek()
		suffix = fmt.Sprintf("-%04d-W%02d", year, week)
	case "monthly":
		suffix = "-" + now.Format("2006-01")
	case "yearly":
		suffix = "-" + now.Format("2006")
	}
	return filepath.Join(s.cfg.Dir, key+suffix+ext)
}

func (s *FileStore) formatEntry(e Entry) (string, error) {
	switch s.cfg.Format {
	case "csv":
		return strings.Join([]string{
			e.At.Format(time.RFC3339),
			csvField(e.Channel),
			csvField(e.Nick),
			string(e.Kind),
			csvField(e.MessageType),
			csvField(e.MessageID),
			csvField(e.Raw),
		}, ","), nil
	case "text":
		return fmt.Sprintf("%s %s <%s> %s",
			e.At.Format("2006-01-02T15:04:05"),
			e.Channel, e.Nick, e.Raw), nil
	default: // jsonl
		b, err := json.Marshal(e)
		return string(b), err
	}
}

// Query returns the most recent entries from the current log file.
// Only supported for "jsonl" format; other formats return nil, nil.
func (s *FileStore) Query(channel string, limit int) ([]Entry, error) {
	if s.cfg.Format != "jsonl" {
		return nil, nil
	}
	s.mu.Lock()
	path := s.filePath(keyFor(s.cfg.PerChannel, channel), time.Now())
	s.mu.Unlock()

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		if channel == "" || e.Channel == channel {
			entries = append(entries, e)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

// Close flushes and closes all open file handles.
func (s *FileStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, of := range s.files {
		_ = of.f.Close()
		delete(s.files, key)
	}
}

// PruneOld removes log files whose modification time is older than MaxAgeDays.
// No-op if MaxAgeDays is 0 or Dir is empty.
func (s *FileStore) PruneOld() error {
	if s.cfg.MaxAgeDays <= 0 || s.cfg.Dir == "" {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -s.cfg.MaxAgeDays)
	entries, err := os.ReadDir(s.cfg.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(s.cfg.Dir, de.Name()))
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func keyFor(perChannel bool, channel string) string {
	if perChannel && channel != "" {
		return sanitizeChannel(channel)
	}
	return "_all"
}

// sanitizeChannel strips "#" and replaces filesystem-unsafe characters.
func sanitizeChannel(ch string) string {
	ch = strings.TrimPrefix(ch, "#")
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, ch)
}

// csvField wraps a field in double-quotes if it contains a comma, quote, or newline.
func csvField(s string) string {
	if strings.ContainsAny(s, "\",\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}
