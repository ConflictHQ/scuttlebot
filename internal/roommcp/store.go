package roommcp

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/conflicthq/scuttlebot/internal/bots/bridge"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
	mu sync.Mutex
}
type session struct {
	Name     string           `json:"name"`
	Nick     string           `json:"nick"`
	Channels map[string]bool  `json:"channels"`
	Cursors  map[string]int64 `json:"cursors"`
	Offered  map[string]int64 `json:"offered"`
	revision string
}
type Message struct {
	Cursor string `json:"cursor"`
	At     string `json:"at"`
	Nick   string `json:"nick"`
	Text   string `json:"text"`
}

func OpenStore(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err := f.Chmod(0600); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`PRAGMA busy_timeout=5000`,
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE IF NOT EXISTS room_sessions (owner TEXT NOT NULL, name TEXT NOT NULL, state TEXT NOT NULL, PRIMARY KEY(owner,name))`,
		`CREATE TABLE IF NOT EXISTS room_messages (seq INTEGER PRIMARY KEY AUTOINCREMENT, channel TEXT NOT NULL, id TEXT NOT NULL, at TEXT NOT NULL, nick TEXT NOT NULL, text TEXT NOT NULL, UNIQUE(channel,id))`,
		`CREATE INDEX IF NOT EXISTS room_messages_channel_seq ON room_messages(channel,seq)`,
		`CREATE TABLE IF NOT EXISTS room_metadata (name TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("room store: %w", err)
		}
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) load(owner, name string) (*session, error) {
	var data string
	err := s.db.QueryRow(`SELECT state FROM room_sessions WHERE owner=? AND name=?`, owner, name).Scan(&data)
	if err != nil {
		return nil, err
	}
	var v session
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		return nil, err
	}
	v.revision = data
	return &v, nil
}
func (s *Store) save(owner string, v *session) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var result sql.Result
	if v.revision == "" {
		result, err = s.db.Exec(`INSERT OR IGNORE INTO room_sessions(owner,name,state) VALUES(?,?,?)`, owner, v.Name, string(data))
	} else {
		result, err = s.db.Exec(`UPDATE room_sessions SET state=? WHERE owner=? AND name=? AND state=?`, string(data), owner, v.Name, v.revision)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("session changed concurrently; retry the operation")
	}
	v.revision = string(data)
	return nil
}

// BindEndpoint prevents a custom stdio state path from mixing journals from
// different servers that happen to use the same channel names.
func (s *Store) BindEndpoint(endpoint string) error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO room_metadata(name,value) VALUES('endpoint',?)`, endpoint); err != nil {
		return err
	}
	var saved string
	if err := s.db.QueryRow(`SELECT value FROM room_metadata WHERE name='endpoint'`).Scan(&saved); err != nil {
		return err
	}
	if saved != endpoint {
		return fmt.Errorf("state database belongs to another server; choose a different --state path")
	}
	return nil
}

// Record captures an observed message durably. It never stores API credentials.
// The daemon calls this at bridge ingress; the stdio client also imports each
// HTTP snapshot. Server history predating observation cannot be reconstructed.
func (s *Store) Record(m bridge.Message) error {
	id := m.MsgID
	if id == "" {
		data, err := json.Marshal([]string{m.At.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), m.Nick, m.Text})
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		id = hex.EncodeToString(sum[:])
	}
	// Persist only rooms somebody has joined through this connector, rather
	// than creating a second full-fleet transcript archive.
	_, err := s.db.Exec(`INSERT OR IGNORE INTO room_messages(channel,id,at,nick,text)
		SELECT ?,?,?,?,? WHERE NOT EXISTS (
			SELECT 1 FROM room_messages WHERE channel=? AND id=?
		) AND EXISTS (
			SELECT 1 FROM room_sessions, json_each(room_sessions.state, '$.channels') AS room
			WHERE room.key=? AND room.value=1
		)`, m.Channel, id, m.At.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), m.Nick, m.Text, m.Channel, id, m.Channel)
	return err
}

func (s *Store) messages(channel string, after int64, limit int) ([]Message, bool, error) {
	rows, err := s.db.Query(`SELECT seq,at,nick,text FROM room_messages WHERE channel=? AND seq>? ORDER BY seq LIMIT ?`, channel, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Cursor, &m.At, &m.Nick, &m.Text); err != nil {
			return nil, false, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}
