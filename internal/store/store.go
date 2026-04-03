// Package store provides a thin database/sql wrapper for scuttlebot's
// persistent state: agent registry, admin accounts, and policies.
// Both SQLite and PostgreSQL are supported.
package store

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// AgentRow is the flat database representation of a registered agent.
type AgentRow struct {
	Nick      string
	Type      string
	Config    []byte // JSON-encoded EngagementConfig
	CreatedAt time.Time
	Revoked   bool
	LastSeen  *time.Time
}

// AdminRow is the flat database representation of an admin account.
type AdminRow struct {
	Username  string
	Hash      []byte // bcrypt hash
	CreatedAt time.Time
}

// Store wraps a sql.DB with scuttlebot-specific CRUD operations.
type Store struct {
	db     *sql.DB
	driver string
}

// Open opens a database connection, runs schema migrations, and returns a Store.
// driver must be "sqlite" or "postgres". dsn is the connection string.
func Open(driver, dsn string) (*Store, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", driver, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", driver, err)
	}
	s := &Store{db: db, driver: driver}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error { return s.db.Close() }

// ph returns the query placeholder for argument n (1-indexed).
// SQLite uses "?"; PostgreSQL uses "$1", "$2", …
func (s *Store) ph(n int) string {
	if s.driver == "postgres" {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agents (
			nick       TEXT PRIMARY KEY,
			type       TEXT NOT NULL,
			config     TEXT NOT NULL,
			created_at TEXT NOT NULL,
			revoked    INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS admins (
			username   TEXT PRIMARY KEY,
			hash       TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS policies (
			id   INTEGER PRIMARY KEY,
			data TEXT NOT NULL
		)`,
	}
	// Run base schema.
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// Additive migrations — safe to re-run.
	addColumns := []string{
		`ALTER TABLE agents ADD COLUMN last_seen TEXT`,
	}
	for _, stmt := range addColumns {
		_, _ = s.db.Exec(stmt) // ignore "column already exists"
	}
	return nil
}

// AgentUpsert inserts or updates an agent row by nick.
func (s *Store) AgentUpsert(r *AgentRow) error {
	revoked := 0
	if r.Revoked {
		revoked = 1
	}
	var lastSeen string
	if r.LastSeen != nil {
		lastSeen = r.LastSeen.UTC().Format(time.RFC3339Nano)
	}
	q := fmt.Sprintf(
		`INSERT INTO agents (nick, type, config, created_at, revoked, last_seen)
		 VALUES (%s, %s, %s, %s, %s, %s)
		 ON CONFLICT(nick) DO UPDATE SET
		   type=EXCLUDED.type, config=EXCLUDED.config,
		   created_at=EXCLUDED.created_at, revoked=EXCLUDED.revoked,
		   last_seen=EXCLUDED.last_seen`,
		s.ph(1), s.ph(2), s.ph(3), s.ph(4), s.ph(5), s.ph(6),
	)
	_, err := s.db.Exec(q,
		r.Nick, r.Type, string(r.Config),
		r.CreatedAt.UTC().Format(time.RFC3339), revoked, lastSeen,
	)
	return err
}

// AgentDelete removes an agent row entirely.
func (s *Store) AgentDelete(nick string) error {
	_, err := s.db.Exec(
		fmt.Sprintf(`DELETE FROM agents WHERE nick=%s`, s.ph(1)),
		nick,
	)
	return err
}

// AgentList returns all agent rows, including revoked ones.
func (s *Store) AgentList() ([]*AgentRow, error) {
	rows, err := s.db.Query(`SELECT nick, type, config, created_at, revoked, COALESCE(last_seen,'') FROM agents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*AgentRow
	for rows.Next() {
		var r AgentRow
		var cfg, ts, lastSeenStr string
		var revokedInt int
		if err := rows.Scan(&r.Nick, &r.Type, &cfg, &ts, &revokedInt, &lastSeenStr); err != nil {
			return nil, err
		}
		r.Config = []byte(cfg)
		r.Revoked = revokedInt != 0
		r.CreatedAt, err = time.Parse(time.RFC3339, ts)
		if err != nil {
			return nil, fmt.Errorf("store: agent %s timestamp: %w", r.Nick, err)
		}
		if lastSeenStr != "" {
			if t, err := time.Parse(time.RFC3339Nano, lastSeenStr); err == nil {
				r.LastSeen = &t
			}
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// AdminUpsert inserts or updates an admin row. The bcrypt hash is stored as base64.
func (s *Store) AdminUpsert(r *AdminRow) error {
	q := fmt.Sprintf(
		`INSERT INTO admins (username, hash, created_at)
		 VALUES (%s, %s, %s)
		 ON CONFLICT(username) DO UPDATE SET hash=EXCLUDED.hash, created_at=EXCLUDED.created_at`,
		s.ph(1), s.ph(2), s.ph(3),
	)
	_, err := s.db.Exec(q,
		r.Username,
		base64.StdEncoding.EncodeToString(r.Hash),
		r.CreatedAt.UTC().Format(time.RFC3339),
	)
	return err
}

// AdminDelete removes an admin row.
func (s *Store) AdminDelete(username string) error {
	_, err := s.db.Exec(
		fmt.Sprintf(`DELETE FROM admins WHERE username=%s`, s.ph(1)),
		username,
	)
	return err
}

// AdminList returns all admin rows.
func (s *Store) AdminList() ([]*AdminRow, error) {
	rows, err := s.db.Query(`SELECT username, hash, created_at FROM admins`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*AdminRow
	for rows.Next() {
		var r AdminRow
		var hashB64, ts string
		if err := rows.Scan(&r.Username, &hashB64, &ts); err != nil {
			return nil, err
		}
		r.Hash, err = base64.StdEncoding.DecodeString(hashB64)
		if err != nil {
			return nil, fmt.Errorf("store: admin %s hash decode: %w", r.Username, err)
		}
		r.CreatedAt, err = time.Parse(time.RFC3339, ts)
		if err != nil {
			return nil, fmt.Errorf("store: admin %s timestamp: %w", r.Username, err)
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// PolicyGet returns the raw JSON blob for the singleton policy record.
// Returns nil, nil if no policies have been saved yet.
func (s *Store) PolicyGet() ([]byte, error) {
	var data string
	err := s.db.QueryRow(`SELECT data FROM policies WHERE id=1`).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

// PolicySet writes the raw JSON blob for the singleton policy record.
func (s *Store) PolicySet(data []byte) error {
	q := fmt.Sprintf(
		`INSERT INTO policies (id, data) VALUES (1, %s)
		 ON CONFLICT(id) DO UPDATE SET data=EXCLUDED.data`,
		s.ph(1),
	)
	_, err := s.db.Exec(q, string(data))
	return err
}
