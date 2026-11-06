package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A token must still authenticate after the manager restarts. Its hash used to be tagged
// `json:"-"`, so it never reached settings.json and every token died on the next load.
func TestTokenSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	tok, plain, err := s.AddToken("ci", true)
	if err != nil {
		t.Fatal(err)
	}

	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !again.CheckToken(plain) {
		t.Fatal("token no longer authenticates after reload")
	}
	if !again.TokenHasAuditAccess(plain) {
		t.Error("audit access lost after reload")
	}
	if again.CheckToken(plain + "x") {
		t.Error("a different token authenticated")
	}
	if l := again.ListTokens(); len(l) != 1 || l[0].ID != tok.ID || l[0].Name != "ci" {
		t.Errorf("listed tokens after reload = %+v, want the one created", l)
	}
}

// The persisted hash must stay out of what the API serves.
func TestListTokensOmitsHash(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, plain, err := s.AddToken("ci", false); err != nil {
		t.Fatal(err)
	} else {
		b, err := json.Marshal(s.ListTokens())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "hash") || strings.Contains(string(b), hashToken(plain)) {
			t.Errorf("listed tokens leak the hash: %s", b)
		}
	}
}

// Tokens written before the fix have no hash and can never authenticate; they are dropped on
// load rather than listed as if they worked. Hashed tokens alongside them are kept.
func TestLoadDropsHashlessTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := `{"tokens":[
		{"id":"old","name":"before-fix","createdAt":"2026-01-01T00:00:00Z","auditAccess":false},
		{"id":"new","name":"after-fix","createdAt":"2026-02-01T00:00:00Z","auditAccess":false,"hash":"` + hashToken("p") + `"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	l := s.ListTokens()
	if len(l) != 1 || l[0].ID != "new" {
		t.Fatalf("tokens = %+v, want only the hashed one", l)
	}
	if !s.CheckToken("p") {
		t.Error("hashed token stopped authenticating")
	}
}

func TestChangePassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	// Open mode: the first password comes from setpass, never the dashboard.
	if err := s.ChangePassword("", "new"); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("open mode: err = %v, want ErrNoPassword", err)
	}
	if s.AuthConfigured() {
		t.Fatal("open mode: a password got set")
	}

	if err := s.SetPassword("admin", "old"); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword("wrong", "new"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("wrong current: err = %v, want ErrWrongPassword", err)
	}
	if err := s.ChangePassword("old", ""); !errors.Is(err, ErrEmptyPassword) {
		t.Errorf("empty new: err = %v, want ErrEmptyPassword", err)
	}
	if err := s.ChangePassword("old", strings.Repeat("x", 73)); !errors.Is(err, ErrPasswordTooLong) {
		t.Errorf("73-byte new: err = %v, want ErrPasswordTooLong", err)
	}
	if !s.CheckLogin("admin", "old") {
		t.Fatal("a refused change altered the password")
	}

	if err := s.ChangePassword("old", "new"); err != nil {
		t.Fatalf("change: %v", err)
	}
	if s.CheckLogin("admin", "old") || !s.CheckLogin("admin", "new") {
		t.Error("in memory: old password still works or new one doesn't")
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.CheckLogin("admin", "old") || !again.CheckLogin("admin", "new") {
		t.Error("after reload: old password still works or new one doesn't")
	}
}
