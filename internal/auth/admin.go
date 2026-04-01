// Package auth provides admin account management with bcrypt-hashed passwords.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Admin is a single admin account record.
type Admin struct {
	Username string    `json:"username"`
	Hash     []byte    `json:"hash"`
	Created  time.Time `json:"created"`
}

// AdminStore persists admin accounts to a JSON file.
type AdminStore struct {
	mu   sync.RWMutex
	path string
	data []Admin
}

// NewAdminStore loads (or creates) the admin store at the given path.
func NewAdminStore(path string) (*AdminStore, error) {
	s := &AdminStore{path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// IsEmpty reports whether there are no admin accounts.
func (s *AdminStore) IsEmpty() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data) == 0
}

// Add adds a new admin account. Returns an error if the username already exists.
func (s *AdminStore) Add(username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, a := range s.data {
		if a.Username == username {
			return fmt.Errorf("admin %q already exists", username)
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("admin: hash password: %w", err)
	}

	s.data = append(s.data, Admin{
		Username: username,
		Hash:     hash,
		Created:  time.Now().UTC(),
	})
	return s.save()
}

// SetPassword updates the password for an existing admin.
func (s *AdminStore) SetPassword(username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, a := range s.data {
		if a.Username == username {
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if err != nil {
				return fmt.Errorf("admin: hash password: %w", err)
			}
			s.data[i].Hash = hash
			return s.save()
		}
	}
	return fmt.Errorf("admin %q not found", username)
}

// Remove removes an admin account. Returns an error if not found.
func (s *AdminStore) Remove(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, a := range s.data {
		if a.Username == username {
			s.data = append(s.data[:i], s.data[i+1:]...)
			return s.save()
		}
	}
	return fmt.Errorf("admin %q not found", username)
}

// List returns a snapshot of all admin accounts.
func (s *AdminStore) List() []Admin {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Admin, len(s.data))
	copy(out, s.data)
	return out
}

// Authenticate returns true if the username/password pair is valid.
func (s *AdminStore) Authenticate(username, password string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, a := range s.data {
		if a.Username == username {
			return bcrypt.CompareHashAndPassword(a.Hash, []byte(password)) == nil
		}
	}
	return false
}

func (s *AdminStore) load() error {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("admin store: read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return fmt.Errorf("admin store: parse: %w", err)
	}
	return nil
}

func (s *AdminStore) save() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0600)
}
