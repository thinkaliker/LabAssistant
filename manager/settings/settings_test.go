package settings

import (
	"os"
	"path/filepath"
	"testing"
)

// A settings file written before composeEditorLayout existed keeps its other values and gets the
// default layout, as does a fresh install.
func TestComposeEditorLayoutDefault(t *testing.T) {
	dir := t.TempDir()

	fresh, err := Load(filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fresh.Manager().ComposeEditorLayout; got != ComposeEditorModal {
		t.Errorf("fresh install: layout = %q, want %q", got, ComposeEditorModal)
	}

	old := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(old, []byte(`{"manager":{"logLevel":"debug","heartbeatSeconds":15,"statusPollSeconds":90}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(old)
	if err != nil {
		t.Fatal(err)
	}
	m := s.Manager()
	if m.ComposeEditorLayout != ComposeEditorModal {
		t.Errorf("older file: layout = %q, want %q", m.ComposeEditorLayout, ComposeEditorModal)
	}
	if m.LogLevel != "debug" || m.HeartbeatSeconds != 15 || m.StatusPollSeconds != 90 {
		t.Errorf("older file: other settings not kept: %+v", m)
	}

	m.ComposeEditorLayout = ComposeEditorPanel
	if err := s.UpdateManager(m); err != nil {
		t.Fatal(err)
	}
	again, err := Load(old)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Manager().ComposeEditorLayout; got != ComposeEditorPanel {
		t.Errorf("after save: layout = %q, want %q", got, ComposeEditorPanel)
	}
}
