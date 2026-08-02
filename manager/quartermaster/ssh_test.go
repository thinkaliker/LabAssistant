package quartermaster

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

// writeKey writes a fresh unencrypted ed25519 private key and returns its path.
func writeKey(t *testing.T, dir string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthMethodsPrefersKeyOverPassword(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "") // don't reach for a real agent during the test
	keyPath := writeKey(t, t.TempDir())

	s := SSHInstaller{KeyPath: keyPath}
	methods, closers := s.authMethods(InstallParams{SSHPassword: "hunter2"})
	for _, c := range closers {
		_ = c.Close()
	}
	if len(methods) != 2 {
		t.Fatalf("got %d auth methods, want key + password", len(methods))
	}
}

// A host reachable only by key must not need a password typed into the dashboard.
func TestAuthMethodsKeyOnly(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	s := SSHInstaller{KeyPath: writeKey(t, t.TempDir())}
	methods, _ := s.authMethods(InstallParams{})
	if len(methods) != 1 {
		t.Fatalf("got %d auth methods, want the key alone", len(methods))
	}
}

// With no key and no password there is nothing to offer; dial reports that rather than
// hanging on a handshake that cannot succeed.
func TestAuthMethodsNone(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	s := SSHInstaller{KeyPath: filepath.Join(t.TempDir(), "missing")}
	if methods, _ := s.authMethods(InstallParams{}); len(methods) != 0 {
		t.Fatalf("got %d auth methods, want none", len(methods))
	}
}

func TestLoadPrivateKey(t *testing.T) {
	path := writeKey(t, t.TempDir())
	if _, err := loadPrivateKey(path, ""); err != nil {
		t.Fatalf("loadPrivateKey: %v", err)
	}
	if _, err := loadPrivateKey(filepath.Join(t.TempDir(), "nope"), ""); err == nil {
		t.Error("loadPrivateKey on a missing file succeeded")
	}
}
