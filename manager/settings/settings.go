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
	"log/slog"
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
	// HeartbeatSeconds is how often associates send host vitals; StatusPollSeconds is how often
	// they re-query module statuses. Both are pushed to connected associates when changed.
	HeartbeatSeconds  int `json:"heartbeatSeconds"`
	StatusPollSeconds int `json:"statusPollSeconds"`
	// ComposeEditorLayout is how the dashboard opens the compose editor: ComposeEditorModal
	// (full screen over the page) or ComposeEditorPanel (docked beside the stack list).
	ComposeEditorLayout string `json:"composeEditorLayout"`
}

// The compose editor layouts the dashboard knows.
const (
	ComposeEditorModal = "modal"
	ComposeEditorPanel = "panel"
)

// Defaults and bounds for the associate refresh intervals. The floor keeps a typo from turning
// every host into a busy loop of docker/apt queries; the ceiling keeps "online" meaningful.
const (
	DefaultHeartbeatSeconds  = 30
	DefaultStatusPollSeconds = 60
	MinIntervalSeconds       = 5
	MaxIntervalSeconds       = 3600
)

// Token is an API token's metadata, as listed to the dashboard (the plaintext is shown only
// once at creation). The hash that authenticates it lives on storedToken, so no API response
// can carry it.
type Token struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	// AuditAccess opts a token into reading the audit log. Off by default: the audit trail
	// can reference sensitive operations, so automation tokens don't get it implicitly.
	AuditAccess bool `json:"auditAccess"`
}

// storedToken is a token as persisted: its metadata plus the SHA-256 of the plaintext. The
// hash used to be a `json:"-"` field on Token to keep it out of API responses, which kept it
// out of settings.json too, so every token stopped authenticating at the next restart.
type storedToken struct {
	Token
	Hash string `json:"hash"`
}

type data struct {
	Manager      Manager       `json:"manager"`
	AuthUsername string        `json:"authUsername"`
	AuthPassHash string        `json:"authPassHash"`
	Tokens       []storedToken `json:"tokens"`
}

// Errors from ChangePassword.
var (
	ErrNoPassword    = errors.New("no password is set")
	ErrWrongPassword = errors.New("current password is incorrect")
	ErrEmptyPassword = errors.New("new password cannot be empty")
	// ErrPasswordTooLong is bcrypt's input limit: it refuses anything past 72 bytes.
	ErrPasswordTooLong = bcrypt.ErrPasswordTooLong
	// ErrPasswordChanged means the password changed between checking the current one and
	// storing the new one (a concurrent change); the caller should retry with the new current.
	ErrPasswordChanged = errors.New("password was changed concurrently")
)

// Store is the persisted settings + auth store.
type Store struct {
	mu   sync.RWMutex
	path string
	d    data
}

// Load reads settings from path (missing file yields defaults).
func Load(path string) (*Store, error) {
	s := &Store{path: path, d: data{Manager: Manager{
		LogLevel:            "info",
		HeartbeatSeconds:    DefaultHeartbeatSeconds,
		StatusPollSeconds:   DefaultStatusPollSeconds,
		ComposeEditorLayout: ComposeEditorModal,
	}, AuthUsername: "admin"}}
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
	s.dropHashlessTokens()
	return s, nil
}

// dropHashlessTokens removes tokens saved before their hash was persisted. Without a hash a
// token can never authenticate, so listing it would only pass a dead credential off as live;
// the warning names each one so it can be reissued.
func (s *Store) dropHashlessTokens() {
	kept := s.d.Tokens[:0]
	for _, t := range s.d.Tokens {
		if t.Hash == "" {
			slog.Warn("dropping API token saved without its hash; create a new one to replace it",
				"id", t.ID, "name", t.Name, "created", t.CreatedAt)
			continue
		}
		kept = append(kept, t)
	}
	s.d.Tokens = kept
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

// ChangePassword replaces the login password once current is verified. It refuses when no
// password is set yet (ErrNoPassword): the first one comes from `manager setpass`, so a
// dashboard left in open mode can't be claimed by whoever reaches it first.
//
// bcrypt is deliberately slow, so both hashes are computed outside the lock (holding it would
// stall every request's auth check); the swap then only lands if the hash is still the one
// that was verified.
func (s *Store) ChangePassword(current, next string) error {
	if next == "" {
		return ErrEmptyPassword
	}
	if len(next) > 72 {
		return ErrPasswordTooLong
	}
	s.mu.RLock()
	old := s.d.AuthPassHash
	s.mu.RUnlock()
	if old == "" {
		return ErrNoPassword
	}
	if bcrypt.CompareHashAndPassword([]byte(old), []byte(current)) != nil {
		return ErrWrongPassword
	}
	h, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.d.AuthPassHash != old {
		return ErrPasswordChanged
	}
	s.d.AuthPassHash = string(h)
	if err := s.save(); err != nil {
		s.d.AuthPassHash = old // keep memory and disk agreeing on the live password
		return err
	}
	return nil
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

// AddToken mints a token, returning its plaintext once. auditAccess grants audit-log read.
func (s *Store) AddToken(name string, auditAccess bool) (Token, string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, "", err
	}
	plain := hex.EncodeToString(raw)
	t := Token{ID: uuid.NewString(), Name: name, CreatedAt: time.Now(), AuditAccess: auditAccess}
	s.mu.Lock()
	s.d.Tokens = append(s.d.Tokens, storedToken{Token: t, Hash: hashToken(plain)})
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
	for i, t := range s.d.Tokens {
		out[i] = t.Token
	}
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

// TokenHasAuditAccess reports whether the matching token is allowed to read the audit log.
func (s *Store) TokenHasAuditAccess(plain string) bool {
	h := hashToken(plain)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.d.Tokens {
		if t.Hash == h {
			return t.AuditAccess
		}
	}
	return false
}

func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}
