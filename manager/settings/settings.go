// Package settings persists manager-wide settings and authentication material (the single
// user's password hash and API tokens) to a JSON file under data/. Passwords are stored
// as bcrypt hashes; API tokens as SHA-256 hashes.
package settings

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// Manager holds the editable, non-secret manager settings.
type Manager struct {
	LogLevel        string `json:"logLevel"`
	DefaultTimezone string `json:"defaultTimezone"`
}

// Token is an API token record (the plaintext is shown only once at creation).
type Token struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Hash      string    `json:"-"`
	CreatedAt time.Time `json:"createdAt"`
}

type data struct {
	Manager      Manager `json:"manager"`
	AuthUsername string  `json:"authUsername"`
	AuthPassHash string  `json:"authPassHash"`
	Tokens       []Token `json:"tokens"`
}

// Store is the persisted settings + auth store.
type Store struct {
	mu   sync.RWMutex
	path string
	d    data
}

// Load reads settings from path (missing file yields defaults).
func Load(path string) (*Store, error) {
	s := &Store{path: path, d: data{Manager: Manager{LogLevel: "info"}, AuthUsername: "admin"}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.d); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Manager returns the editable settings.
func (s *Store) Manager() Manager {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.d.Manager
}

// UpdateManager replaces the editable settings.
func (s *Store) UpdateManager(m Manager) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.Manager = m
	return s.save()
}

// AuthConfigured reports whether a password has been set. When false, the manager runs in
// dev-open mode (no authentication).
func (s *Store) AuthConfigured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.d.AuthPassHash != ""
}

// Username returns the configured login username.
func (s *Store) Username() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.d.AuthUsername
}

// SetPassword sets the login username and bcrypt-hashed password.
func (s *Store) SetPassword(username, password string) error {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if username != "" {
		s.d.AuthUsername = username
	}
	s.d.AuthPassHash = string(h)
	return s.save()
}

// CheckLogin verifies username + password.
func (s *Store) CheckLogin(username, password string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.d.AuthPassHash == "" || username != s.d.AuthUsername {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(s.d.AuthPassHash), []byte(password)) == nil
}

// AddToken mints a token, returning its plaintext once.
func (s *Store) AddToken(name string) (Token, string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, "", err
	}
	plain := hex.EncodeToString(raw)
	t := Token{ID: uuid.NewString(), Name: name, Hash: hashToken(plain), CreatedAt: time.Now()}
	s.mu.Lock()
	s.d.Tokens = append(s.d.Tokens, t)
	err := s.save()
	s.mu.Unlock()
	return t, plain, err
}

// RevokeToken removes a token by id.
func (s *Store) RevokeToken(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.d.Tokens {
		if t.ID == id {
			s.d.Tokens = append(s.d.Tokens[:i], s.d.Tokens[i+1:]...)
			_ = s.save()
			return true
		}
	}
	return false
}

// ListTokens returns token metadata (no hashes).
func (s *Store) ListTokens() []Token {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Token, len(s.d.Tokens))
	copy(out, s.d.Tokens)
	return out
}

// CheckToken reports whether a plaintext token matches a stored token.
func (s *Store) CheckToken(plain string) bool {
	h := hashToken(plain)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.d.Tokens {
		if t.Hash == h {
			return true
		}
	}
	return false
}

func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}
