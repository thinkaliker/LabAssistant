package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func load(t *testing.T, body string) (Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestLoadTLS(t *testing.T) {
	cfg, err := load(t, "tls_cert = \"/c.pem\"\ntls_key = \"/k.pem\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TLSEnabled() || cfg.TLSCert != "/c.pem" || cfg.TLSKey != "/k.pem" {
		t.Fatalf("TLS not loaded: %+v", cfg)
	}

	cfg, err = load(t, "http_addr = \":9090\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSEnabled() {
		t.Fatal("TLS enabled without cert/key")
	}

	for _, half := range []string{"tls_cert = \"/c.pem\"\n", "tls_key = \"/k.pem\"\n"} {
		if _, err := load(t, half); err == nil || !strings.Contains(err.Error(), "set together") {
			t.Errorf("%q: want a set-together error, got %v", half, err)
		}
	}
}

func TestUnknownKeys(t *testing.T) {
	valid := "http_addr = \":8080\"\ntls_cert = \"/c\"\ntls_key = \"/k\"\n[enroll]\nmode = \"local\"\n[audit]\nmax = 10\n"
	if got := unknownKeys([]byte(valid)); len(got) != 0 {
		t.Errorf("valid config reported unknown keys %v", got)
	}

	// TLS keys appended below a section header land in that table, not at the top level.
	misplaced := "http_addr = \":8080\"\n[audit]\nmax = 10\ntls_cert = \"/c\"\ntls_key = \"/k\"\ntypo_key = 1\n"
	got := unknownKeys([]byte(misplaced))
	for _, want := range []string{"audit.tls_cert", "audit.tls_key", "audit.typo_key"} {
		if !slices.Contains(got, want) {
			t.Errorf("unknownKeys = %v, missing %s", got, want)
		}
	}
	if h := misplacedHint("audit.tls_cert"); !strings.Contains(h, "above the first [section]") {
		t.Errorf("no misplacement hint for audit.tls_cert: %q", h)
	}
	if h := misplacedHint("audit.typo_key"); h != "" {
		t.Errorf("hint for a plain typo: %q", h)
	}
	if h := misplacedHint("tls_certs"); h != "" {
		t.Errorf("hint for a top-level typo: %q", h)
	}
}
