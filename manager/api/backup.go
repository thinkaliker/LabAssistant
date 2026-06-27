package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/thinkaliker/labassistant/internal/paths"
)

// Backup exports and imports the manager's persistent data (settings, hosts, tasks, module
// config, and the CA + server certificates). The bundle contains the CA private key, so it
// is sensitive and only available to authenticated callers.
type Backup struct {
	Layout paths.Layout
}

// files returns the data-relative paths included in a backup.
func (b *Backup) files() []string {
	return []string{
		"state.json", "tasks.json", "settings.json", "moduleconfig.json", "audit.log",
		filepath.Join("certs", "ca.crt"), filepath.Join("certs", "ca.key"),
		filepath.Join("certs", "server.crt"), filepath.Join("certs", "server.key"),
	}
}

func (d Deps) backup(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	for _, rel := range d.Backup.files() {
		b, err := os.ReadFile(filepath.Join(d.Backup.Layout.Data, rel))
		if err != nil {
			continue // skip files that don't exist yet
		}
		out[rel] = string(b)
	}
	d.Aud.Record("backup_exported", "", "user", "settings backup exported", nil)
	w.Header().Set("Content-Disposition", `attachment; filename="labassistant-backup.json"`)
	writeJSON(w, http.StatusOK, map[string]any{"files": out})
}

func (d Deps) restore(w http.ResponseWriter, r *http.Request) {
	var bundle struct {
		Files map[string]string `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&bundle); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid bundle")
		return
	}
	allowed := map[string]bool{}
	for _, f := range d.Backup.files() {
		allowed[f] = true
	}
	for rel, content := range bundle.Files {
		if !allowed[rel] {
			continue // ignore unexpected paths (no traversal)
		}
		dst := filepath.Join(d.Backup.Layout.Data, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			writeErr(w, http.StatusInternalServerError, "error", err.Error())
			return
		}
		if err := os.WriteFile(dst, []byte(content), 0o600); err != nil {
			writeErr(w, http.StatusInternalServerError, "error", err.Error())
			return
		}
	}
	d.Aud.Record("backup_restored", "", "user", "settings restored from backup", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored", "note": "restart the manager to apply"})
}
